package captcha

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

// ---------- helpers ----------

func newTestServer(token string) *Server {
	cfg := &config.TurnstileConfig{
		Domain:        "example.com",
		ListenAddr:    "127.0.0.1",
		ListenPort:    0,
		VerifyTimeout: "5m",
	}
	return &Server{
		cfg:          cfg,
		botToken:     token,
		verifyWindow: 5 * time.Minute,
		store:        &stubStore{},
	}
}

// stubStore satisfies store.Store with no-op implementations.
// Only GetPending is meaningful for this test suite.
type stubStore struct {
	pendingFn func(chatID, userID int64) (*store.PendingVerification, error)
}

func (s *stubStore) GetPending(chatID, userID int64) (*store.PendingVerification, error) {
	if s.pendingFn != nil {
		return s.pendingFn(chatID, userID)
	}
	return nil, nil
}

func (s *stubStore) CreatePendingIfAbsent(pending store.PendingVerification) (bool, *store.PendingVerification, error) {
	return true, nil, nil
}

func (s *stubStore) Close() error                                 { return nil }
func (s *stubStore) IsVerified(_, _ int64) (bool, error)          { return false, nil }
func (s *stubStore) SetVerified(_, _ int64) error                 { return nil }
func (s *stubStore) RemoveVerified(_, _ int64) error              { return nil }
func (s *stubStore) IsRejected(_, _ int64) (bool, error)          { return false, nil }
func (s *stubStore) SetRejected(_, _ int64) error                 { return nil }
func (s *stubStore) RemoveRejected(_, _ int64) error              { return nil }
func (s *stubStore) HasActivePending(_, _ int64) (bool, error)    { return false, nil }
func (s *stubStore) SetPending(_ store.PendingVerification) error { return nil }
func (s *stubStore) UpdatePendingMetadataByToken(store.PendingVerification) (bool, error) {
	return true, nil
}
func (s *stubStore) ClearPending(_, _ int64) error { return nil }
func (s *stubStore) ResolvePendingByToken(_, _ int64, _ int64, _ string, _ store.PendingAction, _ int) (store.PendingResolutionResult, error) {
	return store.PendingResolutionResult{}, nil
}
func (s *stubStore) ClearUserVerificationStateEverywhere(_ int64) error { return nil }
func (s *stubStore) GetWarningCount(_, _ int64) (int, error)            { return 0, nil }
func (s *stubStore) IncrWarningCount(_, _ int64) (int, error)           { return 0, nil }
func (s *stubStore) ResetWarningCount(_, _ int64) error                 { return nil }
func (s *stubStore) GetBlacklistWords(_ int64) ([]string, error)        { return nil, nil }
func (s *stubStore) AddBlacklistWord(_ int64, _, _ string) error        { return nil }
func (s *stubStore) RemoveBlacklistWord(_ int64, _ string) error        { return nil }
func (s *stubStore) GetAllBlacklistWords() (map[int64][]string, error)  { return nil, nil }
func (s *stubStore) GetUserLanguagePreference(_ int64) (string, error)  { return "", nil }
func (s *stubStore) SetUserLanguagePreference(_ int64, _ string) error  { return nil }

// ---------- HMAC signature tests ----------

func TestSignatureRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	uid, cid, ts, rand := "42", "-100123", "1712300000", "abcdefg"
	sig := s.sign(uid, cid, ts, rand)

	if !s.verifySignature(uid, cid, ts, rand, sig) {
		t.Fatal("verifySignature() = false, want true for own signature")
	}
}

func TestSignatureDetectsTampering(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	uid, cid, ts, rand := "42", "-100123", "1712300000", "abcdefg"
	sig := s.sign(uid, cid, ts, rand)

	cases := []struct {
		name string
		uid  string
		cid  string
		ts   string
		rand string
	}{
		{"tampered uid", "99", cid, ts, rand},
		{"tampered cid", uid, "-999", ts, rand},
		{"tampered timestamp", uid, cid, "9999999999", rand},
		{"tampered rand", uid, cid, ts, "xxxxxxx"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if s.verifySignature(tc.uid, tc.cid, tc.ts, tc.rand, sig) {
				t.Fatalf("verifySignature() = true, want false for %s", tc.name)
			}
		})
	}
}

func TestSignatureDifferentTokenFails(t *testing.T) {
	t.Parallel()

	s1 := newTestServer("token-A")
	s2 := newTestServer("token-B")

	uid, cid, ts, rand := "42", "-100123", "1712300000", "abcdefg"
	sig := s1.sign(uid, cid, ts, rand)

	if s2.verifySignature(uid, cid, ts, rand, sig) {
		t.Fatal("verifySignature() = true, want false when signed by a different token")
	}
}

func TestSignatureEmptyInputFails(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	uid, cid, ts, rand := "42", "-100123", "1712300000", "abcdefg"
	sig := s.sign(uid, cid, ts, rand)

	if s.verifySignature("", "", "", "", sig) {
		t.Fatal("verifySignature() = true, want false for empty parameters")
	}
}

// ---------- validateVerificationRequest tests ----------

func TestValidateVerificationRequestRejectsMissingFields(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	_, _, err := s.validateVerificationRequest("", "", "", "", "")
	if err == nil {
		t.Fatal("validateVerificationRequest() error = nil, want error for empty fields")
	}
}

func TestValidateVerificationRequestRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	uid, cid := "42", "-100123"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rand := "abcdefg"

	_, _, err := s.validateVerificationRequest(uid, cid, ts, rand, "badsig")
	if err == nil {
		t.Fatal("validateVerificationRequest() error = nil, want error for bad signature")
	}
}

