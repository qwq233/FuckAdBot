package captcha

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

func TestStartAppliesHTTPHardeningConfig(t *testing.T) {
	t.Parallel()

	server := NewServer(&config.TurnstileConfig{
		Domain:     "example.com",
		ListenAddr: "127.0.0.1",
		ListenPort: 0,
	}, &stubStore{}, 5*time.Minute, "token", nil)

	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer server.Shutdown(context.Background())

	if got, want := server.httpServer.ReadHeaderTimeout, serverReadHeaderTimeout; got != want {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := server.httpServer.ReadTimeout, serverReadTimeout; got != want {
		t.Fatalf("ReadTimeout = %v, want %v", got, want)
	}
	if got, want := server.httpServer.WriteTimeout, serverWriteTimeout; got != want {
		t.Fatalf("WriteTimeout = %v, want %v", got, want)
	}
	if got, want := server.httpServer.IdleTimeout, serverIdleTimeout; got != want {
		t.Fatalf("IdleTimeout = %v, want %v", got, want)
	}
	if got, want := server.httpServer.MaxHeaderBytes, serverMaxHeaderBytes; got != want {
		t.Fatalf("MaxHeaderBytes = %d, want %d", got, want)
	}
}

func TestHandleCallbackRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	server := NewServer(&config.TurnstileConfig{
		Domain:     "example.com",
		ListenAddr: "127.0.0.1",
		ListenPort: 8080,
	}, &stubStore{}, 5*time.Minute, "token", nil)

	formBody := "uid=42&cid=-100123&timestamp=1&rand=token&sig=signature&cf-turnstile-response=" + strings.Repeat("x", int(serverMaxFormBytes))
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(formBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("handleCallback() status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if strings.Contains(recorder.Body.String(), "验证成功") {
		t.Fatalf("handleCallback() body = %q, want oversized body rejection", recorder.Body.String())
	}
}

func TestHandleCallbackUpdatesRuntimeStats(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	pending := &store.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   now.Unix(),
		RandomToken: "token-a",
		ExpireAt:    now.Add(5 * time.Minute),
	}

	baseStore := &stubStore{
		pendingFn: func(chatID, userID int64) (*store.PendingVerification, error) {
			if chatID == pending.ChatID && userID == pending.UserID {
				return pending, nil
			}
			return nil, nil
		},
	}

	makeServer := func() *Server {
		server := newHTTPTestServer(baseStore, nil)
		server.httpClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"success":true,"hostname":"example.com"}`)),
					Header:     make(http.Header),
				}, nil
			}),
		}
		return server
	}

	successServer := makeServer()
	form := newSignedForm(successServer, pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken)
	form.Set("cf-turnstile-response", "cf-token")
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	successServer.handleCallback(recorder, req)
	if stats := successServer.RuntimeStats(); stats.Successes != 1 || stats.Failures != 0 || stats.Timeouts != 0 {
		t.Fatalf("success stats = %+v, want success increment", stats)
	}

	timeoutServer := makeServer()
	timeoutServer.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	req = httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	timeoutServer.handleCallback(recorder, req)
	if stats := timeoutServer.RuntimeStats(); stats.Timeouts != 1 || stats.Failures != 0 {
		t.Fatalf("timeout stats = %+v, want timeout increment", stats)
	}

	failureServer := makeServer()
	failureServer.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"success":false,"hostname":"example.com"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	req = httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	failureServer.handleCallback(recorder, req)
	if stats := failureServer.RuntimeStats(); stats.Failures != 1 || stats.Successes != 0 {
		t.Fatalf("failure stats = %+v, want failure increment", stats)
	}
}
