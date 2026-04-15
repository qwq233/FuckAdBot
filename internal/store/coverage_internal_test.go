package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/qwq233/fuckadbot/internal/config"
)

func newRedisStoreForTest(t *testing.T) (*RedisStore, config.StoreConfig, *miniredis.Miniredis) {
	t.Helper()

	redisSrv := miniredis.RunT(t)
	cfg := config.StoreConfig{
		Type:           "redis",
		DataPath:       t.TempDir(),
		RedisAddr:      redisSrv.Addr(),
		RedisKeyPrefix: "redis-test:",
	}
	cfg.Normalize()

	st, err := NewRedisStore(cfg)
	if err != nil {
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	return st, cfg, redisSrv
}

func TestRedisStoreStateAndBlacklistWrappers(t *testing.T) {
	t.Parallel()

	st, _, _ := newRedisStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)
	now := time.Now().UTC().Truncate(time.Second)

	if err := st.SetVerified(chatID, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if verified, err := st.IsVerified(chatID, userID); err != nil || !verified {
		t.Fatalf("IsVerified() = (%v, %v), want (true, nil)", verified, err)
	}
	if err := st.RemoveVerified(chatID, userID); err != nil {
		t.Fatalf("RemoveVerified() error = %v", err)
	}
	if verified, err := st.IsVerified(chatID, userID); err != nil || verified {
		t.Fatalf("IsVerified() after remove = (%v, %v), want (false, nil)", verified, err)
	}

	if err := st.SetRejected(chatID, userID); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if rejected, err := st.IsRejected(chatID, userID); err != nil || !rejected {
		t.Fatalf("IsRejected() = (%v, %v), want (true, nil)", rejected, err)
	}
	if err := st.RemoveRejected(chatID, userID); err != nil {
		t.Fatalf("RemoveRejected() error = %v", err)
	}
	if rejected, err := st.IsRejected(chatID, userID); err != nil || rejected {
		t.Fatalf("IsRejected() after remove = (%v, %v), want (false, nil)", rejected, err)
	}

	activePending := PendingVerification{
		ChatID:            chatID,
		UserID:            userID,
		UserLanguage:      "en",
		Timestamp:         now.Unix(),
		RandomToken:       "token-active",
		ExpireAt:          now.Add(5 * time.Minute),
		OriginalMessageID: 1001,
	}
	if err := st.SetPending(activePending); err != nil {
		t.Fatalf("SetPending(active) error = %v", err)
	}
	if active, err := st.HasActivePending(chatID, userID); err != nil || !active {
		t.Fatalf("HasActivePending(active) = (%v, %v), want (true, nil)", active, err)
	}
	if err := st.ClearPending(chatID, userID); err != nil {
		t.Fatalf("ClearPending() error = %v", err)
	}
	if pending, err := st.GetPending(chatID, userID); err != nil || pending != nil {
		t.Fatalf("GetPending() after clear = (%+v, %v), want (nil, nil)", pending, err)
	}

	expiredPending := activePending
	expiredPending.Timestamp = now.Add(-10 * time.Minute).Unix()
	expiredPending.RandomToken = "token-expired"
	expiredPending.ExpireAt = now.Add(-time.Minute)
	if err := st.SetPending(expiredPending); err != nil {
		t.Fatalf("SetPending(expired) error = %v", err)
	}
	if active, err := st.HasActivePending(chatID, userID); err != nil || active {
		t.Fatalf("HasActivePending(expired) = (%v, %v), want (false, nil)", active, err)
	}
	if err := st.ClearPending(chatID, userID); err != nil {
		t.Fatalf("ClearPending(expired) error = %v", err)
	}

	if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 1 {
		t.Fatalf("IncrWarningCount() = (%d, %v), want (1, nil)", count, err)
	}
	if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 2 {
		t.Fatalf("IncrWarningCount(second) = (%d, %v), want (2, nil)", count, err)
	}
	if err := st.ResetWarningCount(chatID, userID); err != nil {
		t.Fatalf("ResetWarningCount() error = %v", err)
	}
	if warnings, err := st.GetWarningCount(chatID, userID); err != nil || warnings != 0 {
		t.Fatalf("GetWarningCount() after reset = (%d, %v), want (0, nil)", warnings, err)
	}

	if err := st.AddBlacklistWord(chatID, "Spam", "tester"); err != nil {
		t.Fatalf("AddBlacklistWord() error = %v", err)
	}
	if words, err := st.GetBlacklistWords(chatID); err != nil || !reflect.DeepEqual(words, []string{"spam"}) {
		t.Fatalf("GetBlacklistWords() = (%v, %v), want ([spam], nil)", words, err)
	}
	if err := st.RemoveBlacklistWord(chatID, " SPAM "); err != nil {
		t.Fatalf("RemoveBlacklistWord() error = %v", err)
	}
	if words, err := st.GetBlacklistWords(chatID); err != nil || len(words) != 0 {
		t.Fatalf("GetBlacklistWords() after remove = (%v, %v), want ([], nil)", words, err)
	}
}

func TestDualWriteStoreStateAndBlacklistWrappers(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	chatID := int64(-100123)
	userID := int64(42)
	now := time.Now().UTC().Truncate(time.Second)

	if err := st.SetVerified(chatID, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if err := st.RemoveVerified(chatID, userID); err != nil {
		t.Fatalf("RemoveVerified() error = %v", err)
	}
	if verified, err := st.primary.IsVerified(chatID, userID); err != nil || verified {
		t.Fatalf("primary.IsVerified() after remove = (%v, %v), want (false, nil)", verified, err)
	}

	if err := st.SetRejected(chatID, userID); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if err := st.RemoveRejected(chatID, userID); err != nil {
		t.Fatalf("RemoveRejected() error = %v", err)
	}
	if rejected, err := st.primary.IsRejected(chatID, userID); err != nil || rejected {
		t.Fatalf("primary.IsRejected() after remove = (%v, %v), want (false, nil)", rejected, err)
	}

	activePending := PendingVerification{
		ChatID:            chatID,
		UserID:            userID,
		UserLanguage:      "en",
		Timestamp:         now.Unix(),
		RandomToken:       "token-active",
		ExpireAt:          now.Add(5 * time.Minute),
		OriginalMessageID: 1001,
	}
	if err := st.SetPending(activePending); err != nil {
		t.Fatalf("SetPending(active) error = %v", err)
	}
	if active, err := st.HasActivePending(chatID, userID); err != nil || !active {
		t.Fatalf("HasActivePending(active) = (%v, %v), want (true, nil)", active, err)
	}
	if err := st.ClearPending(chatID, userID); err != nil {
		t.Fatalf("ClearPending() error = %v", err)
	}
	if pending, err := st.primary.GetPending(chatID, userID); err != nil || pending != nil {
		t.Fatalf("primary.GetPending() after clear = (%+v, %v), want (nil, nil)", pending, err)
	}

	expiredPending := activePending
	expiredPending.Timestamp = now.Add(-10 * time.Minute).Unix()
	expiredPending.RandomToken = "token-expired"
	expiredPending.ExpireAt = now.Add(-time.Minute)
	if err := st.SetPending(expiredPending); err != nil {
		t.Fatalf("SetPending(expired) error = %v", err)
	}
	if active, err := st.HasActivePending(chatID, userID); err != nil || active {
		t.Fatalf("HasActivePending(expired) = (%v, %v), want (false, nil)", active, err)
	}
	if err := st.ClearPending(chatID, userID); err != nil {
		t.Fatalf("ClearPending(expired) error = %v", err)
	}

	if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 1 {
		t.Fatalf("IncrWarningCount() = (%d, %v), want (1, nil)", count, err)
	}
	if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 2 {
		t.Fatalf("IncrWarningCount(second) = (%d, %v), want (2, nil)", count, err)
	}
	if err := st.ResetWarningCount(chatID, userID); err != nil {
		t.Fatalf("ResetWarningCount() error = %v", err)
	}
	if warnings, err := st.primary.GetWarningCount(chatID, userID); err != nil || warnings != 0 {
		t.Fatalf("primary.GetWarningCount() after reset = (%d, %v), want (0, nil)", warnings, err)
	}

	if err := st.AddBlacklistWord(chatID, "Spam", "tester"); err != nil {
		t.Fatalf("AddBlacklistWord() error = %v", err)
	}
	if words, err := st.GetBlacklistWords(chatID); err != nil || !reflect.DeepEqual(words, []string{"spam"}) {
		t.Fatalf("GetBlacklistWords() = (%v, %v), want ([spam], nil)", words, err)
	}
	if err := st.RemoveBlacklistWord(chatID, " SPAM "); err != nil {
		t.Fatalf("RemoveBlacklistWord() error = %v", err)
	}
	if _, err := st.flushQueue(); err != nil {
		t.Fatalf("flushQueue() after RemoveBlacklistWord error = %v", err)
	}
	if words, err := st.GetBlacklistWords(chatID); err != nil || len(words) != 0 {
		t.Fatalf("GetBlacklistWords() after remove = (%v, %v), want ([], nil)", words, err)
	}
}

func TestSQLiteSnapshotHelpersLoadStateAndHandleEmptyInputs(t *testing.T) {
	t.Parallel()

	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer st.Close()

	if snapshots, err := st.loadUserStateSnapshots(nil); err != nil || len(snapshots) != 0 {
		t.Fatalf("loadUserStateSnapshots(nil) = (%v, %v), want (empty, nil)", snapshots, err)
	}
	if preferences, err := st.loadUserLanguagePreferencesBatch(nil); err != nil || len(preferences) != 0 {
		t.Fatalf("loadUserLanguagePreferencesBatch(nil) = (%v, %v), want (empty, nil)", preferences, err)
	}

	pending := PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 1001,
	}
	if err := st.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if err := st.SetVerified(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if _, err := st.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}
	if err := st.SetUserLanguagePreference(pending.UserID, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}

	keys := []userStateKey{
		{ChatID: pending.ChatID, UserID: pending.UserID},
		{ChatID: pending.ChatID, UserID: pending.UserID},
		{ChatID: -999999, UserID: 7},
	}
	snapshots, err := st.loadUserStateSnapshots(keys)
	if err != nil {
		t.Fatalf("loadUserStateSnapshots() error = %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(loadUserStateSnapshots()) = %d, want 2 unique keys", len(snapshots))
	}

	loaded := snapshots[userStateKey{ChatID: pending.ChatID, UserID: pending.UserID}]
	if loaded.Pending == nil || loaded.Pending.RandomToken != pending.RandomToken {
		t.Fatalf("snapshot.Pending = %+v, want token %q", loaded.Pending, pending.RandomToken)
	}
	if loaded.Status != "verified" {
		t.Fatalf("snapshot.Status = %q, want verified", loaded.Status)
	}
	if loaded.WarningCount != 1 {
		t.Fatalf("snapshot.WarningCount = %d, want 1", loaded.WarningCount)
	}

	missing := snapshots[userStateKey{ChatID: -999999, UserID: 7}]
	if missing.Pending != nil || missing.Status != "" || missing.WarningCount != 0 {
		t.Fatalf("missing snapshot = %+v, want zero value", missing)
	}

	preferences, err := st.loadUserLanguagePreferencesBatch([]int64{pending.UserID, pending.UserID, 99})
	if err != nil {
		t.Fatalf("loadUserLanguagePreferencesBatch() error = %v", err)
	}
	if got, want := preferences[pending.UserID], "en"; got != want {
		t.Fatalf("preferences[%d] = %q, want %q", pending.UserID, got, want)
	}
	if len(preferences) != 1 {
		t.Fatalf("len(preferences) = %d, want 1 populated user", len(preferences))
	}

	if clause, args := buildUserStateKeyWhere([]userStateKey{{ChatID: 1, UserID: 2}, {ChatID: 3, UserID: 4}}); clause != "(chat_id = ? AND user_id = ?) OR (chat_id = ? AND user_id = ?)" || !reflect.DeepEqual(args, []any{int64(1), int64(2), int64(3), int64(4)}) {
		t.Fatalf("buildUserStateKeyWhere() = (%q, %v), want expected clause and args", clause, args)
	}
	if clause, args := buildInt64InClause([]int64{1, 2, 3}); clause != "?, ?, ?" || !reflect.DeepEqual(args, []any{int64(1), int64(2), int64(3)}) {
		t.Fatalf("buildInt64InClause() = (%q, %v), want expected placeholders and args", clause, args)
	}
	if got := uniqueUserStateKeys(keys); !reflect.DeepEqual(got, []userStateKey{{ChatID: pending.ChatID, UserID: pending.UserID}, {ChatID: -999999, UserID: 7}}) {
		t.Fatalf("uniqueUserStateKeys() = %v, want deduplicated order-preserving keys", got)
	}
	if got := uniqueUserIDs([]int64{pending.UserID, pending.UserID, 99}); !reflect.DeepEqual(got, []int64{pending.UserID, 99}) {
		t.Fatalf("uniqueUserIDs() = %v, want deduplicated order-preserving IDs", got)
	}
}

func TestDualWriteQueueMigratesLegacySchemaAndClearsData(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy-queue.db")
	rawDB, err := sql.Open("sqlite", sqliteDataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	rawDB.SetMaxOpenConns(1)
	rawDB.SetMaxIdleConns(1)

	if _, err := rawDB.Exec(`CREATE TABLE redis_sync_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL,
		payload BLOB NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create legacy redis_sync_queue error = %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO redis_sync_queue (kind, payload, attempts, last_error, created_at) VALUES (?, ?, ?, ?, ?)`,
		string(dualWriteEventSyncPreference), []byte("42"), 1, "boom", "2026-04-15 00:00:00",
	); err != nil {
		t.Fatalf("seed legacy redis_sync_queue error = %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("rawDB.Close() error = %v", err)
	}

	queue, err := newDualWriteQueue(path)
	if err != nil {
		t.Fatalf("newDualWriteQueue() error = %v", err)
	}
	t.Cleanup(func() {
		_ = queue.Close()
	})

	events, err := queue.PeekBatch(8)
	if err != nil {
		t.Fatalf("PeekBatch() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(PeekBatch()) = %d, want 1", len(events))
	}
	if events[0].DedupeKey == "" {
		t.Fatal("PeekBatch() returned empty dedupe key after legacy migration")
	}

	rows, err := queue.db.Query(`PRAGMA table_info(redis_sync_queue)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error = %v", err)
	}
	defer rows.Close()

	hasDedupeKey := false
	hasUpdatedAt := false
	for rows.Next() {
		var (
			cid       int
			name      string
			dataType  string
			notNull   int
			defaultV  sql.NullString
			primaryPK int
		)
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultV, &primaryPK); err != nil {
			t.Fatalf("scan pragma row error = %v", err)
		}
		switch name {
		case "dedupe_key":
			hasDedupeKey = true
		case "updated_at":
			hasUpdatedAt = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma rows error = %v", err)
	}
	if !hasDedupeKey || !hasUpdatedAt {
		t.Fatalf("legacy migration columns = dedupe:%v updated:%v, want both true", hasDedupeKey, hasUpdatedAt)
	}

	if err := queue.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if count, err := queue.Count(); err != nil || count != 0 {
		t.Fatalf("Count() after Clear() = (%d, %v), want (0, nil)", count, err)
	}
}

func TestDualWriteQueueCloseHandlesNil(t *testing.T) {
	t.Parallel()

	var queue *dualWriteQueue
	if err := queue.Close(); err != nil {
		t.Fatalf("(*dualWriteQueue)(nil).Close() error = %v, want nil", err)
	}

	if err := (&dualWriteQueue{}).Close(); err != nil {
		t.Fatalf("empty dualWriteQueue.Close() error = %v, want nil", err)
	}
}
