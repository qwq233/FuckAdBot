package captcha

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

func newHTTPTestServer(st store.Store, onVerify func(token VerifiedToken)) *Server {
	if st == nil {
		st = &stubStore{}
	}

	return NewServer(&config.TurnstileConfig{
		Domain:        "example.com",
		SiteKey:       "site-key",
		SecretKey:     "secret-key",
		ListenAddr:    "127.0.0.1",
		ListenPort:    8080,
		VerifyTimeout: "5m",
	}, st, 5*time.Minute, "test-bot-token", onVerify)
}

func newSignedForm(s *Server, chatID, userID int64, timestamp int64, randomToken string) url.Values {
	uid := strconv.FormatInt(userID, 10)
	cid := strconv.FormatInt(chatID, 10)
	ts := strconv.FormatInt(timestamp, 10)

	return url.Values{
		"uid":       {uid},
		"cid":       {cid},
		"timestamp": {ts},
		"rand":      {randomToken},
		"sig":       {s.sign(uid, cid, ts, randomToken)},
	}
}

func TestParseVerificationRequestAcceptsURLForm(t *testing.T) {
	t.Parallel()

	form := url.Values{"uid": {"42"}}
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if err := parseVerificationRequest(req); err != nil {
		t.Fatalf("parseVerificationRequest() error = %v", err)
	}
	if got := req.FormValue("uid"); got != "42" {
		t.Fatalf("FormValue(uid) = %q, want %q", got, "42")
	}
}

func TestParseVerificationRequestRejectsMalformedMultipart(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader("--x\r\nbroken"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")

	if err := parseVerificationRequest(req); err == nil {
		t.Fatal("parseVerificationRequest() error = nil, want multipart parse error")
	}
}

func TestExtractClientIP(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.2:443")
	req.RemoteAddr = "192.0.2.1:1234"
	if got := extractClientIP(req); got != "198.51.100.2" {
		t.Fatalf("extractClientIP(X-Forwarded-For) = %q, want %q", got, "198.51.100.2")
	}

	req = httptest.NewRequest(http.MethodPost, config.CallbackPath, nil)
	req.Header.Set("X-Real-IP", "198.51.100.3:8443")
	req.RemoteAddr = "192.0.2.1:1234"
	if got := extractClientIP(req); got != "198.51.100.3" {
		t.Fatalf("extractClientIP(X-Real-IP) = %q, want %q", got, "198.51.100.3")
	}

	req = httptest.NewRequest(http.MethodPost, config.CallbackPath, nil)
	req.RemoteAddr = "198.51.100.4:9999"
	if got := extractClientIP(req); got != "198.51.100.4" {
		t.Fatalf("extractClientIP(RemoteAddr) = %q, want %q", got, "198.51.100.4")
	}
}

func TestMustParseTimestamp(t *testing.T) {
	t.Parallel()

	if got := mustParseTimestamp("123"); got != 123 {
		t.Fatalf("mustParseTimestamp() = %d, want %d", got, 123)
	}
	if got := mustParseTimestamp("bad"); got != 0 {
		t.Fatalf("mustParseTimestamp() = %d, want 0 for invalid input", got)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	writeJSON(recorder, false, "boom")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("writeJSON() status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"success":false`) || !strings.Contains(body, `"message":"boom"`) {
		t.Fatalf("writeJSON() body = %q, want serialized error payload", body)
	}
}

func TestVerificationRequestErrorHelpers(t *testing.T) {
	t.Parallel()

	err := &verificationRequestError{status: http.StatusForbidden, message: "bad"}
	if got := err.Error(); got != "bad" {
		t.Fatalf("verificationRequestError.Error() = %q, want %q", got, "bad")
	}
	if got := verificationErrorStatus(err); got != http.StatusForbidden {
		t.Fatalf("verificationErrorStatus() = %d, want %d", got, http.StatusForbidden)
	}
	if got := verificationErrorStatus(context.DeadlineExceeded); got != http.StatusInternalServerError {
		t.Fatalf("verificationErrorStatus(generic) = %d, want %d", got, http.StatusInternalServerError)
	}
}

func TestGenerateVerifyURLContainsSignedParameters(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	verifyURL := server.GenerateVerifyURL(-100123, 42, 1712300000, "token-a")

	parsed, err := url.Parse(verifyURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Path != config.VerifyPath {
		t.Fatalf("GenerateVerifyURL() path = %q, want %q", parsed.Path, config.VerifyPath)
	}
	query := parsed.Query()
	if query.Get("uid") != "42" || query.Get("cid") != "-100123" || query.Get("timestamp") != "1712300000" || query.Get("rand") != "token-a" {
		t.Fatalf("GenerateVerifyURL() query = %v, want encoded verification parameters", query)
	}
	if !server.verifySignature(query.Get("uid"), query.Get("cid"), query.Get("timestamp"), query.Get("rand"), query.Get("sig")) {
		t.Fatal("GenerateVerifyURL() produced signature that does not verify")
	}
}

func TestHandleVerifyPageRejectsWrongMethod(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	req := httptest.NewRequest(http.MethodPost, config.VerifyPath, nil)
	recorder := httptest.NewRecorder()

	server.handleVerifyPage(recorder, req)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("handleVerifyPage() status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleVerifyPageValidationError(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	req := httptest.NewRequest(http.MethodGet, config.VerifyPath+"?uid=1", nil)
	recorder := httptest.NewRecorder()

	server.handleVerifyPage(recorder, req)

	if recorder.Code == http.StatusOK {
		t.Fatalf("handleVerifyPage() status = %d, want non-200 for invalid request", recorder.Code)
	}
}

func TestHandleVerifyPageRendersTemplate(t *testing.T) {
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
	form := newSignedForm(server, pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken)
	req := httptest.NewRequest(http.MethodGet, config.VerifyPath+"?"+form.Encode(), nil)
	recorder := httptest.NewRecorder()

	server.handleVerifyPage(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("handleVerifyPage() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "site-key") || !strings.Contains(body, server.cfg.CallbackURL()) {
		t.Fatalf("handleVerifyPage() body = %q, want rendered template with site key and callback URL", body)
	}
}

func TestHandleCallbackRejectsWrongMethod(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	req := httptest.NewRequest(http.MethodGet, config.CallbackPath, nil)
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("handleCallback() status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleCallbackRejectsMalformedForm(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader("--x\r\nbroken"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "Invalid form data") {
		t.Fatalf("handleCallback() = (%d, %q), want invalid form error", recorder.Code, recorder.Body.String())
	}
}

func TestHandleCallbackRejectsMissingTurnstileToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(_, _ int64) (*store.PendingVerification, error) {
			return &store.PendingVerification{
				ChatID:      -100123,
				UserID:      42,
				Timestamp:   now.Unix(),
				RandomToken: "token-a",
				ExpireAt:    now.Add(5 * time.Minute),
			}, nil
		},
	}, nil)
	form := newSignedForm(server, -100123, 42, now.Unix(), "token-a")
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "Missing parameters") {
		t.Fatalf("handleCallback() = (%d, %q), want missing parameter error", recorder.Code, recorder.Body.String())
	}
}

func TestHandleCallbackRejectsFailedTurnstile(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(_, _ int64) (*store.PendingVerification, error) {
			return &store.PendingVerification{
				ChatID:      -100123,
				UserID:      42,
				Timestamp:   now.Unix(),
				RandomToken: "token-a",
				ExpireAt:    now.Add(5 * time.Minute),
			}, nil
		},
	}, nil)
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"success":false,"hostname":"example.com","error-codes":["bad-input-response"]}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	form := newSignedForm(server, -100123, 42, now.Unix(), "token-a")
	form.Set("cf-turnstile-response", "cf-token")
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "人机验证未通过") {
		t.Fatalf("handleCallback() = (%d, %q), want failed verification response", recorder.Code, recorder.Body.String())
	}
}

