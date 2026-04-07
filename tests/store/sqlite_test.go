package store_test

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	storepkg "github.com/qwq233/fuckadbot/internal/store"
	_ "modernc.org/sqlite"
)

func TestSQLiteStoreBlacklistWordsNormalized(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.AddBlacklistWord(0, "  SpAmWord  ", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord() error = %v", err)
	}

	words, err := store.GetBlacklistWords(0)
	if err != nil {
		t.Fatalf("GetBlacklistWords() error = %v", err)
	}

	if len(words) != 1 {
		t.Fatalf("len(words) = %d, want 1", len(words))
	}

	if words[0] != "spamword" {
		t.Fatalf("words[0] = %q, want %q", words[0], "spamword")
	}
}

func TestSQLiteStoreRemoveBlacklistWordCaseInsensitive(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storepkg.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	if _, err := rawDB.Exec(
		`INSERT INTO blacklist_words (chat_id, word, added_by, added_at) VALUES (?, ?, ?, datetime('now'))`,
		0, "MiXeDCaSe", "admin",
	); err != nil {
		t.Fatalf("seed mixed-case row error = %v", err)
	}

	if err := store.RemoveBlacklistWord(0, "mixedcase"); err != nil {
		t.Fatalf("RemoveBlacklistWord() error = %v", err)
	}

	words, err := store.GetBlacklistWords(0)
	if err != nil {
		t.Fatalf("GetBlacklistWords() error = %v", err)
	}

	if len(words) != 0 {
		t.Fatalf("len(words) = %d, want 0", len(words))
	}
}

func TestSQLiteStorePendingVerificationRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	expireAt := time.Now().UTC().Truncate(time.Second)
	want := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         1712300000,
		RandomToken:       "abc123x",
		ExpireAt:          expireAt,
		ReminderMessageID: 7001,
		PrivateMessageID:  8001,
		OriginalMessageID: 8101,
		MessageThreadID:   9001,
		ReplyToMessageID:  5001,
	}

	if err := store.SetPending(want); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	got, err := store.GetPending(want.ChatID, want.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetPending() = nil, want value")
	}

	if got.ChatID != want.ChatID || got.UserID != want.UserID || got.UserLanguage != want.UserLanguage || got.Timestamp != want.Timestamp || got.RandomToken != want.RandomToken || got.ReminderMessageID != want.ReminderMessageID || got.PrivateMessageID != want.PrivateMessageID || got.OriginalMessageID != want.OriginalMessageID || got.MessageThreadID != want.MessageThreadID || got.ReplyToMessageID != want.ReplyToMessageID {
		t.Fatalf("GetPending() = %+v, want %+v", *got, want)
	}

	if !got.ExpireAt.Equal(want.ExpireAt) {
		t.Fatalf("ExpireAt = %v, want %v", got.ExpireAt, want.ExpireAt)
	}
}

func TestSQLiteStoreGroupScopedBlacklist(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	// Add global word
	if err := store.AddBlacklistWord(0, "globalword", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord(global) error = %v", err)
	}
	// Add group word
	if err := store.AddBlacklistWord(-100123, "groupword", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord(group) error = %v", err)
	}

	globalWords, err := store.GetBlacklistWords(0)
	if err != nil {
		t.Fatalf("GetBlacklistWords(0) error = %v", err)
	}
	if len(globalWords) != 1 || globalWords[0] != "globalword" {
		t.Fatalf("GetBlacklistWords(0) = %v, want [globalword]", globalWords)
	}

	groupWords, err := store.GetBlacklistWords(-100123)
	if err != nil {
		t.Fatalf("GetBlacklistWords(-100123) error = %v", err)
	}
	if len(groupWords) != 1 || groupWords[0] != "groupword" {
		t.Fatalf("GetBlacklistWords(-100123) = %v, want [groupword]", groupWords)
	}

	all, err := store.GetAllBlacklistWords()
	if err != nil {
		t.Fatalf("GetAllBlacklistWords() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("GetAllBlacklistWords() returned %d scopes, want 2", len(all))
	}
}

func TestSQLiteStoreSetsSchemaVersionToCurrentForFreshDB(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := storepkg.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	var version int
	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version error = %v", err)
	}

	if version != 3 {
		t.Fatalf("user_version = %d, want 3", version)
	}
}

