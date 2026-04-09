package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
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

func TestSQLiteStoreGetUserLanguagePreferenceReturnsBlankWhenUnset(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	language, err := store.GetUserLanguagePreference(42)
	if err != nil {
		t.Fatalf("GetUserLanguagePreference() error = %v", err)
	}
	if language != "" {
		t.Fatalf("GetUserLanguagePreference() = %q, want blank for unset user", language)
	}
}

func TestSQLiteStoreSetUserLanguagePreferenceRejectsBlank(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetUserLanguagePreference(42, "   "); err == nil {
		t.Fatal("SetUserLanguagePreference() error = nil, want validation error for blank language")
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

func TestCreatePendingIfAbsentReturnsExistingActivePending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	original := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 7001,
	}
	if err := store.SetPending(original); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	created, existing, err := store.CreatePendingIfAbsent(storepkg.PendingVerification{
		ChatID:       original.ChatID,
		UserID:       original.UserID,
		UserLanguage: "zh-cn",
		Timestamp:    original.Timestamp + 1,
		RandomToken:  "token-b",
		ExpireAt:     original.ExpireAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("CreatePendingIfAbsent() error = %v", err)
	}
	if created {
		t.Fatal("CreatePendingIfAbsent() created = true, want false for active pending")
	}
	if existing == nil {
		t.Fatal("CreatePendingIfAbsent() existing = nil, want active pending snapshot")
	}
	if existing.RandomToken != original.RandomToken {
		t.Fatalf("existing.RandomToken = %q, want %q", existing.RandomToken, original.RandomToken)
	}
}

func TestCreatePendingIfAbsentReturnsExistingExpiredPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	existingPending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Add(-10 * time.Minute).Unix(),
		RandomToken:  "expired-token",
		ExpireAt:     time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(existingPending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	created, existing, err := store.CreatePendingIfAbsent(storepkg.PendingVerification{
		ChatID:       existingPending.ChatID,
		UserID:       existingPending.UserID,
		UserLanguage: "zh-cn",
		Timestamp:    existingPending.Timestamp + 1,
		RandomToken:  "new-token",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	})
	if err != nil {
		t.Fatalf("CreatePendingIfAbsent() error = %v", err)
	}
	if created {
		t.Fatal("CreatePendingIfAbsent() created = true, want false for existing expired pending snapshot")
	}
	if existing == nil || existing.RandomToken != existingPending.RandomToken {
		t.Fatalf("CreatePendingIfAbsent() existing = %+v, want expired pending snapshot", existing)
	}
}

func TestCreatePendingIfAbsentSkipsVerifiedAndRejectedUsers(t *testing.T) {
	t.Parallel()

	t.Run("verified", func(t *testing.T) {
		t.Parallel()

		store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore() error = %v", err)
		}
		defer store.Close()

		if err := store.SetVerified(-100123, 42); err != nil {
			t.Fatalf("SetVerified() error = %v", err)
		}

		created, existing, err := store.CreatePendingIfAbsent(storepkg.PendingVerification{
			ChatID:      -100123,
			UserID:      42,
			Timestamp:   time.Now().UTC().Unix(),
			RandomToken: "token-a",
			ExpireAt:    time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		})
		if err != nil {
			t.Fatalf("CreatePendingIfAbsent() error = %v", err)
		}
		if created || existing != nil {
			t.Fatalf("CreatePendingIfAbsent() = (%v, %+v), want (false, nil) for verified user", created, existing)
		}
		if pending, err := store.GetPending(-100123, 42); err != nil || pending != nil {
			t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", pending, err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()

		store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore() error = %v", err)
		}
		defer store.Close()

		if err := store.SetRejected(-100123, 42); err != nil {
			t.Fatalf("SetRejected() error = %v", err)
		}

		created, existing, err := store.CreatePendingIfAbsent(storepkg.PendingVerification{
			ChatID:      -100123,
			UserID:      42,
			Timestamp:   time.Now().UTC().Unix(),
			RandomToken: "token-a",
			ExpireAt:    time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		})
		if err != nil {
			t.Fatalf("CreatePendingIfAbsent() error = %v", err)
		}
		if created || existing != nil {
			t.Fatalf("CreatePendingIfAbsent() = (%v, %+v), want (false, nil) for rejected user", created, existing)
		}
		if pending, err := store.GetPending(-100123, 42); err != nil || pending != nil {
			t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", pending, err)
		}
	})
}

