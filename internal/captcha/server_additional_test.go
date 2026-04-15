package captcha

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

func TestErrorsInitializesServeErrorChannelWhenNeeded(t *testing.T) {
	t.Parallel()

	server := &Server{}
	if ch := server.Errors(); ch == nil || server.serveErrors == nil {
		t.Fatalf("Errors() = %v, want initialized channel", ch)
	}
}

func TestParseVerificationRequestAcceptsMultipartForm(t *testing.T) {
	t.Parallel()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("uid", "42"); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	if err := parseVerificationRequest(req); err != nil {
		t.Fatalf("parseVerificationRequest() error = %v", err)
	}
	if got := req.FormValue("uid"); got != "42" {
		t.Fatalf("FormValue(uid) = %q, want %q", got, "42")
	}
}

func TestRuntimeStatsHandlesNilReceiverAndStartsAtZero(t *testing.T) {
	t.Parallel()

	var nilServer *Server
	if got := nilServer.RuntimeStats(); got != (RuntimeStats{}) {
		t.Fatalf("RuntimeStats(nil) = %+v, want zero value", got)
	}

	server := newTestServer("token")
	if got := server.RuntimeStats(); got != (RuntimeStats{}) {
		t.Fatalf("RuntimeStats(new server) = %+v, want zero value", got)
	}
}

func TestRuntimeStatsReflectsRecordedCounters(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.recordSuccess()
	server.recordFailure()
	server.recordTimeout()

	if got := server.RuntimeStats(); got != (RuntimeStats{Successes: 1, Failures: 1, Timeouts: 1}) {
		t.Fatalf("RuntimeStats() = %+v, want all counters incremented once", got)
	}
}

func TestErrorsReturnsExistingChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan error, 1)
	server := &Server{serveErrors: ch}

	if got := server.Errors(); got != ch {
		t.Fatalf("Errors() = %v, want existing channel %v", got, ch)
	}
}

func TestStartInitializesMissingHooksAndRejectsDoubleStart(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.listen = nil
	server.serve = nil
	server.serveErrors = nil

	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
	})

	if server.listen == nil || server.serve == nil || server.serveErrors == nil {
		t.Fatalf("Start() did not initialize hooks: listenNil=%t serveNil=%t errorsNil=%t", server.listen == nil, server.serve == nil, server.serveErrors == nil)
	}

	if err := server.Start(); err == nil {
		t.Fatal("Start() error = nil, want already-started error")
	}
}

func TestStartReturnsListenErrors(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.listen = func(network, address string) (net.Listener, error) {
		return nil, errors.New("bind failed")
	}

	if err := server.Start(); err == nil || !strings.Contains(err.Error(), "bind failed") {
		t.Fatalf("Start() error = %v, want listen failure", err)
	}
}

func TestHandleVerifyPageReturnsInternalErrorWhenTemplateExecutionFails(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	pending := &store.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   now.Unix(),
		RandomToken: "token-a",
		ExpireAt:    now.Add(5 * time.Minute),
	}
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(chatID, userID int64) (*store.PendingVerification, error) {
			if chatID == pending.ChatID && userID == pending.UserID {
				return pending, nil
			}
			return nil, nil
		},
	}, nil)
	server.tmpl = template.Must(template.New("broken").Parse("{{.Missing.Field}}"))

	form := newSignedForm(server, pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken)
	req := httptest.NewRequest(http.MethodGet, config.VerifyPath+"?"+form.Encode(), nil)
	recorder := httptest.NewRecorder()

	server.handleVerifyPage(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("handleVerifyPage() status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Internal error") {
		t.Fatalf("handleVerifyPage() body = %q, want internal error response", body)
	}
}

func TestValidateVerificationRequestPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(chatID, userID int64) (*store.PendingVerification, error) {
			return nil, errors.New("lookup failed")
		},
	}, nil)

	uid := "42"
	cid := "-100123"
	timestamp := strconv.FormatInt(now.Unix(), 10)
	randomToken := "token-a"
	sig := server.sign(uid, cid, timestamp, randomToken)

	_, _, err := server.validateVerificationRequest(uid, cid, timestamp, randomToken, sig)
	if err == nil || !strings.Contains(err.Error(), "lookup failed") {
		t.Fatalf("validateVerificationRequest() error = %v, want store failure", err)
	}
}
