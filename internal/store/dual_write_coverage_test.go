package store

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/qwq233/fuckadbot/internal/config"
)

func TestDualWriteQueueDeleteBatchAndHelpers(t *testing.T) {
	t.Parallel()

	queue, err := newDualWriteQueue(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("newDualWriteQueue() error = %v", err)
	}
	t.Cleanup(func() {
		_ = queue.Close()
	})

	events := make([]dualWriteQueueItem, 0, dualWriteDeleteBatchChunkSize+1)
	for i := 0; i < dualWriteDeleteBatchChunkSize+1; i++ {
		events = append(events, dualWriteQueueItem{
			Kind:    dualWriteEventSyncPreference,
			Payload: []byte(strconv.FormatInt(int64(i+1), 10)),
		})
	}

	inserted, err := queue.EnqueueBatch(events)
	if err != nil {
		t.Fatalf("EnqueueBatch() error = %v", err)
	}
	if inserted != len(events) {
		t.Fatalf("EnqueueBatch() inserted = %d, want %d", inserted, len(events))
	}

	enqueued, err := queue.PeekBatch(len(events))
	if err != nil {
		t.Fatalf("PeekBatch() error = %v", err)
	}
	if len(enqueued) != len(events) {
		t.Fatalf("len(PeekBatch()) = %d, want %d", len(enqueued), len(events))
	}

	if deleted, err := queue.Delete(enqueued[0].ID); err != nil || deleted != 1 {
		t.Fatalf("Delete() = (%d, %v), want (1, nil)", deleted, err)
	}

	remainingIDs := make([]int64, 0, len(enqueued)-1)
	for _, event := range enqueued[1:] {
		remainingIDs = append(remainingIDs, event.ID)
	}
	deleted, err := queue.DeleteBatch(remainingIDs)
	if err != nil {
		t.Fatalf("DeleteBatch() error = %v", err)
	}
	if deleted != len(remainingIDs) {
		t.Fatalf("DeleteBatch() = %d, want %d", deleted, len(remainingIDs))
	}

	count, err := queue.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Count() = %d, want 0", count)
	}
}

func TestDualWritePayloadHelpersCoverBlacklistAndErrors(t *testing.T) {
	t.Parallel()

	encoded, err := dualWriteEncodePayload(dualWriteEventSyncBlacklistScope, dualWriteBlacklistPayload{ChatID: -100123})
	if err != nil {
		t.Fatalf("dualWriteEncodePayload() error = %v", err)
	}
	if got, want := string(encoded), "-100123"; got != want {
		t.Fatalf("dualWriteEncodePayload() = %q, want %q", got, want)
	}

	decoded, err := dualWriteDecodeBlacklistPayload([]byte(`{"chat_id":-100123}`))
	if err != nil {
		t.Fatalf("dualWriteDecodeBlacklistPayload() error = %v", err)
	}
	if decoded.ChatID != -100123 {
		t.Fatalf("dualWriteDecodeBlacklistPayload().ChatID = %d, want %d", decoded.ChatID, int64(-100123))
	}

	dedupeKey, err := dualWriteDedupeKeyFromBytes(dualWriteEventSyncBlacklistScope, []byte(`{"chat_id":-100123}`))
	if err != nil {
		t.Fatalf("dualWriteDedupeKeyFromBytes() error = %v", err)
	}
	if dedupeKey != "-100123" {
		t.Fatalf("dualWriteDedupeKeyFromBytes() = %q, want %q", dedupeKey, "-100123")
	}

	if _, err := dualWriteDedupeKeyForPayload(dualWriteEventSyncBlacklistScope, dualWriteUserPayload{UserID: 42}); err == nil {
		t.Fatal("dualWriteDedupeKeyForPayload() error = nil, want payload type validation")
	}
	if _, err := dualWriteDedupeKeyFromBytes(dualWriteEventKind("unsupported"), []byte("1")); err == nil {
		t.Fatal("dualWriteDedupeKeyFromBytes() error = nil, want unsupported kind error")
	}
}

