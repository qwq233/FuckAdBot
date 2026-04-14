package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/qwq233/fuckadbot/internal/config"
)

func newDualWriteStoreForTest(t *testing.T) (*DualWriteStore, config.StoreConfig, *miniredis.Miniredis) {
	t.Helper()

	redisSrv := miniredis.RunT(t)
	cfg := config.StoreConfig{
		Type:             "sqlite",
		DataPath:         t.TempDir(),
		RedisAddr:        redisSrv.Addr(),
		RedisKeyPrefix:   "dual-write-test:",
		DualWriteEnabled: true,
	}
	cfg.Normalize()

	st, err := NewDualWriteStore(cfg)
	if err != nil {
		t.Fatalf("NewDualWriteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	return st, cfg, redisSrv
}

func TestDualWriteStoreWritesPrimaryAndRedis(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.SetVerified(chatID, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	verified, err := st.primary.IsVerified(chatID, userID)
	if err != nil {
		t.Fatalf("primary.IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("primary.IsVerified() = false, want true")
	}

	if _, err := st.flushQueue(); err != nil {
		t.Fatalf("flushQueue() error = %v", err)
	}

	status, found, err := st.cache.loadStatus(context.Background(), chatID, userID)
	if err != nil {
		t.Fatalf("cache.loadStatus() error = %v", err)
	}
	if !found || status != "verified" {
		t.Fatalf("cache.loadStatus() = (%q, %v), want (verified, true)", status, found)
	}
}

func TestDualWriteStoreEnqueuesWhenRedisSyncFails(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.cache.client.Close(); err != nil {
		t.Fatalf("cache client close error = %v", err)
	}

	if err := st.SetVerified(chatID, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	verified, err := st.primary.IsVerified(chatID, userID)
	if err != nil {
		t.Fatalf("primary.IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("primary.IsVerified() = false, want true")
	}

	if _, err := st.flushQueue(); err == nil {
		t.Fatal("flushQueue() error = nil, want redis sync failure")
	}

	depth, err := st.queue.Count()
	if err != nil {
		t.Fatalf("queue.Count() error = %v", err)
	}
	if depth != 1 {
		t.Fatalf("queue.Count() = %d, want 1", depth)
	}
}

func TestDualWriteStoreFlushQueueReplaysEvents(t *testing.T) {
	t.Parallel()

	st, cfg, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.cache.client.Close(); err != nil {
		t.Fatalf("cache client close error = %v", err)
	}

	if err := st.SetVerified(chatID, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	replacementCache, err := NewRedisStore(cfg)
	if err != nil {
		t.Fatalf("NewRedisStore(replacement) error = %v", err)
	}
	oldCache := st.cache
	st.cache = replacementCache
	t.Cleanup(func() {
		if st.cache != replacementCache {
			_ = replacementCache.Close()
		}
	})
	_ = oldCache.Close()

	if _, err := st.flushQueue(); err != nil {
		t.Fatalf("flushQueue() error = %v", err)
	}

	depth, err := st.queue.Count()
	if err != nil {
		t.Fatalf("queue.Count() error = %v", err)
	}
	if depth != 0 {
		t.Fatalf("queue.Count() = %d, want 0 after replay", depth)
	}

	verified, err := st.cache.IsVerified(chatID, userID)
	if err != nil {
		t.Fatalf("cache.IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("cache.IsVerified() = false, want true after replay")
	}
}

func TestDualWriteStoreFlushFailureDoesNotSignalFatal(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)

	if err := st.cache.client.Close(); err != nil {
		t.Fatalf("cache client close error = %v", err)
	}

	if err := st.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	select {
	case err := <-st.Errors():
		if err != nil {
			t.Fatalf("Errors() returned %v, want no fatal error", err)
		}
	default:
	}

	flushAt := time.Now().UTC()
	processed, err := st.flushQueue()
	st.recordFlush(flushAt, processed, time.Since(flushAt), err)
	if err == nil {
		t.Fatal("flushQueue() error = nil, want redis sync failure")
	}

	stats := st.RuntimeStats()
	if !stats.Degraded {
		t.Fatalf("RuntimeStats().Degraded = false, want true after flush failure: %+v", stats)
	}
}

func TestDualWriteStoreStartupRecoveryRebuildsCacheAndClearsQueue(t *testing.T) {
	t.Parallel()

	redisSrv := miniredis.RunT(t)
	dataPath := t.TempDir()
	cfg := config.StoreConfig{
		Type:             "sqlite",
		DataPath:         dataPath,
		RedisAddr:        redisSrv.Addr(),
		RedisKeyPrefix:   "dual-write-recover:",
		DualWriteEnabled: true,
	}
	cfg.Normalize()

	primary, err := NewSQLiteStore(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := primary.SetVerified(-100123, 42); err != nil {
		t.Fatalf("primary.SetVerified() error = %v", err)
	}
	if err := primary.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("primary.SetUserLanguagePreference() error = %v", err)
	}
	if err := primary.Close(); err != nil {
		t.Fatalf("primary.Close() error = %v", err)
	}

	queue, err := newDualWriteQueue(cfg.DualWriteQueuePath())
	if err != nil {
		t.Fatalf("newDualWriteQueue() error = %v", err)
	}
	if err := queue.Enqueue(dualWriteEventSyncUserState, dualWriteUserStatePayload{ChatID: -100123, UserID: 42}); err != nil {
		t.Fatalf("queue.Enqueue() error = %v", err)
	}
	if err := queue.Close(); err != nil {
		t.Fatalf("queue.Close() error = %v", err)
	}

	st, err := NewDualWriteStore(cfg)
	if err != nil {
		t.Fatalf("NewDualWriteStore() error = %v", err)
	}
	defer st.Close()

	verified, err := st.cache.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("cache.IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("cache.IsVerified() = false, want true after startup recovery")
	}

	language, err := st.cache.GetUserLanguagePreference(42)
	if err != nil {
		t.Fatalf("cache.GetUserLanguagePreference() error = %v", err)
	}
	if language != "en" {
		t.Fatalf("cache.GetUserLanguagePreference() = %q, want %q", language, "en")
	}

	depth, err := st.queue.Count()
	if err != nil {
		t.Fatalf("queue.Count() error = %v", err)
	}
	if depth != 0 {
		t.Fatalf("queue.Count() = %d, want 0 after startup recovery", depth)
	}

	if _, err := os.Stat(cfg.SQLitePath()); err != nil {
		t.Fatalf("os.Stat(SQLitePath) error = %v", err)
	}
	if _, err := os.Stat(cfg.DualWriteQueuePath()); err != nil {
		t.Fatalf("os.Stat(DualWriteQueuePath) error = %v", err)
	}
}

func TestDualWriteStoreReadThroughWarmsRedis(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	if err := st.primary.SetVerified(chatID, userID); err != nil {
		t.Fatalf("primary.SetVerified() error = %v", err)
	}
	if err := st.cache.clearUserEverywhereCache(context.Background(), userID); err != nil {
		t.Fatalf("cache.clearUserEverywhereCache() error = %v", err)
	}

	verified, err := st.IsVerified(chatID, userID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true from primary fallback")
	}

	if _, err := st.flushQueue(); err != nil {
		t.Fatalf("flushQueue() error = %v", err)
	}

	status, found, err := st.cache.loadStatus(context.Background(), chatID, userID)
	if err != nil {
		t.Fatalf("cache.loadStatus() error = %v", err)
	}
	if !found || status != "verified" {
		t.Fatalf("cache.loadStatus() = (%q, %v), want (verified, true)", status, found)
	}
}

func TestDualWriteStoreBufferedClearUserStateOverridesBufferedUserState(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	st.enqueueUserStateSync(chatID, userID)
	st.enqueueClearUserStateSync(userID)

	inserted, err := st.flushBufferedEvents()
	if err != nil {
		t.Fatalf("flushBufferedEvents() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("flushBufferedEvents() inserted = %d, want 1", inserted)
	}

	events, err := st.queue.PeekBatch(8)
	if err != nil {
		t.Fatalf("queue.PeekBatch() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(queue.PeekBatch()) = %d, want 1", len(events))
	}
	if events[0].Kind != dualWriteEventClearUserState {
		t.Fatalf("queue event kind = %s, want %s", events[0].Kind, dualWriteEventClearUserState)
	}
	if events[0].DedupeKey != dualWriteUserDedupeKey(userID) {
		t.Fatalf("queue event dedupe key = %q, want %q", events[0].DedupeKey, dualWriteUserDedupeKey(userID))
	}
}

func TestDualWriteStoreBufferedUserStateDeduplicatesWithinBatch(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)

	st.enqueueUserStateSync(chatID, userID)
	st.enqueueUserStateSync(chatID, userID)
	st.enqueueUserStateSync(chatID, userID)

	inserted, err := st.flushBufferedEvents()
	if err != nil {
		t.Fatalf("flushBufferedEvents() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("flushBufferedEvents() inserted = %d, want 1", inserted)
	}

	depth, err := st.queue.Count()
	if err != nil {
		t.Fatalf("queue.Count() error = %v", err)
	}
	if depth != 1 {
		t.Fatalf("queue.Count() = %d, want 1 after dedupe", depth)
	}
}
