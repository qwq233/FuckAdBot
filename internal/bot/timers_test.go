package bot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func TestScheduleUserTimerRemovesTrackedTimerAfterFire(t *testing.T) {
	b := &Bot{
		timers: make(map[timerKey][]*time.Timer),
	}

	done := make(chan struct{})
	b.scheduleUserTimer(-100123, 42, 10*time.Millisecond, func() {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduleUserTimer() callback did not fire in time")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.timersMu.Lock()
		remaining := len(b.timers)
		b.timersMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("tracked timers were not cleaned up after firing")
}

func TestCancelAllTimersForUserStopsTimersAcrossChats(t *testing.T) {
	b := &Bot{
		timers: make(map[timerKey][]*time.Timer),
	}

	cancelledUserFired := make(chan struct{}, 2)
	otherUserFired := make(chan struct{}, 1)

	b.scheduleUserTimer(-100123, 42, 25*time.Millisecond, func() {
		cancelledUserFired <- struct{}{}
	})
	b.scheduleUserTimer(-100124, 42, 25*time.Millisecond, func() {
		cancelledUserFired <- struct{}{}
	})
	b.scheduleUserTimer(-100125, 99, 25*time.Millisecond, func() {
		otherUserFired <- struct{}{}
	})

	b.cancelAllTimersForUser(42)

	select {
	case <-cancelledUserFired:
		t.Fatal("cancelled user's timer still fired")
	case <-time.After(80 * time.Millisecond):
	}

	select {
	case <-otherUserFired:
	case <-time.After(time.Second):
		t.Fatal("other user's timer did not fire")
	}
}

func TestRestorePendingVerificationsResolvesExpiredPendingImmediately(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "zh-cn",
		Timestamp:    time.Now().Add(-time.Minute).Unix(),
		RandomToken:  "expired1",
		ExpireAt:     time.Now().UTC().Add(-100 * time.Millisecond),
	}
	if err := store.SetPending(pending); err != nil {
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
		Store:  store,
		timers: make(map[timerKey][]*time.Timer),
	}

	if err := b.restorePendingVerifications(nil); err != nil {
		t.Fatalf("restorePendingVerifications() error = %v", err)
	}

	gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending != nil {
		t.Fatalf("GetPending() = %+v, want nil after immediate expiry replay", *gotPending)
	}

	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 1 {
		t.Fatalf("GetWarningCount() = %d, want 1 after immediate expiry replay", warnings)
	}
}

func TestRestorePendingVerificationsSchedulesFutureExpiry(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "zh-cn",
		Timestamp:    time.Now().Unix(),
		RandomToken:  "future01",
		ExpireAt:     time.Now().UTC().Add(40 * time.Millisecond),
	}
	if err := store.SetPending(pending); err != nil {
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
		Store:  store,
		timers: make(map[timerKey][]*time.Timer),
	}

	if err := b.restorePendingVerifications(nil); err != nil {
		t.Fatalf("restorePendingVerifications() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetWarningCount() error = %v", err)
		}
		if warnings == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("scheduled expiry did not replay pending verification in time")
}

func TestPendingForOriginalMessageDeletionSkipsClearedPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := &Bot{
		Config: &config.Config{
			Moderation: config.ModerationConfig{
				VerifyWindow:       "5m",
				OriginalMessageTTL: "10ms",
			},
		},
		Store: store,
	}

	snapshot := &storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "zh-cn",
		Timestamp:         time.Now().Add(-time.Second).Unix(),
		RandomToken:       "oldtoken",
		ExpireAt:          time.Now().UTC().Add(time.Minute),
		OriginalMessageID: 1001,
	}

	target, shouldPersist, err := b.pendingForOriginalMessageDeletion(snapshot, false)
	if err != nil {
		t.Fatalf("pendingForOriginalMessageDeletion() error = %v", err)
	}
	if target != nil || shouldPersist {
		t.Fatalf("pendingForOriginalMessageDeletion() = (%+v, %v), want (nil, false) for cleared pending", target, shouldPersist)
	}
}

func TestPendingForOriginalMessageDeletionSkipsReplacedPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	currentPending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "zh-cn",
		Timestamp:         time.Now().Unix(),
		RandomToken:       "newtoken",
		ExpireAt:          time.Now().UTC().Add(time.Minute),
		OriginalMessageID: 2002,
	}
	if err := store.SetPending(currentPending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	b := &Bot{
		Config: &config.Config{
			Moderation: config.ModerationConfig{
				VerifyWindow:       "5m",
				OriginalMessageTTL: "10ms",
			},
		},
		Store: store,
	}

	snapshot := &storepkg.PendingVerification{
		ChatID:            currentPending.ChatID,
		UserID:            currentPending.UserID,
		UserLanguage:      currentPending.UserLanguage,
		Timestamp:         currentPending.Timestamp - 1,
		RandomToken:       "oldtoken",
		ExpireAt:          currentPending.ExpireAt,
		OriginalMessageID: 1001,
	}

	target, shouldPersist, err := b.pendingForOriginalMessageDeletion(snapshot, false)
	if err != nil {
		t.Fatalf("pendingForOriginalMessageDeletion() error = %v", err)
	}
	if target != nil || shouldPersist {
		t.Fatalf("pendingForOriginalMessageDeletion() = (%+v, %v), want (nil, false) for replaced pending", target, shouldPersist)
	}
}
