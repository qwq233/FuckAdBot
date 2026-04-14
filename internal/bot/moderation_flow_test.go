package bot

import (
	"errors"
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
	reserve     storepkg.VerificationReservationResult
	reserveErr  error
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
func (s *moderationFlowStoreStub) ReserveVerificationWindow(pending storepkg.PendingVerification, maxWarnings int) (storepkg.VerificationReservationResult, error) {
	return s.reserve, s.reserveErr
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
		Store:  st,
		timers: make(map[timerKey][]*time.Timer),
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

func TestHandlePendingStateAfterCreateRaceStopsVerifiedUser(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, &moderationFlowStoreStub{verified: true})
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

func TestHandleExistingPendingReservationDeletesIncomingAndOldOriginalForActiveWindow(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	client := &recordingBotClient{}
	b := newModerationFlowBot(t, store)
	b.Bot = newRecordingTelegramBot(client)

	existing := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().Add(-2 * time.Minute).UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 7001,
	}
	if err := store.SetPending(existing); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	incoming := &moderatedMessage{
		message: &gotgbot.Message{MessageId: 2002},
		user:    &gotgbot.User{Id: existing.UserID},
		chatID:  existing.ChatID,
	}
	outcome := b.handleExistingPendingReservation(b.Bot, incoming, &existing)
	if outcome != pendingReservationComplete {
		t.Fatalf("handleExistingPendingReservation() = %v, want %v", outcome, pendingReservationComplete)
	}

	gotPending, err := store.GetPending(existing.ChatID, existing.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending == nil || gotPending.OriginalMessageID != 0 {
		t.Fatalf("GetPending() = %+v, want original_message_id cleared", gotPending)
	}
	if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
		t.Fatalf("deleteMessage request count = %d, want 2", got)
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

func TestHandleExpiredPendingWindowCompletesForTerminalOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("verified", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newModerationFlowBot(t, &moderationFlowStoreStub{
			resolve: storepkg.PendingResolutionResult{
				Matched:  true,
				Pending:  &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001},
				Verified: true,
			},
		})
		b.Bot = newRecordingTelegramBot(client)
		pending := &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001}
		incoming := &moderatedMessage{
			message:     &gotgbot.Message{MessageId: 2002},
			user:        &gotgbot.User{Id: 42},
			chatID:      -100123,
			maxWarnings: 3,
		}

		outcome := b.handleExpiredPendingWindow(b.Bot, incoming, pending)
		if outcome != pendingReservationComplete {
			t.Fatalf("handleExpiredPendingWindow() = %v, want %v", outcome, pendingReservationComplete)
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1 for original message cleanup", got)
		}
		if got := len(client.RequestsByMethod("banChatMember")); got != 0 {
			t.Fatalf("banChatMember request count = %d, want 0", got)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newModerationFlowBot(t, &moderationFlowStoreStub{
			resolve: storepkg.PendingResolutionResult{
				Matched:  true,
				Pending:  &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001},
				Rejected: true,
			},
		})
		b.Bot = newRecordingTelegramBot(client)
		pending := &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001}
		incoming := &moderatedMessage{
			message:     &gotgbot.Message{MessageId: 2002},
			user:        &gotgbot.User{Id: 42},
			chatID:      -100123,
			maxWarnings: 3,
		}

		outcome := b.handleExpiredPendingWindow(b.Bot, incoming, pending)
		if outcome != pendingReservationComplete {
			t.Fatalf("handleExpiredPendingWindow() = %v, want %v", outcome, pendingReservationComplete)
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
			t.Fatalf("deleteMessage request count = %d, want 2", got)
		}
		if got := len(client.RequestsByMethod("banChatMember")); got != 0 {
			t.Fatalf("banChatMember request count = %d, want 0", got)
		}
	})

	t.Run("ban", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newModerationFlowBot(t, &moderationFlowStoreStub{
			resolve: storepkg.PendingResolutionResult{
				Matched:      true,
				Pending:      &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001},
				WarningCount: 3,
				ShouldBan:    true,
			},
		})
		b.Bot = newRecordingTelegramBot(client)
		pending := &storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a", OriginalMessageID: 7001}
		incoming := &moderatedMessage{
			message:     &gotgbot.Message{MessageId: 2002},
			user:        &gotgbot.User{Id: 42},
			chatID:      -100123,
			maxWarnings: 3,
		}

		outcome := b.handleExpiredPendingWindow(b.Bot, incoming, pending)
		if outcome != pendingReservationComplete {
			t.Fatalf("handleExpiredPendingWindow() = %v, want %v", outcome, pendingReservationComplete)
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
			t.Fatalf("deleteMessage request count = %d, want 2", got)
		}
		if got := len(client.RequestsByMethod("banChatMember")); got != 1 {
			t.Fatalf("banChatMember request count = %d, want 1", got)
		}
	})
}

