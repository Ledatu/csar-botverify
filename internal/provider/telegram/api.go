package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// APIError contains only status and retry timing. Telegram descriptions and
// response bodies may include credentials or user data and are never retained.
type APIError struct {
	Code       int
	RetryAfter time.Duration
}

// Permanent marks replies rejected because the destination cannot receive them.
func (e *APIError) Permanent() bool {
	return e.Code == http.StatusBadRequest || e.Code == http.StatusForbidden
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram API returned status %d", e.Code)
}

// ConfigureNetwork limits Telegram connections to the selected address family.
// The default preserves Go's dual-stack dialing for existing webhook callers.
func (p *Provider) ConfigureNetwork(network string) error {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return fmt.Errorf("telegram: invalid network %q", network)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Preserve existing proxy behavior for the default dual-stack mode.
	// An explicit address family selects a direct Telegram connection.
	if network != "tcp" {
		transport.Proxy = nil
	}
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	transport.TLSHandshakeTimeout = 5 * time.Second
	p.client = &http.Client{Transport: transport, Timeout: 35 * time.Second}
	return nil
}

func (p *Provider) call(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding Telegram request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL(method), bytes.NewReader(body))
	if err != nil {
		return p.redactErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return p.redactErr(err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponse = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return p.redactErr(err)
	}
	if len(data) > maxResponse {
		return fmt.Errorf("telegram response exceeds size limit")
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		ErrorCode  int             `json:"error_code"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid Telegram response envelope")
	}
	if resp.StatusCode != http.StatusOK || !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Code: code, RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("invalid Telegram result")
		}
	}
	return nil
}

// GetUpdates performs one bounded long poll. Telegram confirms earlier updates
// only when the next request advances offset beyond their update IDs.
func (p *Provider) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var updates []Update
	err := p.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 20, "limit": 20, "allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

// CheckPolling verifies that another replica has not re-registered the webhook.
func (p *Provider) CheckPolling(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info struct {
		URL string `json:"url"`
	}
	if err := p.call(ctx, "getWebhookInfo", nil, &info); err != nil {
		return err
	}
	if info.URL != "" {
		return fmt.Errorf("telegram webhook is still registered")
	}
	return nil
}
