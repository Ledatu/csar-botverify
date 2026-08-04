package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// testToken mimics the real bot token shape: <bot_id>:<secret>.
const testToken = "7712345678:AAHrealLookingSecretValue_abcdefghijklmno"

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestProvider(t *testing.T, rt roundTripperFunc) *Provider {
	t.Helper()
	p := New(testToken, "webhook-secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.client = &http.Client{Transport: rt}
	return p
}

// errTimeout stands in for the production failure: net/http wraps this in a
// *url.Error carrying the full request URL, token and all.
var errTimeout = errors.New("dial tcp 149.154.166.110:443: i/o timeout")

func failingTransport() roundTripperFunc {
	return func(*http.Request) (*http.Response, error) { return nil, errTimeout }
}

func TestRegisterWebhookRedactsTokenOnTransportError(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, failingTransport())

	err := p.RegisterWebhook(context.Background(), "https://example.com/webhook/telegram")
	if err == nil {
		t.Fatal("RegisterWebhook() error = nil, want a transport error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("bot token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), redactedToken) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), redactedToken)
	}
}

func TestSendReplyRedactsTokenOnTransportError(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, failingTransport())

	err := p.SendReply(context.Background(), "99", "hello")
	if err == nil {
		t.Fatal("SendReply() error = nil, want a transport error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("bot token leaked into error: %v", err)
	}
}

// The redacted error must stay inspectable, otherwise redaction would silently
// break callers that branch on the underlying error.
func TestRedactErrPreservesErrorChain(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, failingTransport())

	err := p.RegisterWebhook(context.Background(), "https://example.com/webhook/telegram")
	if !errors.Is(err, errTimeout) {
		t.Fatalf("errors.Is(err, errTimeout) = false, want true (chain broken); err = %v", err)
	}
}

// An empty token must short-circuit: strings.ReplaceAll with an empty old value
// splices the placeholder between every character.
func TestRedactStringEmptyTokenLeavesInputIntact(t *testing.T) {
	t.Parallel()

	p := New("", "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	const in = "dial tcp: i/o timeout"
	if got := p.redactString(in); got != in {
		t.Fatalf("redactString(%q) = %q, want it unchanged", in, got)
	}
}

func TestRedactErrNilStaysNil(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, failingTransport())
	if got := p.redactErr(nil); got != nil {
		t.Fatalf("redactErr(nil) = %v, want nil", got)
	}
}

// The token must not survive in a non-2xx response body either, in case the API
// ever echoes the request back.
func TestRegisterWebhookRedactsTokenInResponseBody(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"description":"bot` + testToken + ` is invalid"}`)),
			Header:     make(http.Header),
		}, nil
	})

	err := p.RegisterWebhook(context.Background(), "https://example.com/webhook/telegram")
	if err == nil {
		t.Fatal("RegisterWebhook() error = nil, want a status error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("bot token leaked from response body: %v", err)
	}
}