func TestSendVerificationReminderCancelsPendingOnSendFailure(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	client.SetError("sendMessage", errors.New("boom"))
	var actions []storepkg.PendingAction
	b := newTestBot(t, &hookedStore{
		Store: base,
		resolvePendingByTokenHook: func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
			actions = append(actions, action)
			return storepkg.PendingResolutionResult{Matched: true}, nil
		},
	}, client)

	incoming := &moderatedMessage{
		message:     &gotgbot.Message{MessageId: 1001},
		user:        &gotgbot.User{Id: 42},
		chatID:      -100123,
		maxWarnings: 3,
	}
	pending := storepkg.PendingVerification{
		ChatID:      incoming.chatID,
		UserID:      incoming.user.Id,
		Timestamp:   1,
		RandomToken: "token-a",
	}

	reminderMsg, ok := b.sendVerificationReminder(b.Bot, incoming, pending, "hello")
	if ok || reminderMsg != nil {
		t.Fatalf("sendVerificationReminder() = (%+v, %v), want (nil, false)", reminderMsg, ok)
	}
	if len(actions) != 1 || actions[0] != storepkg.PendingActionCancel {
		t.Fatalf("cancel actions = %v, want [cancel]", actions)
	}
	if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
		t.Fatalf("deleteMessage request count = %d, want 1", got)
	}
}

func TestPersistVerificationReminderHandlesUpdateFailureAndRace(t *testing.T) {
	t.Parallel()

	t.Run("update failure cancels pending", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		var actions []storepkg.PendingAction
		b := newTestBot(t, &hookedStore{
			Store: base,
			updatePendingMetadataByTokenHook: func(pending storepkg.PendingVerification) (bool, error) {
				return false, errors.New("boom")
			},
			resolvePendingByTokenHook: func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
				actions = append(actions, action)
				return storepkg.PendingResolutionResult{Matched: true}, nil
			},
		}, client)

		incoming := &moderatedMessage{
			message:     &gotgbot.Message{MessageId: 1001},
			user:        &gotgbot.User{Id: 42},
			chatID:      -100123,
			maxWarnings: 3,
		}
		pending := storepkg.PendingVerification{
			ChatID:      incoming.chatID,
			UserID:      incoming.user.Id,
			Timestamp:   1,
			RandomToken: "token-a",
		}
		reminderMsg := &gotgbot.Message{MessageId: 5001, Chat: gotgbot.Chat{Id: incoming.chatID, Type: "supergroup"}}

		if ok := b.persistVerificationReminder(b.Bot, incoming, pending, reminderMsg); ok {
			t.Fatal("persistVerificationReminder() = true, want false on update failure")
		}
		if len(actions) != 1 || actions[0] != storepkg.PendingActionCancel {
			t.Fatalf("cancel actions = %v, want [cancel]", actions)
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1", got)
		}
	})

	t.Run("race deletes reminder without cancel", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		var actions []storepkg.PendingAction
		b := newTestBot(t, &hookedStore{
			Store: base,
			updatePendingMetadataByTokenHook: func(pending storepkg.PendingVerification) (bool, error) {
				return false, nil
			},
			resolvePendingByTokenHook: func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
				actions = append(actions, action)
				return storepkg.PendingResolutionResult{Matched: true}, nil
			},
		}, client)

		incoming := &moderatedMessage{
			message:     &gotgbot.Message{MessageId: 1001},
			user:        &gotgbot.User{Id: 42},
			chatID:      -100123,
			maxWarnings: 3,
		}
		pending := storepkg.PendingVerification{
			ChatID:      incoming.chatID,
			UserID:      incoming.user.Id,
			Timestamp:   1,
			RandomToken: "token-a",
		}
		reminderMsg := &gotgbot.Message{MessageId: 5001, Chat: gotgbot.Chat{Id: incoming.chatID, Type: "supergroup"}}

		if ok := b.persistVerificationReminder(b.Bot, incoming, pending, reminderMsg); ok {
			t.Fatal("persistVerificationReminder() = true, want false on pending race")
		}
		if len(actions) != 0 {
			t.Fatalf("cancel actions = %v, want none", actions)
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1", got)
		}
	})
}

func TestCancelPendingVerificationClearsStoredPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := newModerationFlowBot(t, store)
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

	b.cancelPendingVerification(pending, 3, "test")

	gotPending, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if gotPending != nil {
		t.Fatalf("GetPending() = %+v, want nil after cancelPendingVerification", gotPending)
	}
}