func TestDualWritePayloadHelpersCoverUserStatePreferenceAndClearKinds(t *testing.T) {
	t.Parallel()

	userState := dualWriteUserStatePayload{ChatID: -100123, UserID: 42}
	if key, err := dualWriteDedupeKeyForPayload(dualWriteEventSyncUserState, userState); err != nil || key != "-100123:42" {
		t.Fatalf("dualWriteDedupeKeyForPayload(user-state) = (%q, %v), want (-100123:42, nil)", key, err)
	}
	if encoded, err := dualWriteEncodePayload(dualWriteEventSyncUserState, userState); err != nil || string(encoded) != "-100123:42" {
		t.Fatalf("dualWriteEncodePayload(user-state) = (%q, %v), want (-100123:42, nil)", encoded, err)
	}
	if key, err := dualWriteDedupeKeyFromBytes(dualWriteEventSyncUserState, []byte(`{"chat_id":-100123,"user_id":42}`)); err != nil || key != "-100123:42" {
		t.Fatalf("dualWriteDedupeKeyFromBytes(user-state json) = (%q, %v), want (-100123:42, nil)", key, err)
	}

	for _, kind := range []dualWriteEventKind{dualWriteEventSyncPreference, dualWriteEventClearUserState} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			payload := dualWriteUserPayload{UserID: 42}
			if key, err := dualWriteDedupeKeyForPayload(kind, payload); err != nil || key != "42" {
				t.Fatalf("dualWriteDedupeKeyForPayload(%s) = (%q, %v), want (42, nil)", kind, key, err)
			}
			if encoded, err := dualWriteEncodePayload(kind, payload); err != nil || string(encoded) != "42" {
				t.Fatalf("dualWriteEncodePayload(%s) = (%q, %v), want (42, nil)", kind, encoded, err)
			}
			if key, err := dualWriteDedupeKeyFromBytes(kind, []byte(`{"user_id":42}`)); err != nil || key != "42" {
				t.Fatalf("dualWriteDedupeKeyFromBytes(%s json) = (%q, %v), want (42, nil)", kind, key, err)
			}
		})
	}

	if userID, ok := dualWriteUserIDFromStateDedupeKey("-100123:42"); !ok || userID != 42 {
		t.Fatalf("dualWriteUserIDFromStateDedupeKey(valid) = (%d, %v), want (42, true)", userID, ok)
	}
	if userID, ok := dualWriteUserIDFromStateDedupeKey("missing-separator"); ok || userID != 0 {
		t.Fatalf("dualWriteUserIDFromStateDedupeKey(invalid) = (%d, %v), want (0, false)", userID, ok)
	}

	if _, err := dualWriteDedupeKeyFromBytes(dualWriteEventSyncUserState, []byte("{")); err == nil || !strings.Contains(err.Error(), "decode user-state payload") {
		t.Fatalf("dualWriteDedupeKeyFromBytes(user-state invalid) error = %v, want decode error", err)
	}
	if _, err := dualWriteDedupeKeyFromBytes(dualWriteEventSyncPreference, []byte("{")); err == nil || !strings.Contains(err.Error(), "decode user payload") {
		t.Fatalf("dualWriteDedupeKeyFromBytes(preference invalid) error = %v, want decode error", err)
	}
}

