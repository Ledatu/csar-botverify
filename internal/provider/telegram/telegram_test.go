package telegram

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestExtractVerificationCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "plain code", text: "abc123", want: "ABC123"},
		{name: "start payload", text: "/start abc123", want: "ABC123"},
		{name: "start payload with spaces", text: " /start   abc123  ", want: "ABC123"},
		{name: "empty", text: "   ", want: ""},
		{name: "broken start without payload", text: "/start", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractVerificationCode(tt.text); got != tt.want {
				t.Fatalf("extractVerificationCode(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestParseWebhookExtractsStartPayload(t *testing.T) {
	t.Parallel()

	p := New("token", "secret", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	req := httptest.NewRequest("POST", "/webhook/telegram", strings.NewReader(`{
		"message": {
			"from": {"id": 42, "first_name": "Alice", "last_name": "Bot"},
			"chat": {"id": 99},
			"text": "/start a7x9k2"
		}
	}`))

	msg, err := p.ParseWebhook(req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}

	if msg.Text != "A7X9K2" {
		t.Fatalf("msg.Text = %q, want %q", msg.Text, "A7X9K2")
	}
	if msg.ProviderUserID != "42" {
		t.Fatalf("msg.ProviderUserID = %q, want 42", msg.ProviderUserID)
	}
	if msg.ChatID != "99" {
		t.Fatalf("msg.ChatID = %q, want 99", msg.ChatID)
	}
}
