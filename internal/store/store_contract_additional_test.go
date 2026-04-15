package store_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func TestStoreContractAdditionalStateOperations(t *testing.T) {
	t.Parallel()

	for _, factory := range contractStoreFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			st := factory.new(t)
			settle := func() {
				factory.settle(t, st)
			}

			chatID := int64(-100123)
			userID := int64(42)
			now := time.Now().UTC().Truncate(time.Second)

			if err := st.SetVerified(chatID, userID); err != nil {
				t.Fatalf("SetVerified() error = %v", err)
			}
			settle()
			if err := st.RemoveVerified(chatID, userID); err != nil {
				t.Fatalf("RemoveVerified() error = %v", err)
			}
			settle()
			verified, err := st.IsVerified(chatID, userID)
			if err != nil {
				t.Fatalf("IsVerified() after RemoveVerified error = %v", err)
			}
			if verified {
				t.Fatal("IsVerified() = true, want false after RemoveVerified")
			}

			active, err := st.HasActivePending(chatID, userID)
			if err != nil {
				t.Fatalf("HasActivePending() before set error = %v", err)
			}
			if active {
				t.Fatal("HasActivePending() = true, want false before SetPending")
			}

			activePending := storepkg.PendingVerification{
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
			settle()
			active, err = st.HasActivePending(chatID, userID)
			if err != nil {
				t.Fatalf("HasActivePending(active) error = %v", err)
			}
			if !active {
				t.Fatal("HasActivePending() = false, want true for active pending")
			}

			if err := st.ClearPending(chatID, userID); err != nil {
				t.Fatalf("ClearPending() error = %v", err)
			}
			settle()
			pending, err := st.GetPending(chatID, userID)
			if err != nil {
				t.Fatalf("GetPending() after ClearPending error = %v", err)
			}
			if pending != nil {
				t.Fatalf("GetPending() = %+v, want nil after ClearPending", pending)
			}

			expiredPending := activePending
			expiredPending.Timestamp = now.Add(-10 * time.Minute).Unix()
			expiredPending.RandomToken = "token-expired"
			expiredPending.ExpireAt = now.Add(-time.Minute)
			expiredPending.OriginalMessageID = 1002
			if err := st.SetPending(expiredPending); err != nil {
				t.Fatalf("SetPending(expired) error = %v", err)
			}
			settle()
			active, err = st.HasActivePending(chatID, userID)
			if err != nil {
				t.Fatalf("HasActivePending(expired) error = %v", err)
			}
			if active {
				t.Fatal("HasActivePending() = true, want false for expired pending")
			}
			if err := st.ClearPending(chatID, userID); err != nil {
				t.Fatalf("ClearPending(expired) error = %v", err)
			}
			settle()

			if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 1 {
				t.Fatalf("IncrWarningCount() = (%d, %v), want (1, nil)", count, err)
			}
			settle()
			if count, err := st.IncrWarningCount(chatID, userID); err != nil || count != 2 {
				t.Fatalf("IncrWarningCount(second) = (%d, %v), want (2, nil)", count, err)
			}
			settle()
			if err := st.ResetWarningCount(chatID, userID); err != nil {
				t.Fatalf("ResetWarningCount() error = %v", err)
			}
			settle()
			warnings, err := st.GetWarningCount(chatID, userID)
			if err != nil {
				t.Fatalf("GetWarningCount() after reset error = %v", err)
			}
			if warnings != 0 {
				t.Fatalf("GetWarningCount() = %d, want 0 after ResetWarningCount", warnings)
			}

			if err := st.AddBlacklistWord(0, "Spam", "tester"); err != nil {
				t.Fatalf("AddBlacklistWord(spam) error = %v", err)
			}
			if err := st.AddBlacklistWord(0, "Eggs", "tester"); err != nil {
				t.Fatalf("AddBlacklistWord(eggs) error = %v", err)
			}
			settle()

			words, err := st.GetBlacklistWords(0)
			if err != nil {
				t.Fatalf("GetBlacklistWords() error = %v", err)
			}
			sort.Strings(words)
			if want := []string{"eggs", "spam"}; !reflect.DeepEqual(words, want) {
				t.Fatalf("GetBlacklistWords() = %v, want %v", words, want)
			}

			if err := st.RemoveBlacklistWord(0, "  SPAM "); err != nil {
				t.Fatalf("RemoveBlacklistWord() error = %v", err)
			}
			settle()

			words, err = st.GetBlacklistWords(0)
			if err != nil {
				t.Fatalf("GetBlacklistWords() after remove error = %v", err)
			}
			sort.Strings(words)
			if want := []string{"eggs"}; !reflect.DeepEqual(words, want) {
				t.Fatalf("GetBlacklistWords() after remove = %v, want %v", words, want)
			}
		})
	}
}
