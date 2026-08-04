package telegram

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParseMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		text          string
		want          string
		wantBareStart bool
	}{
		{name: "plain code", text: "abc123", want: "ABC123"},
		{name: "start payload", text: "/start abc123", want: "ABC123"},
		{name: "start payload with spaces", text: " /start   abc123  ", want: "ABC123"},
		{name: "empty", text: "   ", want: ""},
		{name: "bare start", text: "/start", want: "", wantBareStart: true},
		{name: "bare start mixed case", text: "/START", want: "", wantBareStart: true},
		{name: "bare start in group", text: "/start@aurumskynetauthnbot", want: "", wantBareStart: true},
		{name: "start payload in group", text: "/start@aurumskynetauthnbot abc123", want: "ABC123"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, gotBareStart := parseMessage(tt.text)
			if got != tt.want {
				t.Fatalf("parseMessage(%q) code = %q, want %q", tt.text, got, tt.want)
			}
			if gotBareStart != tt.wantBareStart {
				t.Fatalf("parseMessage(%q) bareStart = %v, want %v", tt.text, gotBareStart, tt.wantBareStart)
			}
		})
	}
}

func TestParseWebhookFlagsBareStart(t *testing.T) {
	t.Parallel()

	p := New("token", "secret", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	req := httptest.NewRequest("POST", "/webhook/telegram", strings.NewReader(`{
		"message": {
			"from": {"id": 42, "first_name": "Alice"},
			"chat": {"id": 99},
			"text": "/start"
		}
	}`))

	msg, err := p.ParseWebhook(req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}

	if msg.Text != "" {
		t.Fatalf("msg.Text = %q, want empty", msg.Text)
	}
	if !msg.IsStart {
		t.Fatal("msg.IsStart = false, want true for a bare /start")
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
