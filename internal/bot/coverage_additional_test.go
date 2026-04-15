package bot

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type testVerificationProvider struct{}

func (testVerificationProvider) GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string {
	return "https://example.invalid/verify"
}

func TestRecordInternalFaultWrapperRecordsError(t *testing.T) {
	t.Parallel()

	b := newTestBot(t, &moderationFlowStoreStub{}, &recordingBotClient{})
	b.Config.Bot.Admins = nil

	b.RecordInternalFault("store.runtime", errors.New("queue stalled"))

	snapshot := b.runtimeStats.snapshot()
	if len(snapshot.RecentErrors) != 1 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: store.runtime: queue stalled") {
		t.Fatalf("RecentErrors = %v, want recorded wrapper fault", snapshot.RecentErrors)
	}
	if got := len(b.internalFaults.entries); got != 1 {
		t.Fatalf("len(internalFaults.entries) = %d, want 1", got)
	}
}

func TestHandleVerifiedUserReturnsStoredStateAndRecordsStoreErrors(t *testing.T) {
	t.Parallel()

	t.Run("verified", func(t *testing.T) {
		t.Parallel()

		b := newTestBot(t, &moderationFlowStoreStub{}, &recordingBotClient{})
		b.Store = &hookedStore{
			Store: &moderationFlowStoreStub{},
			isVerifiedHook: func(chatID, userID int64) (bool, error) {
				return true, nil
			},
		}

		incoming := &moderatedMessage{
			chatID: -100123,
			user:   &gotgbot.User{Id: 42},
		}

		if !b.handleVerifiedUser(incoming) {
			t.Fatal("handleVerifiedUser() = false, want true for verified user")
		}
	})

	t.Run("store error", func(t *testing.T) {
		t.Parallel()

		b := newTestBot(t, &moderationFlowStoreStub{}, &recordingBotClient{})
		b.Config.Bot.Admins = nil
		b.Store = &hookedStore{
			Store: &moderationFlowStoreStub{},
			isVerifiedHook: func(chatID, userID int64) (bool, error) {
				return false, errors.New("verification lookup failed")
			},
		}

		incoming := &moderatedMessage{
			chatID: -100123,
			user:   &gotgbot.User{Id: 42},
		}

		if !b.handleVerifiedUser(incoming) {
			t.Fatal("handleVerifiedUser() = false, want true on internal fault short-circuit")
		}

		snapshot := b.runtimeStats.snapshot()
		if len(snapshot.RecentErrors) == 0 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: store.is_verified: verification lookup failed") {
			t.Fatalf("RecentErrors = %v, want recorded store.is_verified fault", snapshot.RecentErrors)
		}
	})
}

func TestHandleGroupAdminAutoApprovalSuccessAndErrorPaths(t *testing.T) {
	t.Parallel()

	adminResponder := func(params map[string]any) (json.RawMessage, error) {
		userID := toInt64(params["user_id"])
		return json.RawMessage(`{"status":"administrator","user":{"id":` + strconv.FormatInt(userID, 10) + `,"is_bot":false,"first_name":"Admin"}}`), nil
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetResponder("getChatMember", adminResponder)
		setVerifiedCalled := false
		resetWarningsCalled := false
		removeRejectedCalled := false
		b := newTestBot(t, &hookedStore{
			Store: &moderationFlowStoreStub{},
			getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
				return nil, nil
			},
			setVerifiedHook: func(chatID, userID int64) error {
				setVerifiedCalled = true
				return nil
			},
			resetWarningCountHook: func(chatID, userID int64) error {
				resetWarningsCalled = true
				return nil
			},
			removeRejectedHook: func(chatID, userID int64) error {
				removeRejectedCalled = true
				return nil
			},
		}, client)

		incoming := &moderatedMessage{
			chatID: -100123,
			user:   &gotgbot.User{Id: 42},
		}

		if !b.handleGroupAdminAutoApproval(b.Bot, incoming) {
			t.Fatal("handleGroupAdminAutoApproval() = false, want true for group admin")
		}
		if !setVerifiedCalled || !resetWarningsCalled || !removeRejectedCalled {
			t.Fatalf("approveUser hooks called = verified:%v reset:%v unreject:%v, want all true", setVerifiedCalled, resetWarningsCalled, removeRejectedCalled)
		}
	})

	t.Run("approval error", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetResponder("getChatMember", adminResponder)
		b := newTestBot(t, &moderationFlowStoreStub{}, client)
		b.Config.Bot.Admins = nil
		b.Store = &hookedStore{
			Store: &moderationFlowStoreStub{},
			getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
				return nil, errors.New("pending lookup failed")
			},
		}

		incoming := &moderatedMessage{
			chatID: -100123,
			user:   &gotgbot.User{Id: 42},
		}

		if !b.handleGroupAdminAutoApproval(b.Bot, incoming) {
			t.Fatal("handleGroupAdminAutoApproval() = false, want true even when auto-approval fails")
		}

		snapshot := b.runtimeStats.snapshot()
		if len(snapshot.RecentErrors) == 0 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: moderation.auto_approve: pending lookup failed") {
			t.Fatalf("RecentErrors = %v, want moderation.auto_approve fault", snapshot.RecentErrors)
		}
	})
}

