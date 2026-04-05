package captcha

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type turnstileResponse struct {
	Success  bool     `json:"success"`
	Hostname string   `json:"hostname"`
	Errors   []string `json:"error-codes"`
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := parseVerificationRequest(r); err != nil {
		writeJSON(w, false, "Invalid form data")
		return
	}

	uid := r.FormValue("uid")
	cid := r.FormValue("cid")
	timestamp := r.FormValue("timestamp")
	randomToken := r.FormValue("rand")
	sig := r.FormValue("sig")
	cfToken := r.FormValue("cf-turnstile-response")

	if cfToken == "" {
		writeJSON(w, false, "Missing parameters")
		return
	}

	chatID, userID, err := s.validateVerificationRequest(uid, cid, timestamp, randomToken, sig)
	if err != nil {
		writeJSON(w, false, err.Error())
		return
	}

	// Verify Turnstile token with Cloudflare
	ok, err := s.verifyTurnstile(cfToken, extractClientIP(r))
	if err != nil {
		log.Printf("[captcha] turnstile verify error: %v", err)
		writeJSON(w, false, "验证服务异常，请稍后重试")
		return
	}
	if !ok {
		writeJSON(w, false, "人机验证未通过，请重试")
		return
	}

	if s.onVerify != nil {
		s.onVerify(chatID, userID)
	}

	writeJSON(w, true, "验证成功")
}

func parseVerificationRequest(r *http.Request) error {
	if err := r.ParseMultipartForm(1 << 20); err == nil {
		return nil
	} else if !errors.Is(err, http.ErrNotMultipart) {
		return err
	}

	return r.ParseForm()
}

func extractClientIP(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value == "" {
			continue
		}

		if header == "X-Forwarded-For" {
			parts := strings.Split(value, ",")
			value = strings.TrimSpace(parts[0])
		}

		if host, _, err := net.SplitHostPort(value); err == nil {
			return host
		}

		return value
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}

	return strings.TrimSpace(r.RemoteAddr)
}

func (s *Server) verifyTurnstile(token, remoteIP string) (bool, error) {
	resp, err := http.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", url.Values{
		"secret":   {s.cfg.SecretKey},
		"response": {token},
		"remoteip": {remoteIP},
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result turnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	if !result.Success {
		log.Printf("[captcha] turnstile rejected: errors=%v", result.Errors)
		return false, nil
	}

	// Optionally verify hostname
	if s.cfg.Domain != "" && result.Hostname != s.cfg.Domain {
		log.Printf("[captcha] hostname mismatch: expected %s, got %s", s.cfg.Domain, result.Hostname)
		return false, nil
	}

	return true, nil
}

func writeJSON(w http.ResponseWriter, success bool, message string) {
	w.Header().Set("Content-Type", "application/json")
	if !success {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": success,
		"message": message,
	})
}