func TestCreatePendingIfAbsentDefaultsBlankLanguage(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	created, existing, err := store.CreatePendingIfAbsent(storepkg.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   time.Now().UTC().Unix(),
		RandomToken: "token-a",
		ExpireAt:    time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	})
	if err != nil {
		t.Fatalf("CreatePendingIfAbsent() error = %v", err)
	}
	if !created || existing != nil {
		t.Fatalf("CreatePendingIfAbsent() = (%v, %+v), want (true, nil)", created, existing)
	}

	gotPending, err := store.GetPending(-100123, 42)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending == nil || gotPending.UserLanguage != "zh-cn" {
		t.Fatalf("GetPending() = %+v, want default language zh-cn", gotPending)
	}
}

func TestResolvePendingByTokenApproveSetsVerifiedAndClearsPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		ReminderMessageID: 7001,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	result, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionApprove, 3)
	if err != nil {
		t.Fatalf("ResolvePendingByToken() error = %v", err)
	}
	if !result.Matched || !result.Verified {
		t.Fatalf("ResolvePendingByToken() = %+v, want matched verified result", result)
	}

	gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending != nil {
		t.Fatalf("GetPending() = %+v, want nil after approve", *gotPending)
	}

	verified, err := store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after approve")
	}

	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 0 {
		t.Fatalf("GetWarningCount() = %d, want 0 after approve", warnings)
	}
}

func TestResolvePendingByTokenExpireIncrementsWarningOnce(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	first, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionExpire, 3)
	if err != nil {
		t.Fatalf("ResolvePendingByToken() first error = %v", err)
	}
	if !first.Matched || first.WarningCount != 1 {
		t.Fatalf("first ResolvePendingByToken() = %+v, want warning count 1", first)
	}

	second, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionExpire, 3)
	if err != nil {
		t.Fatalf("ResolvePendingByToken() second error = %v", err)
	}
	if second.Matched {
		t.Fatalf("second ResolvePendingByToken() = %+v, want unmatched no-op", second)
	}

	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 1 {
		t.Fatalf("GetWarningCount() = %d, want 1 after repeated expiry resolution", warnings)
	}
}

func TestResolvePendingByTokenConcurrentOnlyOneMutationWins(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	type resolutionAttempt struct {
		result storepkg.PendingResolutionResult
		err    error
	}
	attempts := make(chan resolutionAttempt, 2)

	go func() {
		result, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionExpire, 3)
		attempts <- resolutionAttempt{result: result, err: err}
	}()
	go func() {
		result, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionApprove, 3)
		attempts <- resolutionAttempt{result: result, err: err}
	}()

	matchedCount := 0
	var matchedResult storepkg.PendingResolutionResult
	for i := 0; i < 2; i++ {
		attempt := <-attempts
		if attempt.err != nil {
			t.Fatalf("ResolvePendingByToken() concurrent error = %v", attempt.err)
		}
		if attempt.result.Matched {
			matchedCount++
			matchedResult = attempt.result
		}
	}

	if matchedCount != 1 {
		t.Fatalf("matched resolution count = %d, want 1", matchedCount)
	}
	if pendingAfter, err := store.GetPending(pending.ChatID, pending.UserID); err != nil || pendingAfter != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil) after winning mutation", pendingAfter, err)
	}
	verified, err := store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if matchedResult.Action == storepkg.PendingActionApprove {
		if !verified || warnings != 0 {
			t.Fatalf("final state after approve win = verified:%v warnings:%d, want verified with zero warnings", verified, warnings)
		}
	}
	if matchedResult.Action == storepkg.PendingActionExpire {
		if verified || warnings != 1 {
			t.Fatalf("final state after expire win = verified:%v warnings:%d, want unverified with one warning", verified, warnings)
		}
	}
}

func TestCreatePendingIfAbsentConcurrentOnlyOneCreateWins(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	base := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}

	type createResult struct {
		created bool
		token   string
		err     error
	}

	results := make(chan createResult, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			pending := base
			pending.Timestamp = time.Now().UTC().Unix() + int64(i)
			pending.RandomToken = fmt.Sprintf("token-%d", i)
			created, _, err := store.CreatePendingIfAbsent(pending)
			results <- createResult{created: created, token: pending.RandomToken, err: err}
		}()
	}

	createdCount := 0
	winningTokens := make(map[string]struct{})
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("CreatePendingIfAbsent() error = %v", result.err)
		}
		if result.created {
			createdCount++
			winningTokens[result.token] = struct{}{}
		}
	}

	if createdCount != 1 {
		t.Fatalf("created pending count = %d, want 1", createdCount)
	}
	gotPending, err := store.GetPending(base.ChatID, base.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending == nil {
		t.Fatal("GetPending() = nil, want persisted pending record")
	}
	if _, ok := winningTokens[gotPending.RandomToken]; !ok {
		t.Fatalf("GetPending().RandomToken = %q, want winning token from successful creator", gotPending.RandomToken)
	}
}

