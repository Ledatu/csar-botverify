// Package relay handles bot webhook requests and forwards verification
// confirmations to csar-authn via the csar router.
package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ledatu/csar-botverify/internal/provider"
)

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

	code := strings.TrimSpace(strings.ToUpper(msg.Text))
	if code == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	status, err := h.confirm(r, code, providerName, msg.ProviderUserID, msg.DisplayName)
	if err != nil {
		h.logger.Error("confirm call failed", "provider", providerName, "error", err)
		_ = bp.SendReply(r.Context(), msg.ChatID, "An error occurred. Please try again later.")
		w.WriteHeader(http.StatusOK)
		return
	}

	switch status {
	case http.StatusOK:
		h.logger.Info("verification confirmed",
			"provider", providerName,
			"provider_user_id", msg.ProviderUserID,
		)
		_ = bp.SendReply(r.Context(), msg.ChatID, "Code accepted! Return to your browser.")
	case http.StatusNotFound:
		_ = bp.SendReply(r.Context(), msg.ChatID, "Invalid or expired code. Please try again.")
	default:
		h.logger.Warn("unexpected confirm status", "status", status, "provider", providerName)
		_ = bp.SendReply(r.Context(), msg.ChatID, "Something went wrong. Please try again.")
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) confirm(r *http.Request, code, providerName, providerUserID, displayName string) (int, error) {
	body, _ := json.Marshal(map[string]string{
		"code":             code,
		"provider":         providerName,
		"provider_user_id": providerUserID,
		"display_name":     displayName,
	})

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.confirmURL, bytes.NewReader(body))
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
