package captcha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

//go:embed verify.html
var verifyHTML embed.FS

type Server struct {
	cfg          *config.TurnstileConfig
	store        store.Store
	verifyWindow time.Duration
	botToken     string
	tmpl         *template.Template
	onVerify     func(chatID, userID int64) // callback when user passes verification
	httpServer   *http.Server
}

func NewServer(cfg *config.TurnstileConfig, st store.Store, verifyWindow time.Duration, botToken string, onVerify func(chatID, userID int64)) *Server {
	tmpl := template.Must(template.ParseFS(verifyHTML, "verify.html"))
	return &Server{
		cfg:          cfg,
		store:        st,
		verifyWindow: verifyWindow,
		botToken:     botToken,
		tmpl:         tmpl,
		onVerify:     onVerify,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(config.VerifyPath, s.handleVerifyPage)
	mux.HandleFunc(config.CallbackPath, s.handleCallback)

	addr := fmt.Sprintf("%s:%d", s.cfg.ListenAddr, s.cfg.ListenPort)
	s.httpServer = &http.Server{Addr: addr, Handler: mux}
	log.Printf("[captcha] HTTP server listening on %s", addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the captcha HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// GenerateVerifyURL creates a signed verification URL.
func (s *Server) GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string {
	ts := strconv.FormatInt(timestamp, 10)
	uid := strconv.FormatInt(userID, 10)
	cid := strconv.FormatInt(chatID, 10)
	sig := s.sign(uid, cid, ts, randomToken)

	return fmt.Sprintf("%s?uid=%s&cid=%s&timestamp=%s&rand=%s&sig=%s",
		s.cfg.VerifyURL(),
		uid, cid, ts, randomToken, sig)
}

func (s *Server) sign(uid, cid, timestamp, randomToken string) string {
	mac := hmac.New(sha256.New, []byte(s.botToken))
	mac.Write([]byte(uid + ":" + cid + ":" + timestamp + ":" + randomToken))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifySignature(uid, cid, timestamp, randomToken, sig string) bool {
	expected := s.sign(uid, cid, timestamp, randomToken)
	return hmac.Equal([]byte(expected), []byte(sig))
}

type verificationRequestError struct {
	status  int
	message string
}

func (e *verificationRequestError) Error() string {
	return e.message
}

func (s *Server) validateVerificationRequest(uid, cid, timestamp, randomToken, sig string) (int64, int64, error) {
	if uid == "" || cid == "" || timestamp == "" || randomToken == "" || sig == "" {
		return 0, 0, &verificationRequestError{status: http.StatusBadRequest, message: "Missing parameters"}
	}

	if !s.verifySignature(uid, cid, timestamp, randomToken, sig) {
		return 0, 0, &verificationRequestError{status: http.StatusForbidden, message: "Invalid signature"}
	}

	chatID, err := strconv.ParseInt(cid, 10, 64)
	if err != nil {
		return 0, 0, &verificationRequestError{status: http.StatusBadRequest, message: "Invalid chat id"}
	}

	userID, err := strconv.ParseInt(uid, 10, 64)
	if err != nil {
		return 0, 0, &verificationRequestError{status: http.StatusBadRequest, message: "Invalid user id"}
	}

	timestampValue, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return 0, 0, &verificationRequestError{status: http.StatusBadRequest, message: "Invalid timestamp"}
	}

	age := time.Since(time.Unix(timestampValue, 0))
	if age < 0 || age > s.verifyWindow {
		return 0, 0, &verificationRequestError{status: http.StatusGone, message: "链接已过期，请在群组中重新触发验证"}
	}

	pending, err := s.store.GetPending(chatID, userID)
	if err != nil {
		return 0, 0, err
	}
	if pending == nil {
		return 0, 0, &verificationRequestError{status: http.StatusGone, message: "链接已失效，请在群组中重新触发验证"}
	}

	if pending.Timestamp != timestampValue || pending.RandomToken != randomToken {
		return 0, 0, &verificationRequestError{status: http.StatusForbidden, message: "链接已失效，请重新触发验证"}
	}

	if !pending.ExpireAt.After(time.Now().UTC()) {
		return 0, 0, &verificationRequestError{status: http.StatusGone, message: "链接已过期，请在群组中重新触发验证"}
	}

	return chatID, userID, nil
}

func verificationErrorStatus(err error) int {
	var validationErr *verificationRequestError
	if errors.As(err, &validationErr) {
		return validationErr.status
	}

	return http.StatusInternalServerError
}

func (s *Server) handleVerifyPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uid := r.URL.Query().Get("uid")
	cid := r.URL.Query().Get("cid")
	timestamp := r.URL.Query().Get("timestamp")
	randomToken := r.URL.Query().Get("rand")
	sig := r.URL.Query().Get("sig")

	_, _, err := s.validateVerificationRequest(uid, cid, timestamp, randomToken, sig)
	if err != nil {
		http.Error(w, err.Error(), verificationErrorStatus(err))
		return
	}

	data := struct {
		SiteKey     string
		CallbackURL string
		UID         string
		CID         string
		Timestamp   string
		Rand        string
		Sig         string
	}{
		SiteKey:     s.cfg.SiteKey,
		CallbackURL: s.cfg.CallbackURL(),
		UID:         uid,
		CID:         cid,
		Timestamp:   timestamp,
		Rand:        randomToken,
		Sig:         sig,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, data); err != nil {
		log.Printf("[captcha] template execute error: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}