func TestSQLiteStoreHasActivePendingReflectsExpiry(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	active, err := store.HasActivePending(-100123, 42)
	if err != nil {
		t.Fatalf("HasActivePending() error = %v", err)
	}
	if active {
		t.Fatal("HasActivePending() = true, want false for empty store")
	}

	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "active-token",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("SetPending(active) error = %v", err)
	}

	active, err = store.HasActivePending(-100123, 42)
	if err != nil {
		t.Fatalf("HasActivePending(active) error = %v", err)
	}
	if !active {
		t.Fatal("HasActivePending() = false, want true for active pending")
	}

	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "expired-token",
		ExpireAt:     time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("SetPending(expired) error = %v", err)
	}

	active, err = store.HasActivePending(-100123, 42)
	if err != nil {
		t.Fatalf("HasActivePending(expired) error = %v", err)
	}
	if active {
		t.Fatal("HasActivePending() = true, want false for expired pending")
	}
}

func TestSQLiteStoreListPendingVerificationsSorted(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	base := time.Now().UTC().Truncate(time.Second)
	records := []storepkg.PendingVerification{
		{ChatID: -100200, UserID: 7, UserLanguage: "en", Timestamp: 3, RandomToken: "c", ExpireAt: base.Add(2 * time.Minute)},
		{ChatID: -100100, UserID: 9, UserLanguage: "en", Timestamp: 2, RandomToken: "b", ExpireAt: base.Add(1 * time.Minute)},
		{ChatID: -100100, UserID: 8, UserLanguage: "en", Timestamp: 1, RandomToken: "a", ExpireAt: base.Add(1 * time.Minute)},
	}

	for _, pending := range records {
		if err := store.SetPending(pending); err != nil {
			t.Fatalf("SetPending(%+v) error = %v", pending, err)
		}
	}

	got, err := store.ListPendingVerifications()
	if err != nil {
		t.Fatalf("ListPendingVerifications() error = %v", err)
	}

	want := []storepkg.PendingVerification{records[2], records[1], records[0]}
	if len(got) != len(want) {
		t.Fatalf("len(ListPendingVerifications()) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ChatID != want[i].ChatID || got[i].UserID != want[i].UserID || got[i].RandomToken != want[i].RandomToken {
			t.Fatalf("ListPendingVerifications()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSQLiteStoreUpdatePendingMetadataByToken(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "zh-cn",
		Timestamp:         1712300000,
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		ReminderMessageID: 7001,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	pending.UserLanguage = "en"
	pending.ExpireAt = pending.ExpireAt.Add(2 * time.Minute)
	pending.ReminderMessageID = 8001
	pending.PrivateMessageID = 9001
	pending.OriginalMessageID = 9101
	pending.MessageThreadID = 77
	pending.ReplyToMessageID = 88

	updated, err := store.UpdatePendingMetadataByToken(pending)
	if err != nil {
		t.Fatalf("UpdatePendingMetadataByToken() error = %v", err)
	}
	if !updated {
		t.Fatal("UpdatePendingMetadataByToken() = false, want true")
	}

	got, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetPending() = nil, want updated row")
	}
	if got.UserLanguage != "en" || got.ReminderMessageID != 8001 || got.PrivateMessageID != 9001 || got.OriginalMessageID != 9101 || got.MessageThreadID != 77 || got.ReplyToMessageID != 88 {
		t.Fatalf("updated pending = %+v, want metadata persisted", *got)
	}
}

func TestSQLiteStoreUpdatePendingMetadataByTokenReturnsFalseForMismatch(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    1,
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	updated, err := store.UpdatePendingMetadataByToken(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "zh-cn",
		Timestamp:    1,
		RandomToken:  "token-b",
		ExpireAt:     time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second),
	})
	if err != nil {
		t.Fatalf("UpdatePendingMetadataByToken() error = %v", err)
	}
	if updated {
		t.Fatal("UpdatePendingMetadataByToken() = true, want false for token mismatch")
	}
}

func TestSQLiteStoreStatusRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	verified, err := store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after SetVerified")
	}

	if err := store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	verified, err = store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() after SetRejected error = %v", err)
	}
	if verified {
		t.Fatal("IsVerified() = true, want false after SetRejected overwrites status")
	}
	rejected, err := store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if !rejected {
		t.Fatal("IsRejected() = false, want true after SetRejected")
	}

	if err := store.RemoveRejected(-100123, 42); err != nil {
		t.Fatalf("RemoveRejected() error = %v", err)
	}
	rejected, err = store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() after RemoveRejected error = %v", err)
	}
	if rejected {
		t.Fatal("IsRejected() = true, want false after RemoveRejected")
	}
}