func TestValidateVerificationRequestRejectsExpiredTimestamp(t *testing.T) {
	t.Parallel()

	s := newTestServer("test-bot-token")
	uid, cid := "42", "-100123"
	// Timestamp well in the past (beyond 5 min window)
	oldTS := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	rand := "abcdefg"
	sig := s.sign(uid, cid, oldTS, rand)

	_, _, err := s.validateVerificationRequest(uid, cid, oldTS, rand, sig)
	if err == nil {
		t.Fatal("validateVerificationRequest() error = nil, want error for expired timestamp")
	}
}

func TestValidateVerificationRequestRejectsWhenNoPendingRecord(t *testing.T) {
	t.Parallel()

	// stubStore returns nil pending by default.
	s := newTestServer("test-bot-token")
	uid, cid := "42", "-100123"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rand := "abcdefg"
	sig := s.sign(uid, cid, ts, rand)

	_, _, err := s.validateVerificationRequest(uid, cid, ts, rand, sig)
	if err == nil {
		t.Fatal("validateVerificationRequest() error = nil, want error when no pending record exists")
	}
}

func TestValidateVerificationRequestAcceptsValidRequest(t *testing.T) {
	t.Parallel()

	const (
		chatID int64 = -100123
		userID int64 = 42
	)

	ts := time.Now().Unix()
	rand := "abcdefg"
	expireAt := time.Now().UTC().Add(5 * time.Minute)

	stub := &stubStore{
		pendingFn: func(cid, uid int64) (*store.PendingVerification, error) {
			if cid != chatID || uid != userID {
				return nil, nil
			}
			return &store.PendingVerification{
				ChatID:      chatID,
				UserID:      userID,
				Timestamp:   ts,
				RandomToken: rand,
				ExpireAt:    expireAt,
			}, nil
		},
	}

	s := &Server{
		cfg:          &config.TurnstileConfig{Domain: "example.com"},
		botToken:     "test-bot-token",
		verifyWindow: 5 * time.Minute,
		store:        stub,
	}

	uidStr := strconv.FormatInt(userID, 10)
	cidStr := strconv.FormatInt(chatID, 10)
	tsStr := strconv.FormatInt(ts, 10)
	sig := s.sign(uidStr, cidStr, tsStr, rand)

	gotChatID, gotUserID, err := s.validateVerificationRequest(uidStr, cidStr, tsStr, rand, sig)
	if err != nil {
		t.Fatalf("validateVerificationRequest() error = %v, want nil", err)
	}
	if gotChatID != chatID || gotUserID != userID {
		t.Fatalf("validateVerificationRequest() = (%d, %d), want (%d, %d)", gotChatID, gotUserID, chatID, userID)
	}
}

func TestValidateVerificationRequestRejectsTokenMismatch(t *testing.T) {
	t.Parallel()

	const (
		chatID int64 = -100123
		userID int64 = 42
	)

	ts := time.Now().Unix()
	rand := "abcdefg"
	expireAt := time.Now().UTC().Add(5 * time.Minute)

	stub := &stubStore{
		pendingFn: func(_, _ int64) (*store.PendingVerification, error) {
			return &store.PendingVerification{
				ChatID:      chatID,
				UserID:      userID,
				Timestamp:   ts,
				RandomToken: "different-rand", // mismatch
				ExpireAt:    expireAt,
			}, nil
		},
	}

	s := &Server{
		cfg:          &config.TurnstileConfig{Domain: "example.com"},
		botToken:     "test-bot-token",
		verifyWindow: 5 * time.Minute,
		store:        stub,
	}

	uidStr := strconv.FormatInt(userID, 10)
	cidStr := strconv.FormatInt(chatID, 10)
	tsStr := strconv.FormatInt(ts, 10)
	sig := s.sign(uidStr, cidStr, tsStr, rand)

	_, _, err := s.validateVerificationRequest(uidStr, cidStr, tsStr, rand, sig)
	if err == nil {
		t.Fatal("validateVerificationRequest() error = nil, want error for random-token mismatch")
	}
}

func TestStartFailsFastWhenPortIsOccupied(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	server := NewServer(&config.TurnstileConfig{
		Domain:     "example.com",
		ListenAddr: "127.0.0.1",
		ListenPort: port,
	}, &stubStore{}, 5*time.Minute, "token", nil)

	if err := server.Start(); err == nil {
		t.Fatal("Start() error = nil, want bind failure when port is occupied")
	}
}

func TestVerifyTurnstileReturnsErrorOnTimeout(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}

	if _, err := server.verifyTurnstile(context.Background(), "cf-token", "127.0.0.1"); err == nil {
		t.Fatal("verifyTurnstile() error = nil, want timeout error")
	}
}

func TestVerifyTurnstileRejectsNon200Response(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("upstream bad gateway")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	if _, err := server.verifyTurnstile(context.Background(), "cf-token", "127.0.0.1"); err == nil {
		t.Fatal("verifyTurnstile() error = nil, want upstream status error")
	}
}

func TestVerifyTurnstileRejectsHostnameMismatch(t *testing.T) {
	t.Parallel()

	server := newTestServer("token")
	server.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"hostname":"other.example.com"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	ok, err := server.verifyTurnstile(context.Background(), "cf-token", "127.0.0.1")
	if err != nil {
		t.Fatalf("verifyTurnstile() error = %v, want nil", err)
	}
	if ok {
		t.Fatal("verifyTurnstile() ok = true, want false for hostname mismatch")
	}
}
