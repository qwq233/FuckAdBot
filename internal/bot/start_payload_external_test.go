package bot_test

import (
	"strings"
	"testing"

	botpkg "github.com/qwq233/fuckadbot/internal/bot"
)

func TestVerificationStartPayloadRoundTrip(t *testing.T) {
	t.Parallel()

	payload := botpkg.BuildVerificationStartPayload(-100123, 42, 7001)
	chatID, userID, verificationInfoID, err := botpkg.ParseVerificationStartPayload(payload)
	if err != nil {
		t.Fatalf("ParseVerificationStartPayload() error = %v", err)
	}

	if chatID != -100123 || userID != 42 || verificationInfoID != 7001 {
		t.Fatalf("parsed values = (%d, %d, %d), want (%d, %d, %d)", chatID, userID, verificationInfoID, int64(-100123), int64(42), int64(7001))
	}
}

func TestVerificationStartURLContainsPayload(t *testing.T) {
	t.Parallel()

	url := botpkg.BuildVerificationStartURL("FuckAdBot", -100123, 42, 7001)
	if !strings.Contains(url, "https://t.me/FuckAdBot?start=verify_") {
		t.Fatalf("BuildVerificationStartURL() = %q, want bot start deep link", url)
	}
}

func TestParseVerificationStartPayloadRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	cases := []string{
		"bad",
		"verify_only_two_parts",
		"verify_nope_42_7001",
		"verify_-100123_bad_7001",
		"verify_-100123_42_bad",
	}

	for _, payload := range cases {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			if _, _, _, err := botpkg.ParseVerificationStartPayload(payload); err == nil {
				t.Fatalf("ParseVerificationStartPayload(%q) error = nil, want error", payload)
			}
		})
	}
}
