// Package telegram implements the BotProvider interface for Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ledatu/csar-botverify/internal/provider"
)

const apiBase = "https://api.telegram.org/bot"

// Provider implements provider.BotProvider for Telegram.
type Provider struct {
	token         string
	webhookSecret string
	client        *http.Client
	logger        *slog.Logger
}

// New creates a Telegram bot provider.
func New(token, webhookSecret string, logger *slog.Logger) *Provider {
	return &Provider{
		token:         token,
		webhookSecret: webhookSecret,
		client:        &http.Client{},
		logger:        logger.With("bot_provider", "telegram"),
	}
}

func (p *Provider) Name() string { return "telegram" }

// ValidateWebhook checks the X-Telegram-Bot-Api-Secret-Token header.
func (p *Provider) ValidateWebhook(r *http.Request) bool {
	if p.webhookSecret == "" {
		return true
	}
	return r.Header.Get("X-Telegram-Bot-Api-Secret-Token") == p.webhookSecret
}

// ParseWebhook extracts the message from a Telegram Update payload.
func (p *Provider) ParseWebhook(r *http.Request) (*provider.IncomingMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	defer func() { _ = r.Body.Close() }()

	var update struct {
		Message *struct {
			From *struct {
				ID        int64  `json:"id"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
			} `json:"from"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		return nil, fmt.Errorf("parsing update: %w", err)
	}
	if update.Message == nil || update.Message.From == nil {
		return nil, fmt.Errorf("no message or sender in update")
	}

	displayName := update.Message.From.FirstName
	if update.Message.From.LastName != "" {
		displayName += " " + update.Message.From.LastName
	}

	return &provider.IncomingMessage{
		ProviderUserID: strconv.FormatInt(update.Message.From.ID, 10),
		DisplayName:    displayName,
		ChatID:         strconv.FormatInt(update.Message.Chat.ID, 10),
		Text:           update.Message.Text,
	}, nil
}

// RegisterWebhook calls the Telegram setWebhook API.
func (p *Provider) RegisterWebhook(ctx context.Context, webhookURL string) error {
	payload := map[string]string{
		"url":          webhookURL,
		"secret_token": p.webhookSecret,
	}
	body, _ := json.Marshal(payload)

	url := apiBase + p.token + "/setWebhook"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling setWebhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("setWebhook returned %d: %s", resp.StatusCode, respBody)
	}

	p.logger.Info("telegram webhook registered", "url", webhookURL)
	return nil
}

// SendReply sends a text message to the specified chat.
func (p *Provider) SendReply(ctx context.Context, chatID, text string) error {
	payload := map[string]string{
		"chat_id": chatID,
		"text":    text,
	}
	body, _ := json.Marshal(payload)

	url := apiBase + p.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling sendMessage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		p.logger.Warn("sendMessage failed", "status", resp.StatusCode, "body", string(respBody))
	}
	return nil
}
