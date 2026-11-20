// Package relay handles bot webhook requests and forwards verification
// confirmations to csar-authn via the csar router.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ledatu/csar-botverify/internal/provider"
)

// startMessage answers a bare /start. The bot's intended entry point is a
// t.me/<bot>?start=<code> deep link from the login page, so a user arriving any
// other way has no code attached and needs pointing back to it.
const startMessage = "Это бот подтверждения входа AURUMSKYNET.\n\n" +
	"Отправьте сюда код подтверждения со страницы входа — или откройте бота по ссылке с кодом, которую вы получили в браузере."

// Handler routes incoming bot webhooks to the appropriate provider and relays
// confirmation to csar-authn.
type Handler struct {
	providers     map[string]provider.BotProvider
	confirmURL    string
	stsHTTPClient *http.Client
	logger        *slog.Logger
}

// New creates a relay Handler.
// confirmURL is the full URL to the csar-authn confirm endpoint via the router
// (e.g. "https://api.aurum-sky.net/svc/authn/bot-verify/confirm").
func New(providers map[string]provider.BotProvider, confirmURL string, stsHTTPClient *http.Client, logger *slog.Logger) *Handler {
	return &Handler{
		providers:     providers,
		confirmURL:    confirmURL,
		stsHTTPClient: stsHTTPClient,
		logger:        logger.With("component", "relay"),
	}
}

// HandleWebhook handles POST /webhook/{provider}.
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	bp, ok := h.providers[providerName]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	if !bp.ValidateWebhook(r) {
		h.logger.Warn("webhook validation failed", "provider", providerName)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	msg, err := bp.ParseWebhook(r)
	if err != nil {
		h.logger.Warn("failed to parse webhook", "provider", providerName, "error", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.Process(r.Context(), providerName, msg); err != nil {
		h.logger.Error("bot message processing failed", "provider", providerName, "error", err)
		http.Error(w, "temporary processing failure", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Process is shared by webhook and polling delivery. An error prevents the
// transport from acknowledging this update.
func (h *Handler) Process(ctx context.Context, providerName string, msg *provider.IncomingMessage) error {
	bp, ok := h.providers[providerName]
	if !ok {
		return fmt.Errorf("unknown bot provider")
	}
	code := strings.TrimSpace(strings.ToUpper(msg.Text))
	reply := ""
	if code == "" {
		if !msg.IsStart {
			return nil
		}
		reply = startMessage
	} else {
		status, err := h.confirm(ctx, code, providerName, msg.ProviderUserID, msg.DisplayName)
		if err != nil {
			return err
		}
		switch status {
		case http.StatusOK:
			h.logger.Info("verification confirmed", "provider", providerName)
			reply = "Код принят. Вернитесь в браузер и завершите вход."
		case http.StatusNotFound:
			reply = "Код недействителен или уже истёк. Запросите новый код и попробуйте снова."
		default:
			return fmt.Errorf("confirm returned HTTP %d", status)
		}
	}
	if err := bp.SendReply(ctx, msg.ChatID, reply); err != nil {
		h.logger.Warn("bot reply failed", "provider", providerName, "error", err)
		var permanent interface {
			error
			Permanent() bool
		}
		if errors.As(err, &permanent) && permanent.Permanent() {
			return nil
		}
		return err
	}
	return nil
}

func (h *Handler) confirm(ctx context.Context, code, providerName, providerUserID, displayName string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{
		"code":             code,
		"provider":         providerName,
		"provider_user_id": providerUserID,
		"display_name":     displayName,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.confirmURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.stsHTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("calling confirm: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, nil
}
