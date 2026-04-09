package store_test

import (
	"path/filepath"
	"testing"
	"time"

	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func TestSQLiteStoreGetUserLanguagePreferenceReturnsErrorAfterClose(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	language, err := store.GetUserLanguagePreference(42)
	if err == nil {
		t.Fatal("GetUserLanguagePreference() error = nil, want closed database error")
	}
	if language != "" {
		t.Fatalf("GetUserLanguagePreference() = %q, want blank language on error", language)
	}
}

func TestSQLiteStoreResolvePendingByTokenIgnoresMismatchedToken(t *testing.T) {
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
		Timestamp:    1712300000,
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	result, err := store.ResolvePendingByToken(
		pending.ChatID,
		pending.UserID,
		pending.Timestamp,
		"token-b",
		storepkg.PendingActionApprove,
		3,
	)
	if err != nil {
		t.Fatalf("ResolvePendingByToken() error = %v", err)
	}
	if result.Matched {
		t.Fatalf("ResolvePendingByToken() = %+v, want unmatched result for token mismatch", result)
	}

	gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending == nil || gotPending.RandomToken != pending.RandomToken {
		t.Fatalf("GetPending() = %+v, want original pending preserved", gotPending)
	}
}

func TestSQLiteStoreResolvePendingByTokenExpireKeepsExistingTerminalState(t *testing.T) {
	t.Parallel()

	t.Run("verified", func(t *testing.T) {
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
			Timestamp:    1712300000,
			RandomToken:  "token-a",
			ExpireAt:     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		}
		if err := store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}
		if err := store.SetVerified(pending.ChatID, pending.UserID); err != nil {
			t.Fatalf("SetVerified() error = %v", err)
		}
		if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
			t.Fatalf("IncrWarningCount() error = %v", err)
		}

		result, err := store.ResolvePendingByToken(
			pending.ChatID,
			pending.UserID,
			pending.Timestamp,
			pending.RandomToken,
			storepkg.PendingActionExpire,
			3,
		)
		if err != nil {
			t.Fatalf("ResolvePendingByToken() error = %v", err)
		}
		if !result.Matched || !result.Verified || result.WarningCount != 0 || result.ShouldBan {
			t.Fatalf("ResolvePendingByToken() = %+v, want matched verified result without extra warning", result)
		}

		gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetPending() error = %v", err)
		}
		if gotPending != nil {
			t.Fatalf("GetPending() = %+v, want nil after resolution", gotPending)
		}

		warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetWarningCount() error = %v", err)
		}
		if warnings != 1 {
			t.Fatalf("GetWarningCount() = %d, want existing warning count preserved", warnings)
		}
	})

	t.Run("rejected", func(t *testing.T) {
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
			Timestamp:    1712300001,
			RandomToken:  "token-b",
			ExpireAt:     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		}
		if err := store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}
		if err := store.SetRejected(pending.ChatID, pending.UserID); err != nil {
			t.Fatalf("SetRejected() error = %v", err)
		}
		if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
			t.Fatalf("IncrWarningCount() error = %v", err)
		}

		result, err := store.ResolvePendingByToken(
			pending.ChatID,
			pending.UserID,
			pending.Timestamp,
			pending.RandomToken,
			storepkg.PendingActionExpire,
			3,
		)
		if err != nil {
			t.Fatalf("ResolvePendingByToken() error = %v", err)
		}
		if !result.Matched || !result.Rejected || result.WarningCount != 0 || result.ShouldBan {
			t.Fatalf("ResolvePendingByToken() = %+v, want matched rejected result without extra warning", result)
		}

		gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetPending() error = %v", err)
		}
		if gotPending != nil {
			t.Fatalf("GetPending() = %+v, want nil after resolution", gotPending)
		}

		warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetWarningCount() error = %v", err)
		}
		if warnings != 1 {
			t.Fatalf("GetWarningCount() = %d, want existing warning count preserved", warnings)
		}
	})
}
