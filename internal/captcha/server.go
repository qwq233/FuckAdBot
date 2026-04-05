package captcha

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
)

//go:embed verify.html
var verifyHTML embed.FS

type Server struct {
	cfg      *config.TurnstileConfig
	botToken string
	tmpl     *template.Template
	onVerify func(chatID, userID int64) // callback when user passes verification
}

func NewServer(cfg *config.TurnstileConfig, botToken string, onVerify func(chatID, userID int64)) *Server {
	tmpl := template.Must(template.ParseFS(verifyHTML, "verify.html"))
	return &Server{
		cfg:      cfg,
		botToken: botToken,
		tmpl:     tmpl,
		onVerify: onVerify,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(config.VerifyPath, s.handleVerifyPage)
	mux.HandleFunc(config.CallbackPath, s.handleCallback)

	addr := fmt.Sprintf("%s:%d", s.cfg.ListenAddr, s.cfg.ListenPort)
	log.Printf("[captcha] HTTP server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// GenerateVerifyURL creates a signed verification URL.
func (s *Server) GenerateVerifyURL(chatID, userID int64) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	uid := strconv.FormatInt(userID, 10)
	cid := strconv.FormatInt(chatID, 10)
	sig := s.sign(uid, cid, ts)

	return fmt.Sprintf("%s?uid=%s&cid=%s&ts=%s&sig=%s",
		s.cfg.VerifyURL(),
		uid, cid, ts, sig)
}

func (s *Server) sign(uid, cid, ts string) string {
	mac := hmac.New(sha256.New, []byte(s.botToken))
	mac.Write([]byte(uid + ":" + cid + ":" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifySignature(uid, cid, ts, sig string) bool {
	expected := s.sign(uid, cid, ts)
	return hmac.Equal([]byte(expected), []byte(sig))
}

func (s *Server) handleVerifyPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uid := r.URL.Query().Get("uid")
	cid := r.URL.Query().Get("cid")
	ts := r.URL.Query().Get("ts")
	sig := r.URL.Query().Get("sig")

	if uid == "" || cid == "" || ts == "" || sig == "" {
		http.Error(w, "Invalid parameters", http.StatusBadRequest)
		return
	}

	if !s.verifySignature(uid, cid, ts, sig) {
		http.Error(w, "Invalid signature", http.StatusForbidden)
		return
	}

	// Check if link is expired
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(tsInt, 0)) > s.cfg.GetVerifyTimeout() {
		http.Error(w, "链接已过期，请在群组中重新触发验证", http.StatusGone)
		return
	}

	data := struct {
		SiteKey     string
		CallbackURL string
		UID         string
		CID         string
		TS          string
		Sig         string
	}{
		SiteKey:     s.cfg.SiteKey,
		CallbackURL: s.cfg.CallbackURL(),
		UID:         uid,
		CID:         cid,
		TS:          ts,
		Sig:         sig,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, data); err != nil {
		log.Printf("[captcha] template execute error: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}
