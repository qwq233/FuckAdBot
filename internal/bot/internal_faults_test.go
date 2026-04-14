package bot

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func TestInternalFaultMonitorTriggersAtThreshold(t *testing.T) {
	var monitor internalFaultMonitor
	base := time.Date(2026, time.April, 14, 1, 0, 0, 0, time.UTC)

	for index := 0; index < internalFaultThreshold-1; index++ {
		if snapshot, triggered := monitor.recordAt(base.Add(time.Duration(index)*time.Second), "store.reserve_verification_window", fmt.Errorf("db down %d", index)); triggered {
			t.Fatalf("recordAt() triggered early with snapshot=%+v", snapshot)
		}
	}

	snapshot, triggered := monitor.recordAt(base.Add(time.Duration(internalFaultThreshold-1)*time.Second), "store.reserve_verification_window", errors.New("db down"))
	if !triggered {
		t.Fatal("recordAt() did not trigger at threshold")
	}
	if snapshot.Count != internalFaultThreshold {
		t.Fatalf("snapshot.Count = %d, want %d", snapshot.Count, internalFaultThreshold)
	}
	if snapshot.Threshold != internalFaultThreshold {
		t.Fatalf("snapshot.Threshold = %d, want %d", snapshot.Threshold, internalFaultThreshold)
	}
	if snapshot.Window != internalFaultWindow {
		t.Fatalf("snapshot.Window = %v, want %v", snapshot.Window, internalFaultWindow)
	}
	if len(snapshot.Recent) == 0 || !strings.Contains(snapshot.Recent[0], "db down") {
		t.Fatalf("snapshot.Recent = %v, want recent db-down summary", snapshot.Recent)
	}

	if snapshot, triggered := monitor.recordAt(base.Add(time.Duration(internalFaultThreshold)*time.Second), "store.reserve_verification_window", errors.New("still down")); triggered {
		t.Fatalf("recordAt() retriggered with snapshot=%+v, want one-shot fuse", snapshot)
	}
}

func TestInternalFaultMonitorPrunesExpiredEntries(t *testing.T) {
	var monitor internalFaultMonitor
	base := time.Date(2026, time.April, 14, 2, 0, 0, 0, time.UTC)
	oldAt := base.Add(-internalFaultWindow - time.Second)

	for index := 0; index < internalFaultThreshold-1; index++ {
		if _, triggered := monitor.recordAt(oldAt, "store.list_pending_verifications", errors.New("old failure")); triggered {
			t.Fatal("recordAt() triggered for expired entries")
		}
	}

	if snapshot, triggered := monitor.recordAt(base, "store.list_pending_verifications", errors.New("fresh failure")); triggered {
		t.Fatalf("recordAt() triggered after pruning with snapshot=%+v", snapshot)
	}
	if got := len(monitor.entries); got != 1 {
		t.Fatalf("len(entries) = %d, want 1 fresh entry after pruning", got)
	}
	if got := monitor.entries[0].summary; got != "store.list_pending_verifications: fresh failure" {
		t.Fatalf("fresh summary = %q, want %q", got, "store.list_pending_verifications: fresh failure")
	}
}