func TestCancelPendingVerificationLogsResolveErrors(t *testing.T) {
	t.Parallel()

	b := newTestBot(t, &moderationFlowStoreStub{}, &recordingBotClient{})
	called := false
	b.Store = &hookedStore{
		Store: &moderationFlowStoreStub{},
		resolvePendingByTokenHook: func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
			called = true
			return storepkg.PendingResolutionResult{Action: action}, errors.New("cancel failed")
		},
	}

	b.cancelPendingVerification(storepkg.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   123,
		RandomToken: "token-a",
	}, 3, "test")

	if !called {
		t.Fatal("ResolvePendingByToken() was not called")
	}
}

func TestScheduleOriginalMessageDeletionSkipsMissingMessageAndDeletesExpiredMessage(t *testing.T) {
	t.Parallel()

	t.Run("skip when original message is missing", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, &moderationFlowStoreStub{}, client)

		b.scheduleOriginalMessageDeletion(b.Bot, storepkg.PendingVerification{
			ChatID:      -100123,
			UserID:      42,
			Timestamp:   time.Now().UTC().Add(-2 * time.Minute).Unix(),
			RandomToken: "token-skip",
		})

		if got := len(client.RequestsByMethod("deleteMessage")); got != 0 {
			t.Fatalf("deleteMessage request count = %d, want 0", got)
		}
		if got := len(b.timers); got != 0 {
			t.Fatalf("timers map = %+v, want empty", b.timers)
		}
	})

	t.Run("delete immediately when ttl already elapsed", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		updatedPending := storepkg.PendingVerification{}
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Add(-2 * time.Minute).Unix(),
			RandomToken:       "token-delete",
			ExpireAt:          time.Now().UTC().Add(3 * time.Minute).Truncate(time.Second),
			OriginalMessageID: 1001,
		}
		b := newTestBot(t, &hookedStore{
			Store: &moderationFlowStoreStub{},
			isVerifiedHook: func(chatID, userID int64) (bool, error) {
				return false, nil
			},
			getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
				current := pending
				return &current, nil
			},
			updatePendingMetadataByTokenHook: func(next storepkg.PendingVerification) (bool, error) {
				updatedPending = next
				return true, nil
			},
		}, client)

		b.scheduleOriginalMessageDeletion(b.Bot, pending)

		if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1", got)
		}

		if updatedPending.OriginalMessageID != 0 {
			t.Fatalf("updated pending = %+v, want OriginalMessageID cleared after immediate delete", updatedPending)
		}
	})
}

func TestFormatStoreOperationStatusCoversUnknownErrorAndOK(t *testing.T) {
	t.Parallel()

	if got := formatStoreOperationStatus(time.Time{}, "", "en"); got != tr("en", "diag_unknown") {
		t.Fatalf("formatStoreOperationStatus(zero) = %q, want diag_unknown", got)
	}

	at := time.Date(2026, time.April, 15, 9, 0, 0, 0, time.UTC)
	if got, want := formatStoreOperationStatus(at, "boom", "en"), "2026-04-15T09:00:00Z error=boom"; got != want {
		t.Fatalf("formatStoreOperationStatus(error) = %q, want %q", got, want)
	}
	if got, want := formatStoreOperationStatus(at, "", "en"), "2026-04-15T09:00:00Z ok"; got != want {
		t.Fatalf("formatStoreOperationStatus(ok) = %q, want %q", got, want)
	}
}

func TestAppendTripleIntCodeLineFormatsExpectedOutput(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	appendTripleIntCodeLine(&builder, "Counts", 1, 2, 3)

	if got, want := builder.String(), "Counts: <code>1 / 2 / 3</code>\n"; got != want {
		t.Fatalf("appendTripleIntCodeLine() = %q, want %q", got, want)
	}
}

func TestSetCaptchaHandlesNilReceiverAndStoresProvider(t *testing.T) {
	t.Parallel()

	provider := testVerificationProvider{}
	var nilBot *Bot
	nilBot.SetCaptcha(provider)

	b := &Bot{}
	b.SetCaptcha(provider)
	if b.Captcha != provider {
		t.Fatalf("Captcha = %#v, want %#v", b.Captcha, provider)
	}
}

func TestDefaultUpdaterFactoryReturnsExtAdapter(t *testing.T) {
	t.Parallel()

	updater := defaultUpdaterFactory(ext.NewDispatcher(nil), nil)
	adapter, ok := updater.(*extUpdaterAdapter)
	if !ok {
		t.Fatalf("defaultUpdaterFactory() type = %T, want *extUpdaterAdapter", updater)
	}
	if adapter.Updater == nil {
		t.Fatal("defaultUpdaterFactory() returned adapter with nil Updater")
	}
}

