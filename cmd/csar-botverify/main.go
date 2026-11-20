// csar-botverify bridges Telegram (and future) bot webhooks with the
// csar-authn bot-verify confirm endpoint. It receives platform webhook
// callbacks, extracts a verification code from user messages, and
// relays a confirm call to csar-authn via the csar router using STS
// service tokens.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/ledatu/csar-botverify/internal/polling"
	"github.com/ledatu/csar-core/lock"
	"github.com/ledatu/csar-core/redisutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ledatu/csar-core/configload"
	"github.com/ledatu/csar-core/gatewayctx"
	"github.com/ledatu/csar-core/health"
	"github.com/ledatu/csar-core/httpmiddleware"
	"github.com/ledatu/csar-core/httpserver"
	"github.com/ledatu/csar-core/logutil"
	"github.com/ledatu/csar-core/observe"
	"github.com/ledatu/csar-core/stsclient"
	"github.com/ledatu/csar-core/tlsx"

	"github.com/ledatu/csar-botverify/internal/config"
	"github.com/ledatu/csar-botverify/internal/provider"
	"github.com/ledatu/csar-botverify/internal/provider/telegram"
	"github.com/ledatu/csar-botverify/internal/relay"
)

var Version = "dev"

func main() {
	sf := configload.NewSourceFlags()
	sf.RegisterFlags(flag.CommandLine)
	flag.Parse()

	inner := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(logutil.NewRedactingHandler(inner))

	if err := run(sf, logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(sf *configload.SourceFlags, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srcParams := sf.SourceParams()
	cfg, err := configload.LoadInitial(ctx, &srcParams, logger, config.LoadFromBytes)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	logger.Info("config loaded",
		"service", cfg.Service.Name,
		"port", cfg.Service.Port,
		"tls", cfg.TLS.IsEnabled(),
	)

	// --- Observability ---
	tp, err := observe.InitTracer(ctx, observe.TraceConfig{
		ServiceName:    cfg.Service.Name,
		ServiceVersion: Version,
		Endpoint:       cfg.Tracing.Endpoint,
		SampleRate:     cfg.Tracing.SampleRate,
		Insecure:       true,
	})
	if err != nil {
		return fmt.Errorf("initializing tracer: %w", err)
	}
	defer func() { _ = tp.Close() }()

	// --- Authenticated router client via STS ---
	rc, err := stsclient.NewRouterClient(&cfg.ServiceAuth, logger)
	if err != nil {
		return fmt.Errorf("service auth: %w", err)
	}

	// --- Bot providers ---
	providers := make(map[string]provider.BotProvider)
	var pollingProvider *telegram.Provider
	var pollingKey string
	for _, pc := range cfg.Custom.Providers {
		switch pc.Name {
		case "telegram":
			tg := telegram.New(pc.BotToken, pc.WebhookSecret, logger)
			if err := tg.ConfigureNetwork(pc.Network); err != nil {
				return err
			}
			if pc.Mode != "webhook" {
				pollingProvider = tg
				botID, _, _ := strings.Cut(pc.BotToken, ":")
				pollingKey = "botverify:telegram:polling:" + botID
			}
			providers["telegram"] = tg
			if pc.Mode == "webhook" && pc.WebhookURL != "" {
				if err := tg.RegisterWebhook(ctx, pc.WebhookURL); err != nil {
					return fmt.Errorf("registering telegram webhook: %w", err)
				}
			}
		default:
			logger.Warn("unknown bot provider, skipping", "name", pc.Name)
		}
	}

	confirmURL := rc.BaseURL + "/svc/authn/bot-verify/confirm"
	relayHandler := relay.New(providers, confirmURL, rc.Client, logger)

	// --- Router ---
	r := chi.NewRouter()
	r.Use(
		httpmiddleware.RequestID,
		httpmiddleware.AccessLog(logger),
		httpmiddleware.Recover(logger),
		httpmiddleware.MaxBodySize(1<<20),
		gatewayctx.Middleware,
	)
	readiness := health.NewReadinessChecker(Version, true)
	registry := prometheus.NewRegistry()
	pollingEnabled := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "botverify_polling_enabled", Help: "Whether this replica is configured to receive Telegram polls.",
	})
	registry.MustRegister(pollingEnabled)
	if cfg.Custom.PollingActive {
		pollingEnabled.Set(1)
	}
	metrics := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	var pollingDone chan struct{}
	if cfg.Custom.PollingActive {
		redisCtx, redisCancel := context.WithTimeout(ctx, 5*time.Second)
		rdb, err := redisutil.NewClient(redisCtx, cfg.Custom.Redis)
		redisCancel()
		if err != nil {
			return fmt.Errorf("polling Redis: %w", err)
		}
		defer func() { _ = rdb.Close() }()
		owner, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("polling owner: %w", err)
		}
		receiver := &polling.Receiver{
			Client: pollingProvider, Processor: relayHandler,
			Locker: lock.NewRedisLocker(rdb), Key: pollingKey, Owner: owner, Logger: logger,
		}
		if err := receiver.Validate(); err != nil {
			return err
		}
		receiver.RegisterMetrics(registry)
		readiness.Register("polling", receiver.Readiness)
		readiness.Register("polling_redis", func() health.CheckStatus {
			checkCtx, checkCancel := context.WithTimeout(ctx, time.Second)
			defer checkCancel()
			if err := rdb.Ping(checkCtx).Err(); err != nil {
				return health.CheckStatus{Status: "fail", Detail: "polling Redis unavailable"}
			}
			return health.CheckStatus{Status: "ok"}
		})
		pollingDone = make(chan struct{})
		go func() {
			defer close(pollingDone)
			receiver.Run(ctx)
		}()
		defer func() {
			cancel()
			<-pollingDone
		}()
	}
	readiness.Register("http_server", health.TCPDialCheck(fmt.Sprintf(":%d", cfg.Service.Port), time.Second))
	if cfg.HealthPort == 0 {
		r.Handle("/metrics", metrics)
		r.Get("/health", health.Handler(Version))
		r.Get("/readiness", readiness.Handler())
	}
	r.Post("/webhook/{provider}", relayHandler.HandleWebhook)

	var healthSidecar *health.Sidecar
	if cfg.HealthPort > 0 {
		var err error
		healthSidecar, err = health.NewSidecar(health.SidecarConfig{
			Addr:      fmt.Sprintf(":%d", cfg.HealthPort),
			Version:   Version,
			Readiness: readiness,
			Metrics:   metrics,
			Logger:    logger,
		})
		if err != nil {
			return fmt.Errorf("health sidecar: %w", err)
		}
		go func() {
			if err := healthSidecar.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("health sidecar error", "error", err)
			}
		}()
		logger.Info("health sidecar started", "port", cfg.HealthPort)
	}

	// --- HTTP server ---
	addr := fmt.Sprintf(":%d", cfg.Service.Port)

	var tlsCfg *tlsx.ServerConfig
	if cfg.TLS.IsEnabled() {
		tlsCfg = &tlsx.ServerConfig{
			CertFile:     cfg.TLS.CertFile,
			KeyFile:      cfg.TLS.KeyFile,
			ClientCAFile: cfg.TLS.ClientCAFile,
			MinVersion:   cfg.TLS.MinVersion,
		}
	}

	srv, err := httpserver.New(&httpserver.Config{
		Addr:    addr,
		Handler: r,
		TLS:     tlsCfg,
	}, logger)
	if err != nil {
		return fmt.Errorf("creating server: %w", err)
	}

	logger.Info("service started", "name", cfg.Service.Name, "port", cfg.Service.Port, "tls", cfg.TLS.IsEnabled())
	runErr := srv.Run(ctx)
	cancel()
	if healthSidecar != nil {
		hctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if serr := healthSidecar.Shutdown(hctx); serr != nil {
			logger.Error("health sidecar shutdown error", "error", serr)
		}
	}
	return runErr
}
