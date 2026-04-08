package bot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type moderationFlowStoreStub struct {
	verified    bool
	verifiedErr error
	rejected    bool
	rejectedErr error
	resolve     storepkg.PendingResolutionResult
	resolveErr  error
}

func (s *moderationFlowStoreStub) Close() error { return nil }
func (s *moderationFlowStoreStub) GetUserLanguagePreference(userID int64) (string, error) {
	return "", nil
}
func (s *moderationFlowStoreStub) SetUserLanguagePreference(userID int64, language string) error {
	return nil
}
func (s *moderationFlowStoreStub) IsVerified(chatID, userID int64) (bool, error) {
	return s.verified, s.verifiedErr
}
func (s *moderationFlowStoreStub) SetVerified(chatID, userID int64) error    { return nil }
func (s *moderationFlowStoreStub) RemoveVerified(chatID, userID int64) error { return nil }
func (s *moderationFlowStoreStub) IsRejected(chatID, userID int64) (bool, error) {
	return s.rejected, s.rejectedErr
}
func (s *moderationFlowStoreStub) SetRejected(chatID, userID int64) error    { return nil }
func (s *moderationFlowStoreStub) RemoveRejected(chatID, userID int64) error { return nil }
func (s *moderationFlowStoreStub) HasActivePending(chatID, userID int64) (bool, error) {
	return false, nil
}
func (s *moderationFlowStoreStub) GetPending(chatID, userID int64) (*storepkg.PendingVerification, error) {
	return nil, nil
}
func (s *moderationFlowStoreStub) ListPendingVerifications() ([]storepkg.PendingVerification, error) {
	return nil, nil
}
func (s *moderationFlowStoreStub) CreatePendingIfAbsent(pending storepkg.PendingVerification) (bool, *storepkg.PendingVerification, error) {
	return false, nil, nil
}
func (s *moderationFlowStoreStub) SetPending(pending storepkg.PendingVerification) error {
	return nil
}
func (s *moderationFlowStoreStub) UpdatePendingMetadataByToken(pending storepkg.PendingVerification) (bool, error) {
	return true, nil
}
func (s *moderationFlowStoreStub) ClearPending(chatID, userID int64) error { return nil }
func (s *moderationFlowStoreStub) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
	return s.resolve, s.resolveErr
}
func (s *moderationFlowStoreStub) ClearUserVerificationStateEverywhere(userID int64) error {
	return nil
}
func (s *moderationFlowStoreStub) GetWarningCount(chatID, userID int64) (int, error) {
	return 0, nil
}
func (s *moderationFlowStoreStub) IncrWarningCount(chatID, userID int64) (int, error) {
	return 0, nil
}
func (s *moderationFlowStoreStub) ResetWarningCount(chatID, userID int64) error { return nil }
func (s *moderationFlowStoreStub) GetBlacklistWords(chatID int64) ([]string, error) {
	return nil, nil
}
func (s *moderationFlowStoreStub) AddBlacklistWord(chatID int64, word, addedBy string) error {
	return nil
}
func (s *moderationFlowStoreStub) RemoveBlacklistWord(chatID int64, word string) error {
	return nil
}
func (s *moderationFlowStoreStub) GetAllBlacklistWords() (map[int64][]string, error) {
	return nil, nil
}

func newModerationFlowBot(t *testing.T, st storepkg.Store) *Bot {
	t.Helper()

	if st == nil {
		sqliteStore, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore() error = %v", err)
		}
		t.Cleanup(func() { _ = sqliteStore.Close() })
		st = sqliteStore
	}

	return &Bot{
		Config: &config.Config{
			Moderation: config.ModerationConfig{
				MaxWarnings:        3,
				VerifyWindow:       "5m",
				OriginalMessageTTL: "1m",
			},
		},
		Store: st,
	}
}

func TestModeratedMessageFromMessageSkipsUnsupportedMessages(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, nil)
	cases := []struct {
		name string
		msg  *gotgbot.Message
	}{
		{
			name: "nil message",
			msg:  nil,
		},
		{
			name: "missing sender",
			msg: &gotgbot.Message{
				Chat: gotgbot.Chat{Id: -100123},
			},
		},
		{
			name: "auto forwarded",
			msg: &gotgbot.Message{
				Chat:               gotgbot.Chat{Id: -100123},
				From:               &gotgbot.User{Id: 42},
				IsAutomaticForward: true,
			},
		},
		{
			name: "anonymous admin",
			msg: &gotgbot.Message{
				Chat:       gotgbot.Chat{Id: -100123},
				From:       &gotgbot.User{Id: 42},
				SenderChat: &gotgbot.Chat{Id: -100123},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if incoming, ok := b.moderatedMessageFromMessage(tc.msg); ok || incoming != nil {
				t.Fatalf("moderatedMessageFromMessage() = (%+v, %v), want (nil, false)", incoming, ok)
			}
		})
	}
}

