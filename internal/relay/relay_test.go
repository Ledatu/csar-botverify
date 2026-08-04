package relay

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ledatu/csar-botverify/internal/provider"
)

// fakeProvider records replies instead of calling the Telegram API.
type fakeProvider struct {
	msg     *provider.IncomingMessage
	replies []string
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) ParseWebhook(*http.Request) (*provider.IncomingMessage, error) {
	return f.msg, nil
}

func (f *fakeProvider) RegisterWebhook(context.Context, string) error { return nil }

func (f *fakeProvider) ValidateWebhook(*http.Request) bool { return true }

func (f *fakeProvider) SendReply(_ context.Context, _, text string) error {
	f.replies = append(f.replies, text)
	return nil
}

// newTestHandler wires a Handler to a confirm endpoint that records how many
// times it was called and answers with confirmStatus.
func newTestHandler(t *testing.T, fp *fakeProvider, confirmStatus int) (*Handler, *int) {
	t.Helper()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(confirmStatus)
	}))
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := New(map[string]provider.BotProvider{"fake": fp}, srv.URL, srv.Client(), logger)
	return h, &calls
}

func serve(h *Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook/fake", strings.NewReader("{}"))
	req.SetPathValue("provider", "fake")
	rec := httptest.NewRecorder()
	h.HandleWebhook(rec, req)
	return rec
}

func TestHandleWebhookAnswersBareStart(t *testing.T) {
	t.Parallel()

	fp := &fakeProvider{msg: &provider.IncomingMessage{ChatID: "99", Text: "", IsStart: true}}
	h, confirmCalls := newTestHandler(t, fp, http.StatusOK)

	rec := serve(h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(fp.replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(fp.replies), fp.replies)
	}
	if fp.replies[0] != startMessage {
		t.Fatalf("reply = %q, want startMessage", fp.replies[0])
	}
	if *confirmCalls != 0 {
		t.Fatalf("confirm called %d times, want 0 — a bare /start has no code", *confirmCalls)
	}
}

func TestHandleWebhookStaysSilentOnEmptyNonStart(t *testing.T) {
	t.Parallel()

	// e.g. a sticker or photo: no text, and not a /start.
	fp := &fakeProvider{msg: &provider.IncomingMessage{ChatID: "99", Text: "", IsStart: false}}
	h, confirmCalls := newTestHandler(t, fp, http.StatusOK)

	rec := serve(h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(fp.replies) != 0 {
		t.Fatalf("got %d replies, want 0: %v", len(fp.replies), fp.replies)
	}
	if *confirmCalls != 0 {
		t.Fatalf("confirm called %d times, want 0", *confirmCalls)
	}
}

func TestHandleWebhookConfirmsCode(t *testing.T) {
	t.Parallel()

	fp := &fakeProvider{msg: &provider.IncomingMessage{ChatID: "99", Text: "ABC123"}}
	h, confirmCalls := newTestHandler(t, fp, http.StatusOK)

	serve(h)

	if *confirmCalls != 1 {
		t.Fatalf("confirm called %d times, want 1", *confirmCalls)
	}
	if len(fp.replies) != 1 {
		t.Fatalf("got %d replies, want 1: %v", len(fp.replies), fp.replies)
	}
	if fp.replies[0] == startMessage {
		t.Fatal("code path replied with startMessage, want the acceptance message")
	}
}
