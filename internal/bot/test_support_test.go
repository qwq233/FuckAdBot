package bot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type hookedStore struct {
	storepkg.Store

	addBlacklistWordHook             func(chatID int64, word, addedBy string) error
	removeBlacklistWordHook          func(chatID int64, word string) error
	clearUserVerificationStateHook   func(userID int64) error
	setUserLanguagePreferenceHook    func(userID int64, language string) error
	setVerifiedHook                  func(chatID, userID int64) error
	removeVerifiedHook               func(chatID, userID int64) error
	setRejectedHook                  func(chatID, userID int64) error
	removeRejectedHook               func(chatID, userID int64) error
	resetWarningCountHook            func(chatID, userID int64) error
	getPendingHook                   func(chatID, userID int64) (*storepkg.PendingVerification, error)
	reserveVerificationWindowHook    func(pending storepkg.PendingVerification, maxWarnings int) (storepkg.VerificationReservationResult, error)
	updatePendingMetadataByTokenHook func(pending storepkg.PendingVerification) (bool, error)
	resolvePendingByTokenHook        func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error)
	createPendingIfAbsentHook        func(pending storepkg.PendingVerification) (bool, *storepkg.PendingVerification, error)
	getWarningCountHook              func(chatID, userID int64) (int, error)
	isVerifiedHook                   func(chatID, userID int64) (bool, error)
	isRejectedHook                   func(chatID, userID int64) (bool, error)
	getUserLanguagePreferenceHook    func(userID int64) (string, error)
}

func (s *hookedStore) AddBlacklistWord(chatID int64, word, addedBy string) error {
	if s.addBlacklistWordHook != nil {
		return s.addBlacklistWordHook(chatID, word, addedBy)
	}
	return s.Store.AddBlacklistWord(chatID, word, addedBy)
}

func (s *hookedStore) RemoveBlacklistWord(chatID int64, word string) error {
	if s.removeBlacklistWordHook != nil {
		return s.removeBlacklistWordHook(chatID, word)
	}
	return s.Store.RemoveBlacklistWord(chatID, word)
}

func (s *hookedStore) ClearUserVerificationStateEverywhere(userID int64) error {
	if s.clearUserVerificationStateHook != nil {
		return s.clearUserVerificationStateHook(userID)
	}
	return s.Store.ClearUserVerificationStateEverywhere(userID)
}

func (s *hookedStore) SetUserLanguagePreference(userID int64, language string) error {
	if s.setUserLanguagePreferenceHook != nil {
		return s.setUserLanguagePreferenceHook(userID, language)
	}
	return s.Store.SetUserLanguagePreference(userID, language)
}

func (s *hookedStore) SetVerified(chatID, userID int64) error {
	if s.setVerifiedHook != nil {
		return s.setVerifiedHook(chatID, userID)
	}
	return s.Store.SetVerified(chatID, userID)
}

func (s *hookedStore) RemoveVerified(chatID, userID int64) error {
	if s.removeVerifiedHook != nil {
		return s.removeVerifiedHook(chatID, userID)
	}
	return s.Store.RemoveVerified(chatID, userID)
}

func (s *hookedStore) SetRejected(chatID, userID int64) error {
	if s.setRejectedHook != nil {
		return s.setRejectedHook(chatID, userID)
	}
	return s.Store.SetRejected(chatID, userID)
}

func (s *hookedStore) RemoveRejected(chatID, userID int64) error {
	if s.removeRejectedHook != nil {
		return s.removeRejectedHook(chatID, userID)
	}
	return s.Store.RemoveRejected(chatID, userID)
}

func (s *hookedStore) ResetWarningCount(chatID, userID int64) error {
	if s.resetWarningCountHook != nil {
		return s.resetWarningCountHook(chatID, userID)
	}
	return s.Store.ResetWarningCount(chatID, userID)
}

func (s *hookedStore) GetPending(chatID, userID int64) (*storepkg.PendingVerification, error) {
	if s.getPendingHook != nil {
		return s.getPendingHook(chatID, userID)
	}
	return s.Store.GetPending(chatID, userID)
}

