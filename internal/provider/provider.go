// Package provider defines the BotProvider interface for messenger bot integrations.
package provider

import (
	"context"
	"net/http"
)

// IncomingMessage represents a parsed message from a bot webhook.
type IncomingMessage struct {
	ProviderUserID string
	DisplayName    string
	ChatID         string // for sending reply
	Text           string
}

// BotProvider abstracts messenger bot operations for a single platform.
type BotProvider interface {
	Name() string
	ParseWebhook(r *http.Request) (*IncomingMessage, error)
	RegisterWebhook(ctx context.Context, webhookURL string) error
	ValidateWebhook(r *http.Request) bool
	SendReply(ctx context.Context, chatID, text string) error
}