func TestRecordInternalFaultSendsAlertAndFatal(t *testing.T) {
	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	b.Config.Bot.Admins = []int64{7, 8}

	for index := 0; index < internalFaultThreshold-1; index++ {
		b.recordInternalFault("store.reserve_verification_window", fmt.Errorf("db down %d", index))
	}

	select {
	case err := <-b.Errors():
		t.Fatalf("Errors() received %v before threshold", err)
	default:
	}

	b.recordInternalFault("store.reserve_verification_window", errors.New("db down"))

	select {
	case err := <-b.Errors():
		if err == nil || !strings.Contains(err.Error(), "internal fault fuse triggered") {
			t.Fatalf("Errors() = %v, want internal fault fuse error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Errors() did not receive fuse failure")
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 2 {
		t.Fatalf("sendMessage request count = %d, want 2 admin alerts", len(requests))
	}
	text := requestText(requests[0])
	if !strings.Contains(text, "程序内部错误熔断") || !strings.Contains(text, "10分钟内达到20次程序内部错误") || !strings.Contains(text, "store.reserve_verification_window: db down") {
		t.Fatalf("alert text = %q, want Chinese fuse summary with recent error", text)
	}
}

func TestRecordInternalFaultStillTripsWhenAlertSendFails(t *testing.T) {
	client := &recordingBotClient{}
	client.SetError("sendMessage", errors.New("Forbidden: not enough rights"))

	b := newTestBot(t, nil, client)
	b.Config.Bot.Admins = []int64{7}

	for index := 0; index < internalFaultThreshold; index++ {
		b.recordInternalFault("captcha.server", fmt.Errorf("serve failed %d", index))
	}

	select {
	case err := <-b.Errors():
		if err == nil || !strings.Contains(err.Error(), "internal fault fuse triggered") {
			t.Fatalf("Errors() = %v, want internal fault fuse error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Errors() did not receive fuse failure when alert send failed")
	}

	if got := len(client.RequestsByMethod("sendMessage")); got != 1 {
		t.Fatalf("sendMessage request count = %d, want one best-effort alert attempt", got)
	}
}

func TestHandleMessageInternalStoreErrorsTripFuse(t *testing.T) {
	t.Helper()

	baseStore, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer baseStore.Close()

	b := newTestBot(t, &hookedStore{
		Store: baseStore,
		reserveVerificationWindowHook: func(pending storepkg.PendingVerification, maxWarnings int) (storepkg.VerificationReservationResult, error) {
			return storepkg.VerificationReservationResult{}, errors.New("sqlite unavailable")
		},
	}, &recordingBotClient{})
	b.Config.Bot.Admins = nil

	for index := 0; index < internalFaultThreshold; index++ {
		msg := &gotgbot.Message{
			MessageId: int64(1000 + index),
			Text:      "hello",
			Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
			From:      &gotgbot.User{Id: 42, FirstName: "Alice", LanguageCode: "en"},
		}
		if err := b.handleMessage(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("handleMessage() error = %v", err)
		}
	}

	select {
	case err := <-b.Errors():
		if err == nil {
			t.Fatal("Errors() = nil, want fuse failure")
		}
	case <-time.After(time.Second):
		t.Fatal("Errors() did not receive fuse failure from hot-path store errors")
	}
}

func TestSweeperInternalStoreErrorsTripFuse(t *testing.T) {
	baseStore, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer baseStore.Close()

	b := newTestBot(t, &hookedStore{
		Store: baseStore,
		listPendingVerificationsHook: func() ([]storepkg.PendingVerification, error) {
			return nil, errors.New("backend unavailable")
		},
	}, &recordingBotClient{})
	b.Config.Bot.Admins = nil

	for index := 0; index < internalFaultThreshold; index++ {
		b.runPendingSweeperTick(b.Bot)
	}

	select {
	case err := <-b.Errors():
		if err == nil {
			t.Fatal("Errors() = nil, want fuse failure")
		}
	case <-time.After(time.Second):
		t.Fatal("Errors() did not receive fuse failure from sweeper store errors")
	}
}

func TestDiagnosticsErrorsDoNotTripInternalFaultFuse(t *testing.T) {
	b := newTestBot(t, nil, &recordingBotClient{})
	b.Config.Bot.Admins = nil

	for index := 0; index < internalFaultThreshold+5; index++ {
		b.runtimeStats.recordErrorf("diagnostics pending backlog: %d", index)
	}

	select {
	case err := <-b.Errors():
		t.Fatalf("Errors() received %v, want diagnostics errors to be ignored", err)
	default:
	}
}

func TestTelegramAPIErrorsDoNotTripInternalFaultFuse(t *testing.T) {
	client := &recordingBotClient{}
	client.SetError("getChatMember", errors.New("Forbidden: not enough rights"))
	client.SetError("getChat", errors.New("chat not found"))

	b := newTestBot(t, nil, client)
	b.Config.Bot.Admins = nil

	for index := 0; index < internalFaultThreshold+5; index++ {
		if b.isGroupAdmin(b.Bot, -100123, int64(1000+index)) {
			t.Fatalf("isGroupAdmin() = true for denied Telegram response at iteration %d", index)
		}
	}

	for index := 0; index < internalFaultThreshold+5; index++ {
		got := b.matchUserAgainstBlacklist(b.Bot, -100123, &gotgbot.User{Id: int64(2000 + index), FirstName: "Alice"})
		if got != "" {
			t.Fatalf("matchUserAgainstBlacklist() = %q, want empty match when Telegram GetChat fails", got)
		}
	}

	select {
	case err := <-b.Errors():
		t.Fatalf("Errors() received %v, want Telegram API errors to be ignored", err)
	default:
	}
}