func TestDualWriteStoreSyncHelpersUpdateAndClearCache(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	ctx := context.Background()
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.cache.setPreference(ctx, userID, "en"); err != nil {
		t.Fatalf("cache.setPreference() error = %v", err)
	}
	if err := st.syncPreference(userID); err != nil {
		t.Fatalf("syncPreference(delete) error = %v", err)
	}
	if language, found, err := st.cache.loadPreference(ctx, userID); err != nil || found || language != "" {
		t.Fatalf("cache.loadPreference() after delete = (%q, %v, %v), want (\"\", false, nil)", language, found, err)
	}

	if err := st.primary.SetUserLanguagePreference(userID, "zh-cn"); err != nil {
		t.Fatalf("primary.SetUserLanguagePreference() error = %v", err)
	}
	if err := st.syncPreference(userID); err != nil {
		t.Fatalf("syncPreference(set) error = %v", err)
	}
	if language, found, err := st.cache.loadPreference(ctx, userID); err != nil || !found || language != "zh-cn" {
		t.Fatalf("cache.loadPreference() after set = (%q, %v, %v), want (%q, true, nil)", language, found, err, "zh-cn")
	}

	if err := st.cache.setPendingRaw(ctx, PendingVerification{
		ChatID:       chatID,
		UserID:       userID,
		UserLanguage: "en",
		Timestamp:    1,
		RandomToken:  "tok",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("cache.setPendingRaw() error = %v", err)
	}
	if err := st.cache.setStatus(ctx, chatID, userID, "verified"); err != nil {
		t.Fatalf("cache.setStatus() error = %v", err)
	}
	if err := st.cache.setWarning(ctx, chatID, userID, 2); err != nil {
		t.Fatalf("cache.setWarning() error = %v", err)
	}
	if err := st.syncUserState(chatID, userID); err != nil {
		t.Fatalf("syncUserState() error = %v", err)
	}
	if pending, found, err := st.cache.loadPending(ctx, chatID, userID); err != nil || found || pending != nil {
		t.Fatalf("cache.loadPending() after sync = (%+v, %v, %v), want (nil, false, nil)", pending, found, err)
	}
	if status, found, err := st.cache.loadStatus(ctx, chatID, userID); err != nil || found || status != "" {
		t.Fatalf("cache.loadStatus() after sync = (%q, %v, %v), want (\"\", false, nil)", status, found, err)
	}
	if warnings, found, err := st.cache.loadWarning(ctx, chatID, userID); err != nil || found || warnings != 0 {
		t.Fatalf("cache.loadWarning() after sync = (%d, %v, %v), want (0, false, nil)", warnings, found, err)
	}

	if err := st.primary.AddBlacklistWord(chatID, "spam", "admin"); err != nil {
		t.Fatalf("primary.AddBlacklistWord() error = %v", err)
	}
	if err := st.syncBlacklistScope(chatID); err != nil {
		t.Fatalf("syncBlacklistScope(set) error = %v", err)
	}
	if words, found, err := st.cache.loadBlacklist(ctx, chatID); err != nil || !found || len(words) != 1 || words[0] != "spam" {
		t.Fatalf("cache.loadBlacklist() after set = (%v, %v, %v), want ([spam], true, nil)", words, found, err)
	}

	if err := st.primary.RemoveBlacklistWord(chatID, "spam"); err != nil {
		t.Fatalf("primary.RemoveBlacklistWord() error = %v", err)
	}
	if err := st.syncBlacklistScope(chatID); err != nil {
		t.Fatalf("syncBlacklistScope(delete) error = %v", err)
	}
	if words, found, err := st.cache.loadBlacklist(ctx, chatID); err != nil || found || words != nil {
		t.Fatalf("cache.loadBlacklist() after delete = (%v, %v, %v), want (nil, false, nil)", words, found, err)
	}
}

func TestDualWriteStoreRestoreBufferedEventsSkipsConflicts(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	userID := int64(42)
	chatID := int64(-100123)

	clearKey := dualWriteBufferedMapKey(dualWriteEventClearUserState, dualWriteUserDedupeKey(userID))
	st.bufferMu.Lock()
	st.buffer[clearKey] = dualWriteQueueItem{
		Kind:      dualWriteEventClearUserState,
		DedupeKey: dualWriteUserDedupeKey(userID),
		Payload:   []byte(dualWriteUserDedupeKey(userID)),
	}
	st.bufferMu.Unlock()

	st.restoreBufferedEvents([]dualWriteQueueItem{{
		Kind:      dualWriteEventSyncUserState,
		DedupeKey: dualWriteUserStateDedupeKey(chatID, userID),
		Payload:   []byte(dualWriteUserStateDedupeKey(chatID, userID)),
	}})

	st.bufferMu.Lock()
	if len(st.buffer) != 1 {
		t.Fatalf("len(buffer) after restoring sync = %d, want 1", len(st.buffer))
	}
	st.buffer = map[string]dualWriteQueueItem{
		dualWriteBufferedMapKey(dualWriteEventSyncUserState, dualWriteUserStateDedupeKey(chatID, userID)): {
			Kind:      dualWriteEventSyncUserState,
			DedupeKey: dualWriteUserStateDedupeKey(chatID, userID),
			Payload:   []byte(dualWriteUserStateDedupeKey(chatID, userID)),
		},
	}
	st.bufferMu.Unlock()

	st.restoreBufferedEvents([]dualWriteQueueItem{{
		Kind:      dualWriteEventClearUserState,
		DedupeKey: dualWriteUserDedupeKey(userID),
		Payload:   []byte(dualWriteUserDedupeKey(userID)),
	}})

	st.bufferMu.Lock()
	defer st.bufferMu.Unlock()
	if len(st.buffer) != 1 {
		t.Fatalf("len(buffer) after restoring clear = %d, want 1", len(st.buffer))
	}
	if _, exists := st.buffer[clearKey]; exists {
		t.Fatal("restoreBufferedEvents() restored clear-user-state over buffered sync-user-state")
	}
}

func TestDualWriteStoreApplyEventCoversBlacklistAndClearBranches(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	ctx := context.Background()
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.primary.AddBlacklistWord(chatID, "spam", "admin"); err != nil {
		t.Fatalf("primary.AddBlacklistWord() error = %v", err)
	}
	if err := st.applyEvent(dualWriteEvent{
		Kind:    dualWriteEventSyncBlacklistScope,
		Payload: []byte(`{"chat_id":-100123}`),
	}); err != nil {
		t.Fatalf("applyEvent(blacklist) error = %v", err)
	}
	if words, found, err := st.cache.loadBlacklist(ctx, chatID); err != nil || !found || len(words) != 1 || words[0] != "spam" {
		t.Fatalf("cache.loadBlacklist() = (%v, %v, %v), want ([spam], true, nil)", words, found, err)
	}

	if err := st.cache.setStatus(ctx, chatID, userID, "verified"); err != nil {
		t.Fatalf("cache.setStatus() error = %v", err)
	}
	if err := st.cache.setWarning(ctx, chatID, userID, 1); err != nil {
		t.Fatalf("cache.setWarning() error = %v", err)
	}
	if err := st.cache.setPendingRaw(ctx, PendingVerification{
		ChatID:       chatID,
		UserID:       userID,
		UserLanguage: "en",
		Timestamp:    2,
		RandomToken:  "tok-2",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("cache.setPendingRaw() error = %v", err)
	}
	if err := st.applyEvent(dualWriteEvent{
		Kind:    dualWriteEventClearUserState,
		Payload: []byte(`{"user_id":42}`),
	}); err != nil {
		t.Fatalf("applyEvent(clear-user-state) error = %v", err)
	}
	if status, found, err := st.cache.loadStatus(ctx, chatID, userID); err != nil || found || status != "" {
		t.Fatalf("cache.loadStatus() after clear = (%q, %v, %v), want (\"\", false, nil)", status, found, err)
	}
}

func TestDualWriteStoreSyncUserStateCopiesPendingStatusesAndWarnings(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	ctx := context.Background()

	t.Run("verified", func(t *testing.T) {
		chatID := int64(-100123)
		userID := int64(42)
		pending := PendingVerification{
			ChatID:       chatID,
			UserID:       userID,
			UserLanguage: "en",
			Timestamp:    time.Now().UTC().Unix(),
			RandomToken:  "token-verified",
			ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		}
		if err := st.primary.SetPending(pending); err != nil {
			t.Fatalf("primary.SetPending() error = %v", err)
		}
		if err := st.primary.SetVerified(chatID, userID); err != nil {
			t.Fatalf("primary.SetVerified() error = %v", err)
		}
		if _, err := st.primary.IncrWarningCount(chatID, userID); err != nil {
			t.Fatalf("primary.IncrWarningCount() error = %v", err)
		}

		if err := st.syncUserState(chatID, userID); err != nil {
			t.Fatalf("syncUserState(verified) error = %v", err)
		}

		if gotPending, found, err := st.cache.loadPending(ctx, chatID, userID); err != nil || !found || gotPending == nil || gotPending.RandomToken != pending.RandomToken {
			t.Fatalf("cache.loadPending() = (%+v, %v, %v), want pending mirrored", gotPending, found, err)
		}
		if status, found, err := st.cache.loadStatus(ctx, chatID, userID); err != nil || !found || status != "verified" {
			t.Fatalf("cache.loadStatus() = (%q, %v, %v), want (verified, true, nil)", status, found, err)
		}
		if warnings, found, err := st.cache.loadWarning(ctx, chatID, userID); err != nil || !found || warnings != 1 {
			t.Fatalf("cache.loadWarning() = (%d, %v, %v), want (1, true, nil)", warnings, found, err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		chatID := int64(-100123)
		userID := int64(43)
		if err := st.primary.SetRejected(chatID, userID); err != nil {
			t.Fatalf("primary.SetRejected() error = %v", err)
		}

		if err := st.syncUserState(chatID, userID); err != nil {
			t.Fatalf("syncUserState(rejected) error = %v", err)
		}

		if status, found, err := st.cache.loadStatus(ctx, chatID, userID); err != nil || !found || status != "rejected" {
			t.Fatalf("cache.loadStatus() = (%q, %v, %v), want (rejected, true, nil)", status, found, err)
		}
	})
}

func TestDualWriteStoreApplyEventAndBatchDecodeFailures(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)

	if err := st.applyEvent(dualWriteEvent{Kind: dualWriteEventSyncUserState, Payload: []byte("{")}); err == nil || !strings.Contains(err.Error(), "decode user-state event") {
		t.Fatalf("applyEvent(user-state invalid) error = %v, want decode user-state error", err)
	}
	if err := st.applyEvent(dualWriteEvent{Kind: dualWriteEventSyncPreference, Payload: []byte("{")}); err == nil || !strings.Contains(err.Error(), "decode preference event") {
		t.Fatalf("applyEvent(preference invalid) error = %v, want decode preference error", err)
	}
	if err := st.applyEvent(dualWriteEvent{Kind: dualWriteEventKind("unknown"), Payload: []byte("42")}); err == nil || !strings.Contains(err.Error(), "unsupported dual-write event kind") {
		t.Fatalf("applyEvent(unknown) error = %v, want unsupported kind error", err)
	}
	if err := st.applyUserStateBatch([]dualWriteEvent{{Kind: dualWriteEventSyncUserState, Payload: []byte("{")}}); err == nil || !strings.Contains(err.Error(), "decode user-state event") {
		t.Fatalf("applyUserStateBatch() error = %v, want decode user-state error", err)
	}
	if err := st.applyPreferenceBatch([]dualWriteEvent{{Kind: dualWriteEventSyncPreference, Payload: []byte("{")}}); err == nil || !strings.Contains(err.Error(), "decode preference event") {
		t.Fatalf("applyPreferenceBatch() error = %v, want decode preference error", err)
	}
}

func TestDualWriteStoreEnqueuePreferenceAndBlacklistSync(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	userID := int64(42)
	chatID := int64(-100123)

	st.enqueuePreferenceSync(userID)
	st.enqueueBlacklistScopeSync(chatID)

	st.bufferMu.Lock()
	defer st.bufferMu.Unlock()

	if _, exists := st.buffer[dualWriteBufferedMapKey(dualWriteEventSyncPreference, dualWriteUserDedupeKey(userID))]; !exists {
		t.Fatal("enqueuePreferenceSync() did not buffer preference sync event")
	}
	if _, exists := st.buffer[dualWriteBufferedMapKey(dualWriteEventSyncBlacklistScope, dualWriteBlacklistScopeDedupeKey(chatID))]; !exists {
		t.Fatal("enqueueBlacklistScopeSync() did not buffer blacklist sync event")
	}
}

func TestNewFromConfigSupportsBackendsAndRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		st, err := NewFromConfig(config.StoreConfig{Type: "sqlite", DataPath: t.TempDir()})
		if err != nil {
			t.Fatalf("NewFromConfig(sqlite) error = %v", err)
		}
		defer st.Close()
	})

	t.Run("redis", func(t *testing.T) {
		t.Parallel()

		redisSrv := miniredis.RunT(t)
		cfg := config.StoreConfig{Type: "redis", DataPath: t.TempDir(), RedisAddr: redisSrv.Addr()}
		cfg.Normalize()

		st, err := NewFromConfig(cfg)
		if err != nil {
			t.Fatalf("NewFromConfig(redis) error = %v", err)
		}
		defer st.Close()
	})

	t.Run("dual-write", func(t *testing.T) {
		t.Parallel()

		redisSrv := miniredis.RunT(t)
		cfg := config.StoreConfig{
			Type:             "sqlite",
			DataPath:         t.TempDir(),
			RedisAddr:        redisSrv.Addr(),
			RedisKeyPrefix:   "factory-dual:",
			DualWriteEnabled: true,
		}
		cfg.Normalize()

		st, err := NewFromConfig(cfg)
		if err != nil {
			t.Fatalf("NewFromConfig(dual-write) error = %v", err)
		}
		defer st.Close()
	})

	t.Run("unsupported", func(t *testing.T) {
		t.Parallel()

		if _, err := NewFromConfig(config.StoreConfig{Type: "unknown", DataPath: t.TempDir()}); err == nil {
			t.Fatal("NewFromConfig() error = nil, want unsupported store type")
		}
	})
}