func TestModeratedMessageFromMessageBuildsContext(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, nil)
	msg := &gotgbot.Message{
		MessageId:       1001,
		MessageThreadId: 7,
		Chat:            gotgbot.Chat{Id: -100123},
		From:            &gotgbot.User{Id: 42, LanguageCode: "en-us"},
	}

	incoming, ok := b.moderatedMessageFromMessage(msg)
	if !ok {
		t.Fatal("moderatedMessageFromMessage() ok = false, want true")
	}
	if incoming.chatID != msg.Chat.Id || incoming.user.Id != msg.From.Id {
		t.Fatalf("moderated message = %+v, want chat=%d user=%d", incoming, msg.Chat.Id, msg.From.Id)
	}
	if incoming.userLanguage != "en" {
		t.Fatalf("incoming.userLanguage = %q, want %q", incoming.userLanguage, "en")
	}
	if incoming.verifyWindow != 5*time.Minute {
		t.Fatalf("incoming.verifyWindow = %v, want %v", incoming.verifyWindow, 5*time.Minute)
	}
	if incoming.maxWarnings != 3 {
		t.Fatalf("incoming.maxWarnings = %d, want %d", incoming.maxWarnings, 3)
	}
}

func TestBuildPendingVerificationUsesModeratedMessageContext(t *testing.T) {
	t.Parallel()

	incoming := &moderatedMessage{
		message: &gotgbot.Message{
			MessageId:       1001,
			MessageThreadId: 7,
		},
		user:         &gotgbot.User{Id: 42},
		chatID:       -100123,
		userLanguage: "en",
		verifyWindow: 5 * time.Minute,
		maxWarnings:  3,
	}

	pending, err := buildPendingVerification(incoming)
	if err != nil {
		t.Fatalf("buildPendingVerification() error = %v", err)
	}

	if pending.ChatID != incoming.chatID || pending.UserID != incoming.user.Id {
		t.Fatalf("pending = %+v, want chat=%d user=%d", pending, incoming.chatID, incoming.user.Id)
	}
	if pending.UserLanguage != incoming.userLanguage {
		t.Fatalf("pending.UserLanguage = %q, want %q", pending.UserLanguage, incoming.userLanguage)
	}
	if pending.OriginalMessageID != incoming.message.MessageId || pending.ReplyToMessageID != incoming.message.MessageId {
		t.Fatalf("pending message ids = (%d, %d), want both %d", pending.OriginalMessageID, pending.ReplyToMessageID, incoming.message.MessageId)
	}
	if pending.MessageThreadID != incoming.message.MessageThreadId {
		t.Fatalf("pending.MessageThreadID = %d, want %d", pending.MessageThreadID, incoming.message.MessageThreadId)
	}
	if pending.RandomToken == "" {
		t.Fatal("pending.RandomToken = empty, want generated token")
	}
	if pending.ExpireAt.Sub(time.Unix(pending.Timestamp, 0).UTC()) != incoming.verifyWindow {
		t.Fatalf("pending verify window = %v, want %v", pending.ExpireAt.Sub(time.Unix(pending.Timestamp, 0).UTC()), incoming.verifyWindow)
	}
}

func TestHandlePendingStateAfterCreateRaceRetriesWithoutStateChange(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, &moderationFlowStoreStub{})
	incoming := &moderatedMessage{
		message: &gotgbot.Message{MessageId: 1001},
		user:    &gotgbot.User{Id: 42},
		chatID:  -100123,
	}

	outcome := b.handlePendingStateAfterCreateRace(nil, incoming)
	if outcome != pendingReservationRetry {
		t.Fatalf("handlePendingStateAfterCreateRace() = %v, want %v", outcome, pendingReservationRetry)
	}
}

func TestHandlePendingStateAfterCreateRaceStopsRejectedUser(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, &moderationFlowStoreStub{rejected: true})
	incoming := &moderatedMessage{
		message: &gotgbot.Message{MessageId: 1001},
		user:    &gotgbot.User{Id: 42},
		chatID:  -100123,
	}

	outcome := b.handlePendingStateAfterCreateRace(nil, incoming)
	if outcome != pendingReservationComplete {
		t.Fatalf("handlePendingStateAfterCreateRace() = %v, want %v", outcome, pendingReservationComplete)
	}
}

func TestHandleExpiredPendingWindowRetriesWhenUserStillNeedsVerification(t *testing.T) {
	t.Parallel()

	pending := &storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		Timestamp:         1,
		RandomToken:       "token123",
		OriginalMessageID: 1001,
	}
	b := newModerationFlowBot(t, &moderationFlowStoreStub{
		resolve: storepkg.PendingResolutionResult{
			Matched:      true,
			Pending:      pending,
			WarningCount: 1,
			ShouldBan:    false,
		},
	})
	incoming := &moderatedMessage{
		message:     &gotgbot.Message{MessageId: 2002},
		user:        &gotgbot.User{Id: 42},
		chatID:      -100123,
		maxWarnings: 3,
	}

	outcome := b.handleExpiredPendingWindow(nil, incoming, pending)
	if outcome != pendingReservationRetry {
		t.Fatalf("handleExpiredPendingWindow() = %v, want %v", outcome, pendingReservationRetry)
	}
}
