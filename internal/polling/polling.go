// Package polling receives Telegram updates under one renewable owner session.
package polling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ledatu/csar-botverify/internal/provider"
	"github.com/ledatu/csar-botverify/internal/provider/telegram"
	"github.com/ledatu/csar-core/health"
	"github.com/ledatu/csar-core/lock"
	"github.com/prometheus/client_golang/prometheus"
)

type Client interface {
	GetUpdates(context.Context, int64) ([]telegram.Update, error)
	CheckPolling(context.Context) error
}

type Processor interface {
	Process(context.Context, string, *provider.IncomingMessage) error
}

// Receiver keeps the offset in memory. Telegram retains its last acknowledged
// offset across replica failover. An unacknowledged update is safely replayed.
type Receiver struct {
	Client        Client
	Processor     Processor
	Locker        lock.SessionLocker
	Key           string
	Owner         string
	Logger        *slog.Logger
	FailoverAfter time.Duration

	active       atomic.Bool
	lastPoll     atomic.Int64
	lastElection atomic.Int64
	activeMetric prometheus.Gauge
	pollMetric   prometheus.Gauge
	updateMetric prometheus.Gauge
	errorsMetric *prometheus.CounterVec
}

// RegisterMetrics installs process-local metrics in the supplied registry.
func (r *Receiver) RegisterMetrics(reg prometheus.Registerer) {
	r.activeMetric = prometheus.NewGauge(prometheus.GaugeOpts{Name: "botverify_polling_active", Help: "Whether this replica holds the polling session."})
	r.pollMetric = prometheus.NewGauge(prometheus.GaugeOpts{Name: "botverify_polling_last_success_timestamp_seconds", Help: "Last successful Telegram poll."})
	r.updateMetric = prometheus.NewGauge(prometheus.GaugeOpts{Name: "botverify_polling_last_processed_timestamp_seconds", Help: "Last successfully processed update."})
	r.errorsMetric = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "botverify_polling_errors_total", Help: "Polling errors by stage."}, []string{"stage"})
	reg.MustRegister(r.activeMetric, r.pollMetric, r.updateMetric, r.errorsMetric)
}

func (r *Receiver) recordError(stage string, err error) {
	r.errorsMetric.WithLabelValues(stage).Inc()
	r.Logger.Warn("Telegram polling failed", "stage", stage, "error", err)
}

// Readiness detects a stalled active receiver and a stalled standby election loop.
func (r *Receiver) Readiness() health.CheckStatus {
	last := r.lastElection.Load()
	if r.active.Load() {
		last = r.lastPoll.Load()
	}
	if last == 0 || time.Since(time.Unix(last, 0)) > time.Minute {
		return health.CheckStatus{Status: "fail", Detail: "polling progress is stale"}
	}
	return health.CheckStatus{Status: "ok"}
}

// Run is canceled with the HTTP server. Session ownership loss cancels every
// in-flight Telegram and confirmation request before this replica reacquires.
func (r *Receiver) Run(ctx context.Context) {
	for ctx.Err() == nil {
		acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		session, err := r.Locker.AcquireSession(acquireCtx, lock.Request{
			Key: r.Key, Module: "botverify-polling", Owner: r.Owner,
			LeaseTTL: 30 * time.Second, HeartbeatInterval: 5 * time.Second,
		})
		cancel()
		if err != nil {
			if errors.Is(err, lock.ErrLockNotAcquired) {
				r.lastElection.Store(time.Now().Unix())
			} else {
				r.recordError("leadership", err)
			}
			if !wait(ctx, 3*time.Second) {
				return
			}
			continue
		}
		err = r.serveSession(ctx, session)
		if ctx.Err() != nil {
			return
		}
		delay := 3 * time.Second
		if err != nil {
			r.recordError("session", err)
			// Yield to healthy peers before this receiver competes again.
			delay = retryDelay(err)
			if delay < 30*time.Second {
				delay = 30 * time.Second
			}
		}
		if !wait(ctx, delay) {
			return
		}
	}
}

func (r *Receiver) serveSession(ctx context.Context, session *lock.Session) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-session.Done():
			cancel()
		case <-done:
		}
	}()
	r.active.Store(true)
	r.activeMetric.Set(1)
	defer func() {
		cancel()
		close(done)
		r.active.Store(false)
		r.activeMetric.Set(0)
		if err := session.Release(context.Background()); err != nil {
			r.recordError("release", err)
		}
	}()

	if err := r.Client.CheckPolling(sessionCtx); err != nil {
		r.recordError("webhook", err)
		return err
	}
	return r.receive(sessionCtx)
}

func (r *Receiver) receive(ctx context.Context) error {
	var offset int64
	var failedSince time.Time
	for ctx.Err() == nil {
		if !failedSince.IsZero() && time.Since(failedSince) >= r.FailoverAfter {
			return fmt.Errorf("polling delivery stalled beyond failover window")
		}
		updates, err := r.Client.GetUpdates(ctx, offset)
		if err != nil {
			r.recordError("receive", err)
			if failedSince.IsZero() {
				failedSince = time.Now()
			}
			delay := retryDelay(err)
			if delay >= r.FailoverAfter {
				return err
			}
			if !wait(ctx, delay) {
				return ctx.Err()
			}
			continue
		}
		if len(updates) == 0 {
			failedSince = time.Time{}
		}
		now := time.Now().Unix()
		r.lastPoll.Store(now)
		r.pollMetric.Set(float64(now))
		for _, update := range updates {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if update.ID < offset {
				continue
			}
			if message := update.Incoming(); message != nil {
				if err := r.Processor.Process(ctx, "telegram", message); err != nil {
					r.recordError("process", err)
					if failedSince.IsZero() {
						failedSince = time.Now()
					}
					delay := retryDelay(err)
					if delay >= r.FailoverAfter {
						return err
					}
					if !wait(ctx, delay) {
						return ctx.Err()
					}
					// Stop this batch. A later offset must never acknowledge a
					// failed earlier update.
					break
				}
			}
			failedSince = time.Time{}
			offset = update.ID + 1
			r.updateMetric.Set(float64(time.Now().Unix()))
		}
	}
	return ctx.Err()
}

func retryDelay(err error) time.Duration {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 3*time.Second {
		return apiErr.RetryAfter
	}
	return 3 * time.Second
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Validate checks the runtime dependencies before any Telegram mutation.
func (r *Receiver) Validate() error {
	if r.Client == nil || r.Processor == nil || r.Locker == nil || r.Key == "" || r.Owner == "" || r.FailoverAfter <= 0 {
		return fmt.Errorf("polling receiver dependencies are required")
	}
	return nil
}
