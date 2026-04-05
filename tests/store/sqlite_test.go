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

	if err := store.AddBlacklistWord("  SpAmWord  ", "admin"); err != nil {
		t.Fatalf("AddBlacklistWord() error = %v", err)
	}

	words, err := store.GetBlacklistWords()
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
		`INSERT INTO blacklist_words (word, added_by, added_at) VALUES (?, ?, datetime('now'))`,
		"MiXeDCaSe", "admin",
	); err != nil {
		t.Fatalf("seed mixed-case row error = %v", err)
	}

	if err := store.RemoveBlacklistWord("mixedcase"); err != nil {
		t.Fatalf("RemoveBlacklistWord() error = %v", err)
	}

	words, err := store.GetBlacklistWords()
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

	if got.ChatID != want.ChatID || got.UserID != want.UserID || got.Timestamp != want.Timestamp || got.RandomToken != want.RandomToken || got.ReminderMessageID != want.ReminderMessageID || got.MessageThreadID != want.MessageThreadID || got.ReplyToMessageID != want.ReplyToMessageID {
		t.Fatalf("GetPending() = %+v, want %+v", *got, want)
	}

	if !got.ExpireAt.Equal(want.ExpireAt) {
		t.Fatalf("ExpireAt = %v, want %v", got.ExpireAt, want.ExpireAt)
	}
}
