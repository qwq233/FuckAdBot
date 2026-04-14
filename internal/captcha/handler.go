package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
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

	r.Body = http.MaxBytesReader(w, r.Body, serverMaxFormBytes)
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
	ok, err := s.verifyTurnstile(r.Context(), cfToken, extractClientIP(r))
	if err != nil {
		logTurnstileVerifyError(err)
		if errors.Is(err, context.DeadlineExceeded) {
			s.recordTimeout()
		} else {
			s.recordFailure()
		}
		writeJSON(w, false, "验证服务异常，请稍后重试")
		return
	}
	if !ok {
		s.recordFailure()
		writeJSON(w, false, "人机验证未通过，请重试")
		return
	}

	s.recordSuccess()

	if s.onVerify != nil {
		s.onVerify(VerifiedToken{
			ChatID:      chatID,
			UserID:      userID,
			Timestamp:   mustParseTimestamp(timestamp),
			RandomToken: randomToken,
		})
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
			// Take the rightmost entry: it's appended by the nearest trusted proxy
			// and cannot be injected by the client (unlike the leftmost entry).
			value = strings.TrimSpace(parts[len(parts)-1])
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

func (s *Server) verifyTurnstile(ctx context.Context, token, remoteIP string) (bool, error) {
	form := url.Values{
		"secret":   {s.cfg.SecretKey},
		"response": {token},
		"remoteip": {remoteIP},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: turnstileVerifyRequestTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("turnstile siteverify returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

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

func mustParseTimestamp(value string) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func logTurnstileVerifyError(err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		log.Printf("[captcha] turnstile verify timeout: %v", err)
	case strings.Contains(strings.ToLower(err.Error()), "status 4"):
		log.Printf("[captcha] turnstile verify upstream rejected request: %v", err)
	case strings.Contains(strings.ToLower(err.Error()), "status 5"):
		log.Printf("[captcha] turnstile verify upstream error: %v", err)
	default:
		log.Printf("[captcha] turnstile verify transport error: %v", err)
	}
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
