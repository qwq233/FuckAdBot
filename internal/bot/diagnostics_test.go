package bot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func TestIsGroupAdminUsesCacheAndTracksStats(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	client.SetResponder("getChatMember", func(params map[string]any) (json.RawMessage, error) {
		userID := toInt64(params["user_id"])
		return json.RawMessage(fmt.Sprintf(`{"status":"administrator","user":{"id":%d,"is_bot":false,"first_name":"Admin"}}`, userID)), nil
	})

	b := newTestBot(t, nil, client)
	if !b.isGroupAdmin(b.Bot, -100123, 42) {
		t.Fatal("isGroupAdmin() = false, want true on first lookup")
	}
	if !b.isGroupAdmin(b.Bot, -100123, 42) {
		t.Fatal("isGroupAdmin() = false, want true from cache")
	}

	if got := len(client.RequestsByMethod("getChatMember")); got != 1 {
		t.Fatalf("getChatMember request count = %d, want 1 with cache hit", got)
	}

	snapshot := b.runtimeStats.snapshot()
	if snapshot.AdminCacheMisses != 1 || snapshot.AdminCacheHits != 1 {
		t.Fatalf("admin cache stats = %+v, want 1 miss and 1 hit", snapshot)
	}
}

func TestRunPendingSweeperTickExpiresPendingAndDeletesOriginalMessage(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	now := time.Now().UTC()

	expiredPending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         now.Add(-time.Minute).Unix(),
		RandomToken:       "expired-a",
		ExpireAt:          now.Add(-time.Second),
		OriginalMessageID: 1001,
	}
	if err := b.Store.SetPending(expiredPending); err != nil {
		t.Fatalf("SetPending(expired) error = %v", err)
	}

	activePending := storepkg.PendingVerification{
		ChatID:            -100124,
		UserID:            43,
		UserLanguage:      "en",
		Timestamp:         now.Add(-time.Minute).Unix(),
		RandomToken:       "active-a",
		ExpireAt:          now.Add(time.Minute),
		OriginalMessageID: 1002,
	}
	if err := b.Store.SetPending(activePending); err != nil {
		t.Fatalf("SetPending(active) error = %v", err)
	}

	b.runPendingSweeperTick(b.Bot)

	if pending, err := b.Store.GetPending(expiredPending.ChatID, expiredPending.UserID); err != nil || pending != nil {
		t.Fatalf("GetPending(expired) = (%+v, %v), want nil after expiry", pending, err)
	}

	activeAfter, err := b.Store.GetPending(activePending.ChatID, activePending.UserID)
	if err != nil {
		t.Fatalf("GetPending(active) error = %v", err)
	}
	if activeAfter == nil || activeAfter.OriginalMessageID != 0 {
		t.Fatalf("active pending after sweep = %+v, want original message cleared", activeAfter)
	}

	if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
		t.Fatalf("deleteMessage request count = %d, want 2 for expired + ttl cleanup", got)
	}

	snapshot := b.runtimeStats.snapshot()
	if snapshot.LastSweeperScanned != 2 || snapshot.LastSweeperExpired != 1 {
		t.Fatalf("sweeper snapshot = %+v, want scanned=2 expired=1", snapshot)
	}
}

func TestRestorePendingVerificationsDoesNotScheduleFutureTimers(t *testing.T) {
	t.Parallel()

	st, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer st.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "future-b",
		ExpireAt:     time.Now().UTC().Add(time.Minute),
	}
	if err := st.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	b := &Bot{
		Config: &config.Config{
			Moderation: config.ModerationConfig{
				MaxWarnings:        3,
				VerifyWindow:       "5m",
				OriginalMessageTTL: "1m",
			},
		},
		Store:  st,
		timers: make(map[timerKey][]*time.Timer),
	}

	if err := b.restorePendingVerifications(nil); err != nil {
		t.Fatalf("restorePendingVerifications() error = %v", err)
	}
	if len(b.timers) != 0 {
		t.Fatalf("timers map = %+v, want empty because sweeper handles future expiries", b.timers)
	}
}

func TestCmdHealthRendersCompactRuntimeSummary(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	b.runtimeStats.recordError("compact test error")

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/health",
	}
	if err := b.cmdHealth(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdHealth() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}

	text := requestText(requests[0])
	if !strings.Contains(text, "Health") || !strings.Contains(text, "Pending backlog") || !strings.Contains(text, "Recent errors") {
		t.Fatalf("health text = %q, want compact runtime summary", text)
	}
}

func TestFormatRecentErrorsEscapesEntries(t *testing.T) {
	t.Parallel()

	got := formatRecentErrors([]string{"bad <tag>", "oops & retry"}, "en")
	want := "1. <code>bad &lt;tag&gt;</code>\n2. <code>oops &amp; retry</code>"
	if got != want {
		t.Fatalf("formatRecentErrors() = %q, want %q", got, want)
	}
}

func TestFormatBytesUsesBinaryUnits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input uint64
		want  string
	}{
		{name: "bytes", input: 999, want: "999 B"},
		{name: "kib", input: 1536, want: "1.5 KiB"},
		{name: "mib", input: 5 * 1024 * 1024, want: "5.0 MiB"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := formatBytes(tc.input); got != tc.want {
				t.Fatalf("formatBytes(%d) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