func TestDispatcherMaxRoutinesFallsBackToDefaultWithoutConfig(t *testing.T) {
	t.Parallel()

	want := newTestConfig().Bot.GetDispatcherMaxRoutines()

	var nilBot *Bot
	if got := nilBot.dispatcherMaxRoutines(); got != want {
		t.Fatalf("nilBot.dispatcherMaxRoutines() = %d, want %d", got, want)
	}

	if got := (&Bot{}).dispatcherMaxRoutines(); got != want {
		t.Fatalf("(&Bot{}).dispatcherMaxRoutines() = %d, want %d", got, want)
	}
}

func TestScheduleOriginalMessageDeletionRunsDeferredCleanup(t *testing.T) {
	t.Parallel()

	store := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, store, client)
	b.Config.Moderation.OriginalMessageTTL = "20ms"

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-delay",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 1001,
	}
	if err := b.Store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	b.scheduleOriginalMessageDeletion(b.Bot, pending)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := len(client.RequestsByMethod("deleteMessage")); got == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
		t.Fatalf("deleteMessage request count = %d, want 1 after deferred cleanup", got)
	}

	currentPending, err := b.Store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if currentPending == nil || currentPending.OriginalMessageID != 0 {
		t.Fatalf("GetPending() = %+v, want OriginalMessageID cleared", currentPending)
	}
}

func TestHandleExistingPendingReservationRetriesWhenRacePersists(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, &moderationFlowStoreStub{})
	incoming := &moderatedMessage{
		message: &gotgbot.Message{MessageId: 1001},
		user:    &gotgbot.User{Id: 42},
		chatID:  -100123,
	}

	outcome := b.handleExistingPendingReservation(nil, incoming, nil)
	if outcome != pendingReservationRetry {
		t.Fatalf("handleExistingPendingReservation() = %v, want %v", outcome, pendingReservationRetry)
	}
}

func TestCmdApproveReportsStoreFailures(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, &hookedStore{
		Store: base,
		getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
			return nil, errors.New("approve lookup failed")
		},
	}, client)

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/approve",
		ReplyToMessage: &gotgbot.Message{
			From: &gotgbot.User{Id: 42},
		},
	}
	if err := b.cmdApprove(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdApprove() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "approve_failed"); got != want {
		t.Fatalf("approve failure text = %q, want %q", got, want)
	}

	snapshot := b.runtimeStats.snapshot()
	if len(snapshot.RecentErrors) == 0 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: moderation.manual_approve: approve lookup failed") {
		t.Fatalf("RecentErrors = %v, want moderation.manual_approve fault", snapshot.RecentErrors)
	}
}

func TestCmdRejectReportsStoreFailures(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, &hookedStore{
		Store: base,
		getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
			return nil, errors.New("reject lookup failed")
		},
	}, client)

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/reject",
		ReplyToMessage: &gotgbot.Message{
			From: &gotgbot.User{Id: 42},
		},
	}
	if err := b.cmdReject(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdReject() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "reject_failed"); got != want {
		t.Fatalf("reject failure text = %q, want %q", got, want)
	}

	snapshot := b.runtimeStats.snapshot()
	if len(snapshot.RecentErrors) == 0 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: moderation.manual_reject: reject lookup failed") {
		t.Fatalf("RecentErrors = %v, want moderation.manual_reject fault", snapshot.RecentErrors)
	}
}

func TestCmdResetAllVerifyReportsStoreFailures(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, &hookedStore{
		Store: base,
		clearUserVerificationStateHook: func(userID int64) error {
			return errors.New("clear failed")
		},
	}, client)

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/resetverify 42",
	}
	if err := b.cmdResetAllVerify(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdResetAllVerify() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "resetverify_failed"); got != want {
		t.Fatalf("resetverify failure text = %q, want %q", got, want)
	}

	snapshot := b.runtimeStats.snapshot()
	if len(snapshot.RecentErrors) == 0 || !strings.Contains(snapshot.RecentErrors[0], "internal fault: store.clear_user_verification_state: clear failed") {
		t.Fatalf("RecentErrors = %v, want store.clear_user_verification_state fault", snapshot.RecentErrors)
	}
}

func TestApproveUserResolvesPendingWithoutFallbackMutations(t *testing.T) {
	t.Parallel()

	store := mustNewSQLiteStore(t)
	b := newModerationFlowBot(t, store)

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-approve",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	if err := b.approveUser(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("approveUser() error = %v", err)
	}

	verified, err := store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after pending approval")
	}
	if gotPending, err := store.GetPending(pending.ChatID, pending.UserID); err != nil || gotPending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", gotPending, err)
	}
}