func TestSQLiteStoreMigratesVersionZeroDatabaseToCurrent(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	seedQueries := []string{
		`CREATE TABLE user_status (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('verified','rejected')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE pending_verifications (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at DATETIME NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			private_message_id INTEGER NOT NULL DEFAULT 0,
			message_thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE warnings (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE blacklist_words (
			chat_id INTEGER NOT NULL DEFAULT 0,
			word TEXT NOT NULL,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, word)
		)`,
	}

	for _, query := range seedQueries {
		if _, err := rawDB.Exec(query); err != nil {
			t.Fatalf("seed schema error = %v", err)
		}
	}

	if err := rawDB.Close(); err != nil {
		t.Fatalf("rawDB.Close() error = %v", err)
	}

	store, err := storepkg.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	rawDB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error after migration = %v", err)
	}
	defer rawDB.Close()

	var version int
	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated user_version error = %v", err)
	}
	if version != 3 {
		t.Fatalf("migrated user_version = %d, want 3", version)
	}

	rows, err := rawDB.Query(`PRAGMA table_info(pending_verifications)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(pending_verifications) error = %v", err)
	}
	defer rows.Close()

	foundOriginalMessageID := false
	foundUserLanguage := false
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
			t.Fatalf("scan table_info row error = %v", err)
		}
		if name == "original_message_id" {
			foundOriginalMessageID = true
		}
		if name == "user_language" {
			foundUserLanguage = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info rows error = %v", err)
	}

	if !foundOriginalMessageID {
		t.Fatal("pending_verifications missing original_message_id column after migration")
	}
	if !foundUserLanguage {
		t.Fatal("pending_verifications missing user_language column after migration")
	}
}

func TestSQLiteStoreMigratesVersionOneDatabaseToVersionTwoWithoutDataLoss(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	seedQueries := []string{
		`CREATE TABLE user_status (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('verified','rejected')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE pending_verifications (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at DATETIME NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			private_message_id INTEGER NOT NULL DEFAULT 0,
			original_message_id INTEGER NOT NULL DEFAULT 0,
			message_thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE warnings (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE blacklist_words (
			chat_id INTEGER NOT NULL DEFAULT 0,
			word TEXT NOT NULL,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, word)
		)`,
		`PRAGMA user_version = 1`,
	}

	for _, query := range seedQueries {
		if _, err := rawDB.Exec(query); err != nil {
			t.Fatalf("seed schema error = %v", err)
		}
	}

	if _, err := rawDB.Exec(
		`INSERT INTO pending_verifications (
			chat_id, user_id, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		-100123, 42, 1712300000, "abc123x", time.Now().UTC().Format(time.DateTime), 7001, 8001, 8101, 9001, 5001,
	); err != nil {
		t.Fatalf("seed pending_verifications row error = %v", err)
	}

	if err := rawDB.Close(); err != nil {
		t.Fatalf("rawDB.Close() error = %v", err)
	}

	store, err := storepkg.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	rawDB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error after migration = %v", err)
	}
	defer rawDB.Close()

	var version int
	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated user_version error = %v", err)
	}
	if version != 3 {
		t.Fatalf("migrated user_version = %d, want 3", version)
	}

	var userLanguage string
	if err := rawDB.QueryRow(
		`SELECT user_language FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		-100123, 42,
	).Scan(&userLanguage); err != nil {
		t.Fatalf("read migrated user_language error = %v", err)
	}
	if userLanguage != "zh-cn" {
		t.Fatalf("migrated user_language = %q, want %q", userLanguage, "zh-cn")
	}

	pending, err := store.GetPending(-100123, 42)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending == nil {
		t.Fatal("GetPending() = nil, want preserved row")
	}
	if pending.Timestamp != 1712300000 || pending.RandomToken != "abc123x" || pending.ReminderMessageID != 7001 || pending.PrivateMessageID != 8001 || pending.OriginalMessageID != 8101 || pending.MessageThreadID != 9001 || pending.ReplyToMessageID != 5001 {
		t.Fatalf("GetPending() = %+v, preserved fields mismatch", *pending)
	}
}

func TestSQLiteStoreMigratesVersionTwoDatabaseToVersionThree(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	seedQueries := []string{
		`CREATE TABLE user_status (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('verified','rejected')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE pending_verifications (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			user_language TEXT NOT NULL DEFAULT 'zh-cn',
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at DATETIME NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			private_message_id INTEGER NOT NULL DEFAULT 0,
			original_message_id INTEGER NOT NULL DEFAULT 0,
			message_thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE warnings (
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE blacklist_words (
			chat_id INTEGER NOT NULL DEFAULT 0,
			word TEXT NOT NULL,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, word)
		)`,
		`PRAGMA user_version = 2`,
	}

	for _, query := range seedQueries {
		if _, err := rawDB.Exec(query); err != nil {
			t.Fatalf("seed schema error = %v", err)
		}
	}

	if err := rawDB.Close(); err != nil {
		t.Fatalf("rawDB.Close() error = %v", err)
	}

	store, err := storepkg.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	rawDB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error after migration = %v", err)
	}
	defer rawDB.Close()

	var version int
	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated user_version error = %v", err)
	}
	if version != 3 {
		t.Fatalf("migrated user_version = %d, want 3", version)
	}

	rows, err := rawDB.Query(`PRAGMA table_info(user_preferences)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(user_preferences) error = %v", err)
	}
	defer rows.Close()

	foundPreferredLanguage := false
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
			t.Fatalf("scan user_preferences table_info row error = %v", err)
		}
		if name == "preferred_language" {
			foundPreferredLanguage = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate user_preferences rows error = %v", err)
	}
	if !foundPreferredLanguage {
		t.Fatal("user_preferences missing preferred_language column after migration")
	}
}

func TestSQLiteStoreUserLanguagePreferenceRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}

	language, err := store.GetUserLanguagePreference(42)
	if err != nil {
		t.Fatalf("GetUserLanguagePreference() error = %v", err)
	}
	if language != "en" {
		t.Fatalf("GetUserLanguagePreference() = %q, want %q", language, "en")
	}

	if err := store.SetUserLanguagePreference(42, "zh-cn"); err != nil {
		t.Fatalf("SetUserLanguagePreference(update) error = %v", err)
	}

	language, err = store.GetUserLanguagePreference(42)
	if err != nil {
		t.Fatalf("GetUserLanguagePreference() after update error = %v", err)
	}
	if language != "zh-cn" {
		t.Fatalf("GetUserLanguagePreference() after update = %q, want %q", language, "zh-cn")
	}
}

