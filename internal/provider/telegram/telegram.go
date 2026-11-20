// Package telegram implements the BotProvider interface for Telegram Bot API.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ledatu/csar-botverify/internal/provider"
)

const apiBase = "https://api.telegram.org/bot"

// Provider implements provider.BotProvider for Telegram.
type Provider struct {
	token         string
	webhookSecret string
	client        *http.Client
}

// New creates a Telegram bot provider.
func New(token, webhookSecret string, _ *slog.Logger) *Provider {
	return &Provider{
		token:         token,
		webhookSecret: webhookSecret,
		client:        &http.Client{Timeout: 35 * time.Second},
	}
}

func (p *Provider) Name() string { return "telegram" }

// redactedToken replaces the bot token wherever it would otherwise be exposed.
const redactedToken = "[REDACTED]"

// apiURL builds the endpoint for a Bot API method. The token sits in the URL
// path, so anything derived from this string — including errors returned by
// net/http — must go through redactErr before it reaches a log sink.
func (p *Provider) apiURL(method string) string {
	return apiBase + p.token + "/" + method
}

// redactedError hides the bot token in an error's message while keeping the
// original error reachable for errors.Is/errors.As.
type redactedError struct {
	msg   string
	inner error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.inner }

// redactString strips the bot token out of s.
func (p *Provider) redactString(s string) string {
	// ReplaceAll with an empty token would splice the placeholder between
	// every character, so an unset token must short-circuit.
	if p.token == "" {
		return s
	}
	return strings.ReplaceAll(s, p.token, redactedToken)
}

// redactErr strips the bot token from err's message. net/http embeds the full
// request URL in *url.Error, so an unredacted transport failure — a DNS error
// or an i/o timeout on setWebhook — writes the token straight into the logs.
func (p *Provider) redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := p.redactString(msg)
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, inner: err}
}

// parseMessage classifies incoming message text. It returns the verification
// code the message carries, and whether the message was a bare /start with no
// payload — the case where a user opens the bot from Telegram search instead of
// a t.me/<bot>?start=<code> deep link, leaving nothing to confirm.
func parseMessage(text string) (code string, bareStart bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}

	fields := strings.Fields(trimmed)
	if isStartCommand(fields[0]) {
		if len(fields) < 2 {
			return "", true
		}
		return strings.ToUpper(strings.TrimSpace(fields[1])), false
	}

	return strings.ToUpper(trimmed), false
}

// isStartCommand matches /start, including the /start@botname form Telegram
// sends in group chats. Without stripping the suffix the whole command would be
// mistaken for a verification code.
func isStartCommand(field string) bool {
	if at := strings.IndexByte(field, '@'); at != -1 {
		field = field[:at]
	}
	return strings.EqualFold(field, "/start")
}

// ValidateWebhook checks the X-Telegram-Bot-Api-Secret-Token header.
func (p *Provider) ValidateWebhook(r *http.Request) bool {
	if p.webhookSecret == "" {
		return true
	}
	return r.Header.Get("X-Telegram-Bot-Api-Secret-Token") == p.webhookSecret
}

type Update struct {
	ID      int64 `json:"update_id"`
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

// ParseWebhook extracts the message from a Telegram Update payload.
func (p *Provider) ParseWebhook(r *http.Request) (*provider.IncomingMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	defer func() { _ = r.Body.Close() }()

	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		return nil, fmt.Errorf("parsing update: %w", err)
	}
	if update.Message == nil || update.Message.From == nil {
		return nil, fmt.Errorf("no message or sender in update")
	}
	return update.Incoming(), nil
}

// Incoming ignores non-message updates. It never treats them as confirmations.
func (update Update) Incoming() *provider.IncomingMessage {
	if update.Message == nil || update.Message.From == nil {
		return nil
	}

	displayName := update.Message.From.FirstName
	if update.Message.From.LastName != "" {
		displayName += " " + update.Message.From.LastName
	}

	code, bareStart := parseMessage(update.Message.Text)

	return &provider.IncomingMessage{
		ProviderUserID: strconv.FormatInt(update.Message.From.ID, 10),
		DisplayName:    displayName,
		ChatID:         strconv.FormatInt(update.Message.Chat.ID, 10),
		Text:           code,
		IsStart:        bareStart,
	}
}

// RegisterWebhook calls the Telegram setWebhook API.
func (p *Provider) RegisterWebhook(ctx context.Context, webhookURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.call(ctx, "setWebhook", map[string]string{
		"url": webhookURL, "secret_token": p.webhookSecret,
	}, nil)
}

// SendReply returns an error for both transport and Telegram API failures.
func (p *Provider) SendReply(ctx context.Context, chatID, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.call(ctx, "sendMessage", map[string]string{
		"chat_id": chatID, "text": text,
	}, nil)
}
