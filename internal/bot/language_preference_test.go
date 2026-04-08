package bot

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type countingPreferenceStore struct {
	preference string
	getCalls   int
	setCalls   int
}

func (s *countingPreferenceStore) Close() error { return nil }

func (s *countingPreferenceStore) GetUserLanguagePreference(userID int64) (string, error) {
	s.getCalls++
	return s.preference, nil
}

func (s *countingPreferenceStore) SetUserLanguagePreference(userID int64, language string) error {
	s.setCalls++
	s.preference = language
	return nil
}

func (s *countingPreferenceStore) IsVerified(chatID, userID int64) (bool, error) { return false, nil }
func (s *countingPreferenceStore) SetVerified(chatID, userID int64) error        { return nil }
func (s *countingPreferenceStore) RemoveVerified(chatID, userID int64) error     { return nil }
func (s *countingPreferenceStore) IsRejected(chatID, userID int64) (bool, error) { return false, nil }
func (s *countingPreferenceStore) SetRejected(chatID, userID int64) error        { return nil }
func (s *countingPreferenceStore) RemoveRejected(chatID, userID int64) error     { return nil }
func (s *countingPreferenceStore) HasActivePending(chatID, userID int64) (bool, error) {
	return false, nil
}
func (s *countingPreferenceStore) GetPending(chatID, userID int64) (*storepkg.PendingVerification, error) {
	return nil, nil
}
func (s *countingPreferenceStore) CreatePendingIfAbsent(pending storepkg.PendingVerification) (bool, *storepkg.PendingVerification, error) {
	return true, nil, nil
}
func (s *countingPreferenceStore) SetPending(pending storepkg.PendingVerification) error {
	return nil
}
func (s *countingPreferenceStore) UpdatePendingMetadataByToken(pending storepkg.PendingVerification) (bool, error) {
	return true, nil
}
func (s *countingPreferenceStore) ClearPending(chatID, userID int64) error { return nil }
func (s *countingPreferenceStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
	return storepkg.PendingResolutionResult{}, nil
}
func (s *countingPreferenceStore) ClearUserVerificationStateEverywhere(userID int64) error {
	return nil
}
func (s *countingPreferenceStore) GetWarningCount(chatID, userID int64) (int, error)  { return 0, nil }
func (s *countingPreferenceStore) IncrWarningCount(chatID, userID int64) (int, error) { return 0, nil }
func (s *countingPreferenceStore) ResetWarningCount(chatID, userID int64) error       { return nil }
func (s *countingPreferenceStore) GetBlacklistWords(chatID int64) ([]string, error)   { return nil, nil }
func (s *countingPreferenceStore) AddBlacklistWord(chatID int64, word, addedBy string) error {
	return nil
}
func (s *countingPreferenceStore) RemoveBlacklistWord(chatID int64, word string) error {
	return nil
}
func (s *countingPreferenceStore) GetAllBlacklistWords() (map[int64][]string, error) {
	return nil, nil
}

func TestApplyUserLanguagePreferenceRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := &Bot{Store: store}
	if err := store.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}

	language, changed, err := b.applyUserLanguagePreference(42, "fr")
	if err != nil {
		t.Fatalf("applyUserLanguagePreference() error = %v", err)
	}
	if changed {
		t.Fatal("applyUserLanguagePreference() changed = true, want false")
	}
	if language != "" {
		t.Fatalf("applyUserLanguagePreference() language = %q, want empty", language)
	}

	storedLanguage, err := store.GetUserLanguagePreference(42)
	if err != nil {
		t.Fatalf("GetUserLanguagePreference() error = %v", err)
	}
	if storedLanguage != "en" {
		t.Fatalf("stored language = %q, want %q", storedLanguage, "en")
	}
}

func TestRequestLanguageForUserPrefersStoredPreference(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if err := store.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}

	b := &Bot{Store: store}
	user := &gotgbot.User{Id: 42, LanguageCode: "zh-cn"}

	if language := b.requestLanguageForUser(user); language != "en" {
		t.Fatalf("requestLanguageForUser() = %q, want %q", language, "en")
	}
}

func TestRequestLanguageForUserCachesStoreLookup(t *testing.T) {
	t.Parallel()

	store := &countingPreferenceStore{preference: "en"}
	b := &Bot{Store: store}
	user := &gotgbot.User{Id: 42, LanguageCode: "zh-cn"}

	if language := b.requestLanguageForUser(user); language != "en" {
		t.Fatalf("first requestLanguageForUser() = %q, want %q", language, "en")
	}
	if language := b.requestLanguageForUser(user); language != "en" {
		t.Fatalf("second requestLanguageForUser() = %q, want %q", language, "en")
	}

	if store.getCalls != 1 {
		t.Fatalf("GetUserLanguagePreference() call count = %d, want 1", store.getCalls)
	}
}

func TestApplyUserLanguagePreferenceWarmsCache(t *testing.T) {
	t.Parallel()

	store := &countingPreferenceStore{}
	b := &Bot{Store: store}

	language, changed, err := b.applyUserLanguagePreference(42, "en")
	if err != nil {
		t.Fatalf("applyUserLanguagePreference() error = %v", err)
	}
	if !changed || language != "en" {
		t.Fatalf("applyUserLanguagePreference() = (%q, %v), want (%q, true)", language, changed, "en")
	}

	user := &gotgbot.User{Id: 42, LanguageCode: "zh-cn"}
	if got := b.requestLanguageForUser(user); got != "en" {
		t.Fatalf("requestLanguageForUser() after apply = %q, want %q", got, "en")
	}

	if store.getCalls != 0 {
		t.Fatalf("GetUserLanguagePreference() call count = %d, want 0 after cache warm", store.getCalls)
	}
	if store.setCalls != 1 {
		t.Fatalf("SetUserLanguagePreference() call count = %d, want 1", store.setCalls)
	}
}

func TestCachedUserChatUsesCachedValue(t *testing.T) {
	t.Parallel()

	b := &Bot{}
	fetchCalls := 0
	fetch := func(userID int64) (*gotgbot.ChatFullInfo, error) {
		fetchCalls++
		return &gotgbot.ChatFullInfo{Id: userID, Bio: "cached bio"}, nil
	}

	chat := b.cachedUserChat(42, fetch)
	if chat == nil || chat.Bio != "cached bio" {
		t.Fatalf("first cachedUserChat() = %+v, want bio %q", chat, "cached bio")
	}

	chat = b.cachedUserChat(42, fetch)
	if chat == nil || chat.Bio != "cached bio" {
		t.Fatalf("second cachedUserChat() = %+v, want bio %q", chat, "cached bio")
	}

	if fetchCalls != 1 {
		t.Fatalf("fetch call count = %d, want 1", fetchCalls)
	}
}

func TestCachedUserChatCachesFetchFailure(t *testing.T) {
	t.Parallel()

	b := &Bot{}
	fetchCalls := 0
	fetch := func(userID int64) (*gotgbot.ChatFullInfo, error) {
		fetchCalls++
		return nil, errors.New("boom")
	}

	if chat := b.cachedUserChat(42, fetch); chat != nil {
		t.Fatalf("first cachedUserChat() = %+v, want nil", chat)
	}
	if chat := b.cachedUserChat(42, fetch); chat != nil {
		t.Fatalf("second cachedUserChat() = %+v, want nil", chat)
	}

	if fetchCalls != 1 {
		t.Fatalf("fetch call count = %d, want 1", fetchCalls)
	}
}
