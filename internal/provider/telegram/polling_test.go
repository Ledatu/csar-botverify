package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPollingPreservesOffset(t *testing.T) {
	methods := []string{}
	p := newTestProvider(t, func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		methods = append(methods, method)
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response := `{"ok":true,"result":true}`
		if method == "getUpdates" {
			if body["offset"] != float64(23) || body["timeout"] != float64(20) {
				t.Fatal("wrong long polling offset or timeout")
			}
			response = `{"ok":true,"result":[{"update_id":23,"message":{"from":{"id":12},"chat":{"id":12},"text":"/start abc123"}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	updates, err := p.GetUpdates(context.Background(), 23)
	if err != nil || len(updates) != 1 || updates[0].Incoming().Text != "ABC123" {
		t.Fatalf("unexpected updates: %v", err)
	}
	if len(methods) != 1 {
		t.Fatal("unexpected Telegram requests")
	}
}

func TestTelegramAPIRejectsFalseOKAndPreservesRetryTiming(t *testing.T) {
	p := newTestProvider(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
			`{"ok":false,"error_code":429,"description":"` + testToken + `","parameters":{"retry_after":17}}`)), Header: make(http.Header)}, nil
	})
	err := p.SendReply(context.Background(), "99", "private message")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 17*time.Second || strings.Contains(err.Error(), testToken) {
		t.Fatal("API failure lost retry timing or leaked credential")
	}
}

func TestPollingRefusesRegisteredWebhook(t *testing.T) {
	p := newTestProvider(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
			`{"ok":true,"result":{"url":"https://example.com/webhook"}}`)), Header: make(http.Header)}, nil
	})
	if err := p.CheckPolling(context.Background()); err == nil {
		t.Fatal("polling accepted an active webhook")
	}
}
