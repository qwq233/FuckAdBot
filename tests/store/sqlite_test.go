package store_test

import (
	"database/sql"
	"path/filepath"
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
		Timestamp:         1712300000,
		RandomToken:       "abc123x",
		ExpireAt:          expireAt,
		ReminderMessageID: 7001,
		PrivateMessageID:  8001,
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

	if got.ChatID != want.ChatID || got.UserID != want.UserID || got.Timestamp != want.Timestamp || got.RandomToken != want.RandomToken || got.ReminderMessageID != want.ReminderMessageID || got.PrivateMessageID != want.PrivateMessageID || got.MessageThreadID != want.MessageThreadID || got.ReplyToMessageID != want.ReplyToMessageID {
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
