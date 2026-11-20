package polling

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ledatu/csar-botverify/internal/provider"
	"github.com/ledatu/csar-botverify/internal/provider/telegram"
	"github.com/ledatu/csar-core/lock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

type testClient struct {
	get func(context.Context, int64) ([]telegram.Update, error)
}

func (c testClient) GetUpdates(ctx context.Context, offset int64) ([]telegram.Update, error) {
	return c.get(ctx, offset)
}
func (testClient) CheckPolling(context.Context) error { return nil }

type testProcessor func(context.Context, string, *provider.IncomingMessage) error

func (p testProcessor) Process(ctx context.Context, name string, msg *provider.IncomingMessage) error {
	return p(ctx, name, msg)
}

func receiverForTest(client Client, processor Processor) *Receiver {
	r := &Receiver{Client: client, Processor: processor, FailoverAfter: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r.RegisterMetrics(prometheus.NewRegistry())
	return r
}

func TestReceiveRetriesEarlierUpdateBeforeAdvancingOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var updates []telegram.Update
	if err := json.Unmarshal([]byte(`[
		{"update_id":1,"message":{"from":{"id":1},"chat":{"id":1},"text":"FIRST"}},
		{"update_id":2,"message":{"from":{"id":1},"chat":{"id":1},"text":"SECOND"}}
	]`), &updates); err != nil {
		t.Fatal(err)
	}
	var offsets []int64
	client := testClient{get: func(_ context.Context, offset int64) ([]telegram.Update, error) {
		offsets = append(offsets, offset)
		if len(offsets) == 3 {
			cancel()
			return nil, context.Canceled
		}
		return updates, nil
	}}
	var processed []string
	failFirst := true
	processor := testProcessor(func(_ context.Context, _ string, msg *provider.IncomingMessage) error {
		processed = append(processed, msg.Text)
		if failFirst {
			failFirst = false
			return errors.New("temporary confirm failure")
		}
		return nil
	})
	r := receiverForTest(client, processor)
	if err := r.receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected receive result: %v", err)
	}
	if !reflect.DeepEqual(offsets, []int64{0, 0, 3}) ||
		!reflect.DeepEqual(processed, []string{"FIRST", "FIRST", "SECOND"}) {
		t.Fatalf("failed update was skipped: offsets=%v processed=%v", offsets, processed)
	}
}

func TestSessionLossCancelsInFlightPollAndPermitsFailover(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	locker := lock.NewRedisLocker(rdb)
	acquire := func(owner string) *lock.Session {
		t.Helper()
		session, err := locker.AcquireSession(context.Background(), lock.Request{
			Key: "test-poller", Module: "test", Owner: owner,
			LeaseTTL: time.Second, HeartbeatInterval: 20 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	session := acquire("first")
	if _, err := locker.AcquireSession(context.Background(), lock.Request{
		Key: "test-poller", Module: "test", Owner: "second",
		LeaseTTL: time.Second, HeartbeatInterval: 20 * time.Millisecond,
	}); !errors.Is(err, lock.ErrLockNotAcquired) {
		t.Fatalf("second receiver obtained active session: %v", err)
	}
	started := make(chan struct{})
	var canceled atomic.Bool
	client := testClient{get: func(ctx context.Context, _ int64) ([]telegram.Update, error) {
		close(started)
		<-ctx.Done()
		canceled.Store(true)
		return nil, ctx.Err()
	}}
	r := receiverForTest(client, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := r.serveSession(context.Background(), session); !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected lost-session result: %v", err)
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	mr.Del("test-poller")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lost ownership did not cancel poll")
	}
	if !canceled.Load() || r.active.Load() {
		t.Fatal("receiver remained active after ownership loss")
	}
	replacement := acquire("replacement")
	if err := replacement.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentDeliveryFailureReleasesLeadership(t *testing.T) {
	for _, stage := range []string{"receive", "process"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			locker := lock.NewRedisLocker(rdb)
			req := lock.Request{
				Key: "failed-receiver", Module: "test", Owner: "first",
				LeaseTTL: time.Second, HeartbeatInterval: 20 * time.Millisecond,
			}
			session, err := locker.AcquireSession(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var updates []telegram.Update
			if err := json.Unmarshal([]byte(`[{"update_id":1,"message":{"from":{"id":1},"chat":{"id":1},"text":"CODE"}}]`), &updates); err != nil {
				t.Fatal(err)
			}
			client := testClient{get: func(context.Context, int64) ([]telegram.Update, error) {
				if stage == "receive" {
					return nil, errors.New("network unavailable")
				}
				return updates, nil
			}}
			r := receiverForTest(client, testProcessor(func(context.Context, string, *provider.IncomingMessage) error {
				return errors.New("CSAR unavailable")
			}))
			r.FailoverAfter = 5 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
			defer cancel()
			if err := r.serveSession(ctx, session); err == nil || ctx.Err() != nil {
				t.Fatalf("persistent failure did not yield ownership in time: %v", err)
			}
			req.Owner = "replacement"
			replacement, err := locker.AcquireSession(context.Background(), req)
			if err != nil {
				t.Fatalf("healthy peer cannot take ownership: %v", err)
			}
			if err := replacement.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
