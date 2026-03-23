package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/ledatu/aurumskynet-core/app"
	"github.com/ledatu/csar-core/jwtx"
	"github.com/ledatu/csar-core/stsclient"

	"github.com/ledatu/csar-botverify/internal/config"
	"github.com/ledatu/csar-botverify/internal/provider"
	"github.com/ledatu/csar-botverify/internal/provider/telegram"
	"github.com/ledatu/csar-botverify/internal/relay"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	a := app.New("", logger,
		app.WithoutAMQP(),
		app.WithoutPostgres(),
	)

	a.RegisterRoutes(func(r chi.Router) {
		custom := parseCustomConfig(a.Config.Custom, logger)

		kp, err := jwtx.LoadKeyPairFromPEM(custom.JWT.PrivateKeyFile, custom.JWT.PublicKeyFile)
		if err != nil {
			logger.Error("load jwt keys", "error", err)
			os.Exit(1)
		}

		internalTransport := buildInternalTransport(custom, logger)

		ts, err := stsclient.New(&stsclient.Config{
			STSEndpoint:       custom.STSEndpoint,
			Audience:          custom.Audience,
			ServiceName:       custom.ServiceName,
			AssertionAudience: custom.AssertionAudience,
			KeyPair:           kp,
			AssertionTTL:      4 * time.Minute,
			HTTPClient: &http.Client{
				Transport: internalTransport,
				Timeout:   30 * time.Second,
			},
			Logger: logger,
		})
		if err != nil {
			logger.Error("sts client", "error", err)
			os.Exit(1)
		}

		stsHTTPClient := &http.Client{
			Transport: ts.Transport(internalTransport),
			Timeout:   30 * time.Second,
		}

		providers := make(map[string]provider.BotProvider)
		for _, pc := range custom.Providers {
			switch pc.Name {
			case "telegram":
				tg := telegram.New(pc.BotToken, pc.WebhookSecret, logger)
				providers["telegram"] = tg

				if pc.WebhookURL != "" {
					if err := tg.RegisterWebhook(context.Background(), pc.WebhookURL); err != nil {
						logger.Error("failed to register telegram webhook", "error", err)
						os.Exit(1)
					}
				}
			default:
				logger.Warn("unknown bot provider, skipping", "name", pc.Name)
			}
		}

		confirmURL := custom.RouterBaseURL + "/svc/authn/bot-verify/confirm"
		relayHandler := relay.New(providers, confirmURL, stsHTTPClient, logger)

		r.Post("/webhook/{provider}", relayHandler.HandleWebhook)
	})

	if err := a.Run(); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func parseCustomConfig(node yaml.Node, logger *slog.Logger) config.Custom {
	var c config.Custom
	if err := node.Decode(&c); err != nil {
		logger.Error("failed to parse custom config", "error", err)
		os.Exit(1)
	}
	return c
}

func buildInternalTransport(custom config.Custom, logger *slog.Logger) http.RoundTripper {
	if custom.RouterTLS.CAFile == "" {
		return http.DefaultTransport
	}

	def, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		logger.Error("http.DefaultTransport is not *http.Transport")
		os.Exit(1)
	}
	tr := def.Clone()
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(custom.RouterTLS.CAFile)
	if err != nil {
		logger.Error("read router tls ca", "error", err)
		os.Exit(1)
	}
	if !pool.AppendCertsFromPEM(pem) {
		logger.Error("parse router tls ca", "file", custom.RouterTLS.CAFile)
		os.Exit(1)
	}
	tr.TLSClientConfig = &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	return tr
}