func TestSQLiteStoreRemoveVerifiedClearPendingAndResetWarnings(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if err := store.RemoveVerified(-100123, 42); err != nil {
		t.Fatalf("RemoveVerified() error = %v", err)
	}
	verified, err := store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if verified {
		t.Fatal("IsVerified() = true, want false after RemoveVerified")
	}

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if err := store.ClearPending(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("ClearPending() error = %v", err)
	}
	if gotPending, err := store.GetPending(pending.ChatID, pending.UserID); err != nil || gotPending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil) after ClearPending", gotPending, err)
	}

	if _, err := store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}
	if err := store.ResetWarningCount(-100123, 42); err != nil {
		t.Fatalf("ResetWarningCount() error = %v", err)
	}
	if warnings, err := store.GetWarningCount(-100123, 42); err != nil || warnings != 0 {
		t.Fatalf("GetWarningCount() = (%d, %v), want (0, nil) after ResetWarningCount", warnings, err)
	}
}

func TestSQLiteStoreBlacklistWordValidation(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.AddBlacklistWord(0, "   ", "admin"); err == nil {
		t.Fatal("AddBlacklistWord() error = nil, want validation error for blank word")
	}
	if err := store.RemoveBlacklistWord(0, "   "); err == nil {
		t.Fatal("RemoveBlacklistWord() error = nil, want validation error for blank word")
	}
}

func TestSQLiteStoreResolvePendingByTokenCancelClearsPendingWithoutStatusChange(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	result, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionCancel, 3)
	if err != nil {
		t.Fatalf("ResolvePendingByToken(cancel) error = %v", err)
	}
	if !result.Matched {
		t.Fatalf("ResolvePendingByToken(cancel) = %+v, want matched result", result)
	}

	gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending != nil {
		t.Fatalf("GetPending() = %+v, want nil after cancel", *gotPending)
	}
	verified, err := store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	rejected, err := store.IsRejected(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if verified || rejected {
		t.Fatalf("status after cancel = verified:%v rejected:%v, want both false", verified, rejected)
	}
}

func TestSQLiteStoreResolvePendingByTokenRejectSetsRejectedAndClearsWarnings(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	result, err := store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionReject, 3)
	if err != nil {
		t.Fatalf("ResolvePendingByToken(reject) error = %v", err)
	}
	if !result.Matched || !result.Rejected {
		t.Fatalf("ResolvePendingByToken(reject) = %+v, want matched rejected result", result)
	}

	rejected, err := store.IsRejected(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if !rejected {
		t.Fatal("IsRejected() = false, want true after reject")
	}
	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 0 {
		t.Fatalf("GetWarningCount() = %d, want 0 after reject", warnings)
	}
}

func TestSQLiteStoreResolvePendingByTokenRejectsUnsupportedAction(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	_, err = store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingAction("unknown"), 3)
	if err == nil {
		t.Fatal("ResolvePendingByToken() error = nil, want unsupported action error")
	}
}

func TestSQLiteStoreGetPendingDefaultsBlankLanguageFromLegacyRow(t *testing.T) {
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

	expireAt := time.Now().UTC().Add(5 * time.Minute)
	if _, err := rawDB.Exec(
		`INSERT INTO pending_verifications (
			chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		-100123, 42, "", 1712300000, "legacy", expireAt.Format(time.RFC3339Nano), 1, 2, 3, 4, 5,
	); err != nil {
		t.Fatalf("insert legacy pending row error = %v", err)
	}

	got, err := store.GetPending(-100123, 42)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetPending() = nil, want legacy row")
	}
	if got.UserLanguage != "zh-cn" {
		t.Fatalf("GetPending().UserLanguage = %q, want %q", got.UserLanguage, "zh-cn")
	}
	if !got.ExpireAt.Equal(expireAt) {
		t.Fatalf("GetPending().ExpireAt = %v, want %v", got.ExpireAt, expireAt)
	}
}

func TestSQLiteStoreGetAllBlacklistWordsAggregatesScopes(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.AddBlacklistWord(0, "global", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord(global) error = %v", err)
	}
	if err := store.AddBlacklistWord(-100123, "group-a", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord(group-a) error = %v", err)
	}
	if err := store.AddBlacklistWord(-100123, "group-b", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord(group-b) error = %v", err)
	}

	got, err := store.GetAllBlacklistWords()
	if err != nil {
		t.Fatalf("GetAllBlacklistWords() error = %v", err)
	}

	if !reflect.DeepEqual(got[0], []string{"global"}) {
		t.Fatalf("GetAllBlacklistWords()[0] = %v, want %v", got[0], []string{"global"})
	}
	if !reflect.DeepEqual(got[-100123], []string{"group-a", "group-b"}) && !reflect.DeepEqual(got[-100123], []string{"group-b", "group-a"}) {
		t.Fatalf("GetAllBlacklistWords() group scope = %v, want both group words", got[-100123])
	}
}