func TestHandleCallbackHandlesTurnstileError(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(_, _ int64) (*store.PendingVerification, error) {
			return &store.PendingVerification{
				ChatID:      -100123,
				UserID:      42,
				Timestamp:   now.Unix(),
				RandomToken: "token-a",
				ExpireAt:    now.Add(5 * time.Minute),
			}, nil
		},
	}, nil)
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}

	form := newSignedForm(server, -100123, 42, now.Unix(), "token-a")
	form.Set("cf-turnstile-response", "cf-token")
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "验证服务异常") {
		t.Fatalf("handleCallback() = (%d, %q), want turnstile error response", recorder.Code, recorder.Body.String())
	}
}

func TestHandleCallbackSuccess(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	pending := &store.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   now.Unix(),
		RandomToken: "token-a",
		ExpireAt:    now.Add(5 * time.Minute),
	}
	var verifiedToken VerifiedToken
	server := newHTTPTestServer(&stubStore{
		pendingFn: func(chatID, userID int64) (*store.PendingVerification, error) {
			if chatID == pending.ChatID && userID == pending.UserID {
				return pending, nil
			}
			return nil, nil
		},
	}, func(token VerifiedToken) {
		verifiedToken = token
	})
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"hostname":"example.com"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	form := newSignedForm(server, pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken)
	form.Set("cf-turnstile-response", "cf-token")
	req := httptest.NewRequest(http.MethodPost, config.CallbackPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.2")
	recorder := httptest.NewRecorder()

	server.handleCallback(recorder, req)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "验证成功") {
		t.Fatalf("handleCallback() = (%d, %q), want success response", recorder.Code, recorder.Body.String())
	}
	if verifiedToken.ChatID != pending.ChatID || verifiedToken.UserID != pending.UserID || verifiedToken.Timestamp != pending.Timestamp || verifiedToken.RandomToken != pending.RandomToken {
		t.Fatalf("onVerify token = %+v, want %+v", verifiedToken, VerifiedToken{
			ChatID:      pending.ChatID,
			UserID:      pending.UserID,
			Timestamp:   pending.Timestamp,
			RandomToken: pending.RandomToken,
		})
	}
}

func TestLogTurnstileVerifyError(t *testing.T) {
	buffer := bytes.NewBuffer(nil)
	origWriter := log.Writer()
	log.SetOutput(buffer)
	defer log.SetOutput(origWriter)

	logTurnstileVerifyError(context.DeadlineExceeded)
	if !strings.Contains(buffer.String(), "timeout") {
		t.Fatalf("timeout log = %q, want timeout message", buffer.String())
	}

	buffer.Reset()
	logTurnstileVerifyError(&verificationRequestError{message: "status 400"})
	if !strings.Contains(strings.ToLower(buffer.String()), "upstream rejected request") {
		t.Fatalf("4xx log = %q, want upstream rejected request log", buffer.String())
	}

	buffer.Reset()
	logTurnstileVerifyError(&verificationRequestError{message: "status 500"})
	if !strings.Contains(strings.ToLower(buffer.String()), "upstream error") {
		t.Fatalf("5xx log = %q, want upstream error log", buffer.String())
	}

	buffer.Reset()
	logTurnstileVerifyError(io.EOF)
	if !strings.Contains(strings.ToLower(buffer.String()), "transport error") {
		t.Fatalf("transport log = %q, want transport error log", buffer.String())
	}
}

func TestShutdownWithoutStartReturnsNil(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(nil, nil)
	server.httpServer = nil

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil when server was never started", err)
	}
}