func (s *hookedStore) ReserveVerificationWindow(pending storepkg.PendingVerification, maxWarnings int) (storepkg.VerificationReservationResult, error) {
	if s.reserveVerificationWindowHook != nil {
		return s.reserveVerificationWindowHook(pending, maxWarnings)
	}
	return s.Store.ReserveVerificationWindow(pending, maxWarnings)
}

func (s *hookedStore) UpdatePendingMetadataByToken(pending storepkg.PendingVerification) (bool, error) {
	if s.updatePendingMetadataByTokenHook != nil {
		return s.updatePendingMetadataByTokenHook(pending)
	}
	return s.Store.UpdatePendingMetadataByToken(pending)
}

func (s *hookedStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
	if s.resolvePendingByTokenHook != nil {
		return s.resolvePendingByTokenHook(chatID, userID, timestamp, randomToken, action, maxWarnings)
	}
	return s.Store.ResolvePendingByToken(chatID, userID, timestamp, randomToken, action, maxWarnings)
}

func (s *hookedStore) CreatePendingIfAbsent(pending storepkg.PendingVerification) (bool, *storepkg.PendingVerification, error) {
	if s.createPendingIfAbsentHook != nil {
		return s.createPendingIfAbsentHook(pending)
	}
	return s.Store.CreatePendingIfAbsent(pending)
}

func (s *hookedStore) GetWarningCount(chatID, userID int64) (int, error) {
	if s.getWarningCountHook != nil {
		return s.getWarningCountHook(chatID, userID)
	}
	return s.Store.GetWarningCount(chatID, userID)
}

func (s *hookedStore) IsVerified(chatID, userID int64) (bool, error) {
	if s.isVerifiedHook != nil {
		return s.isVerifiedHook(chatID, userID)
	}
	return s.Store.IsVerified(chatID, userID)
}

func (s *hookedStore) IsRejected(chatID, userID int64) (bool, error) {
	if s.isRejectedHook != nil {
		return s.isRejectedHook(chatID, userID)
	}
	return s.Store.IsRejected(chatID, userID)
}

func (s *hookedStore) GetUserLanguagePreference(userID int64) (string, error) {
	if s.getUserLanguagePreferenceHook != nil {
		return s.getUserLanguagePreferenceHook(userID)
	}
	return s.Store.GetUserLanguagePreference(userID)
}

func newTestBot(t *testing.T, st storepkg.Store, client *recordingBotClient) *Bot {
	t.Helper()

	if st == nil {
		sqliteStore, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore() error = %v", err)
		}
		t.Cleanup(func() { _ = sqliteStore.Close() })
		st = sqliteStore
	}
	if client == nil {
		client = &recordingBotClient{}
	}

	return &Bot{
		Bot:       newRecordingTelegramBot(client),
		Config:    newTestConfig(),
		Store:     st,
		Blacklist: blacklist.New(),
		timers:    make(map[timerKey][]*time.Timer),
	}
}

func newTestConfig() *config.Config {
	return &config.Config{
		Bot: config.BotConfig{
			Admins: []int64{7},
		},
		Turnstile: config.TurnstileConfig{
			Domain:        "verify.example.com",
			ListenAddr:    "127.0.0.1",
			ListenPort:    8080,
			VerifyTimeout: "5m",
		},
		Moderation: config.ModerationConfig{
			MaxWarnings:        3,
			ReminderTTL:        30,
			VerifyWindow:       "5m",
			OriginalMessageTTL: "1m",
		},
	}
}

func newMessageContext(bot *gotgbot.Bot, msg *gotgbot.Message) *ext.Context {
	return ext.NewContext(bot, &gotgbot.Update{
		UpdateId: 1,
		Message:  msg,
	}, nil)
}

func newCallbackContext(bot *gotgbot.Bot, cq *gotgbot.CallbackQuery) *ext.Context {
	return ext.NewContext(bot, &gotgbot.Update{
		UpdateId:      1,
		CallbackQuery: cq,
	}, nil)
}