func TestSQLiteStoreClearUserVerificationStateEverywhere(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	userID := int64(42)
	chatA := int64(-100100)
	chatB := int64(-100200)

	if err := store.SetVerified(chatA, userID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if err := store.SetRejected(chatB, userID); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := store.IncrWarningCount(chatA, userID); err != nil {
		t.Fatalf("IncrWarningCount(chatA) error = %v", err)
	}
	if _, err := store.IncrWarningCount(chatB, userID); err != nil {
		t.Fatalf("IncrWarningCount(chatB) error = %v", err)
	}
	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:            chatA,
		UserID:            userID,
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		ReminderMessageID: 1001,
	}); err != nil {
		t.Fatalf("SetPending(chatA) error = %v", err)
	}
	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:            chatB,
		UserID:            userID,
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-b",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		ReminderMessageID: 1002,
	}); err != nil {
		t.Fatalf("SetPending(chatB) error = %v", err)
	}

	if err := store.ClearUserVerificationStateEverywhere(userID); err != nil {
		t.Fatalf("ClearUserVerificationStateEverywhere() error = %v", err)
	}

	verified, err := store.IsVerified(chatA, userID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if verified {
		t.Fatal("IsVerified() = true, want false after reset")
	}

	rejected, err := store.IsRejected(chatB, userID)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if rejected {
		t.Fatal("IsRejected() = true, want false after reset")
	}

	pendingA, err := store.GetPending(chatA, userID)
	if err != nil {
		t.Fatalf("GetPending(chatA) error = %v", err)
	}
	if pendingA != nil {
		t.Fatalf("GetPending(chatA) = %+v, want nil after reset", *pendingA)
	}

	pendingB, err := store.GetPending(chatB, userID)
	if err != nil {
		t.Fatalf("GetPending(chatB) error = %v", err)
	}
	if pendingB != nil {
		t.Fatalf("GetPending(chatB) = %+v, want nil after reset", *pendingB)
	}

	warningsA, err := store.GetWarningCount(chatA, userID)
	if err != nil {
		t.Fatalf("GetWarningCount(chatA) error = %v", err)
	}
	if warningsA != 0 {
		t.Fatalf("GetWarningCount(chatA) = %d, want 0 after reset", warningsA)
	}

	warningsB, err := store.GetWarningCount(chatB, userID)
	if err != nil {
		t.Fatalf("GetWarningCount(chatB) error = %v", err)
	}
	if warningsB != 0 {
		t.Fatalf("GetWarningCount(chatB) = %d, want 0 after reset", warningsB)
	}
}

// TestIncrWarningCountReturnsNewCount verifies IncrWarningCount returns the
// post-increment count in a single atomic operation.
func TestIncrWarningCountReturnsNewCount(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	for want := 1; want <= 5; want++ {
		count, err := store.IncrWarningCount(-100123, 42)
		if err != nil {
			t.Fatalf("IncrWarningCount() iteration %d error = %v", want, err)
		}
		if count != want {
			t.Fatalf("IncrWarningCount() = %d, want %d", count, want)
		}
	}
}

// TestIncrWarningCountConcurrent fires many goroutines that each increment the
// same counter. Run with -race to validate there are no data races, and verify
// the final count equals the number of increments performed.
func TestIncrWarningCountConcurrent(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := store.IncrWarningCount(-100123, 42); err != nil {
				// t.Errorf is goroutine-safe.
				t.Errorf("IncrWarningCount() error = %v", err)
			}
		}()
	}
	wg.Wait()

	count, err := store.GetWarningCount(-100123, 42)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if count != goroutines {
		t.Fatalf("GetWarningCount() = %d, want %d (concurrent increment mismatch)", count, goroutines)
	}
}
