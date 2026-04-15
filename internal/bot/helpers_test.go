package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type recordedBotRequest struct {
	method string
	params map[string]any
}

type botResponseFunc func(params map[string]any) (json.RawMessage, error)

type recordingBotClient struct {
	mu          sync.Mutex
	requests    []recordedBotRequest
	responders  map[string]botResponseFunc
	nextMessage int64
}

func (c *recordingBotClient) RequestWithContext(_ context.Context, _ string, method string, params map[string]any, _ *gotgbot.RequestOpts) (json.RawMessage, error) {
	c.mu.Lock()
	c.requests = append(c.requests, recordedBotRequest{
		method: method,
		params: cloneParams(params),
	})
	if c.nextMessage == 0 {
		c.nextMessage = 501
	}
	responder := c.responders[method]
	if responder == nil {
		switch method {
		case "sendMessage":
			messageID := c.nextMessage
			c.nextMessage++
			responder = func(params map[string]any) (json.RawMessage, error) {
				chatID := toInt64(params["chat_id"])
				return messageResponse(chatID, messageID), nil
			}
		case "deleteMessage", "banChatMember", "answerCallbackQuery":
			responder = func(params map[string]any) (json.RawMessage, error) {
				return json.RawMessage(`true`), nil
			}
		case "getChat":
			responder = func(params map[string]any) (json.RawMessage, error) {
				chatID := toInt64(params["chat_id"])
				return json.RawMessage(fmt.Sprintf(`{"id":%d,"type":"private","bio":"profile bio"}`, chatID)), nil
			}
		case "getChatMember":
			responder = func(params map[string]any) (json.RawMessage, error) {
				userID := toInt64(params["user_id"])
				return json.RawMessage(fmt.Sprintf(`{"status":"member","user":{"id":%d,"is_bot":false,"first_name":"Member"}}`, userID)), nil
			}
		case "editMessageText", "editMessageReplyMarkup":
			responder = func(params map[string]any) (json.RawMessage, error) {
				chatID := toInt64(params["chat_id"])
				messageID := toInt64(params["message_id"])
				return messageResponse(chatID, messageID), nil
			}
		}
	}
	c.mu.Unlock()

	if responder == nil {
		return nil, fmt.Errorf("unexpected bot method %q", method)
	}

	return responder(cloneParams(params))
}

func (c *recordingBotClient) SetResponder(method string, responder botResponseFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.responders == nil {
		c.responders = make(map[string]botResponseFunc)
	}
	c.responders[method] = responder
}

func (c *recordingBotClient) SetError(method string, err error) {
	c.SetResponder(method, func(params map[string]any) (json.RawMessage, error) {
		return nil, err
	})
}

func (c *recordingBotClient) RequestsByMethod(method string) []recordedBotRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	var requests []recordedBotRequest
	for _, request := range c.requests {
		if request.method != method {
			continue
		}
		requests = append(requests, recordedBotRequest{
			method: request.method,
			params: cloneParams(request.params),
		})
	}
	return requests
}

func (c *recordingBotClient) GetAPIURL(_ *gotgbot.RequestOpts) string {
	return "https://example.invalid"
}

func (c *recordingBotClient) FileURL(_, _ string, _ *gotgbot.RequestOpts) string {
	return ""
}

func (c *recordingBotClient) Requests() []recordedBotRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]recordedBotRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

func cloneParams(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}

	cloned := make(map[string]any, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	return cloned
}

func messageResponse(chatID, messageID int64) json.RawMessage {
	chatType := "private"
	if chatID < 0 {
		chatType = "supergroup"
	}

	return json.RawMessage(fmt.Sprintf(`{"message_id":%d,"date":0,"chat":{"id":%d,"type":"%s"}}`, messageID, chatID, chatType))
}

func toInt64(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func newRecordingTelegramBot(client *recordingBotClient) *gotgbot.Bot {
	return &gotgbot.Bot{
		Token: "123:test-token",
		User: gotgbot.User{
			Id:        123,
			IsBot:     true,
			FirstName: "Test Bot",
			Username:  "TestBot",
		},
		BotClient: client,
	}
}

func TestCancelUserTimersStopsTrackedTimers(t *testing.T) {
	t.Parallel()

	b := newModerationFlowBot(t, nil)
	fired := make(chan struct{}, 1)

	b.scheduleUserTimer(-100123, 42, 200*time.Millisecond, func() {
		fired <- struct{}{}
	})
	b.cancelUserTimers(-100123, 42)

	time.Sleep(50 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("tracked timer fired after cancelUserTimers()")
	default:
	}

	if len(b.timers) != 0 {
		t.Fatalf("timers map = %+v, want empty after cancelUserTimers()", b.timers)
	}
}

func TestIsBotAdmin(t *testing.T) {
	t.Parallel()

	b := &Bot{Config: &config.Config{Bot: config.BotConfig{Admins: []int64{1, 2, 3}}}}
	if !b.isBotAdmin(2) {
		t.Fatal("isBotAdmin() = false, want true for configured admin")
	}
	if b.isBotAdmin(9) {
		t.Fatal("isBotAdmin() = true, want false for unknown user")
	}
}

func TestIsBlocklistAdminPrivateUsesBotAdminList(t *testing.T) {
	t.Parallel()

	b := &Bot{Config: &config.Config{Bot: config.BotConfig{Admins: []int64{7}}}}
	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7},
	}

	if !b.isBlocklistAdmin(nil, msg) {
		t.Fatal("isBlocklistAdmin() = false, want true for private bot admin")
	}
}

func TestIsAnonymousGroupAdminMessage(t *testing.T) {
	t.Parallel()

	if !isAnonymousGroupAdminMessage(&gotgbot.Message{
		Chat:       gotgbot.Chat{Id: -100123, Type: "supergroup"},
		SenderChat: &gotgbot.Chat{Id: -100123},
	}) {
		t.Fatal("isAnonymousGroupAdminMessage() = false, want true for anonymous admin sender chat")
	}

	if isAnonymousGroupAdminMessage(&gotgbot.Message{
		Chat:       gotgbot.Chat{Id: -100123, Type: "private"},
		SenderChat: &gotgbot.Chat{Id: -100123},
	}) {
		t.Fatal("isAnonymousGroupAdminMessage() = true, want false outside group chats")
	}
}

func TestCanApproveFromMessageAllowsAnonymousAdmin(t *testing.T) {
	t.Parallel()

	b := &Bot{}
	msg := &gotgbot.Message{
		Chat:       gotgbot.Chat{Id: -100123, Type: "group"},
		SenderChat: &gotgbot.Chat{Id: -100123},
	}

	if !b.canApproveFromMessage(nil, msg) {
		t.Fatal("canApproveFromMessage() = false, want true for anonymous group admin message")
	}
}

func TestCommandActorLabel(t *testing.T) {
	t.Parallel()

	if got := commandActorLabel(nil); got != "unknown" {
		t.Fatalf("commandActorLabel(nil) = %q, want %q", got, "unknown")
	}
	if got := commandActorLabel(&gotgbot.Message{From: &gotgbot.User{Id: 42}}); got != "42" {
		t.Fatalf("commandActorLabel(user) = %q, want %q", got, "42")
	}
	if got := commandActorLabel(&gotgbot.Message{
		Chat:       gotgbot.Chat{Id: -100123, Type: "group"},
		SenderChat: &gotgbot.Chat{Id: -100123},
	}); got != "anonymous-admin" {
		t.Fatalf("commandActorLabel(anonymous admin) = %q, want %q", got, "anonymous-admin")
	}
}

func TestExtractTargetUserID(t *testing.T) {
	t.Parallel()

	msg := &gotgbot.Message{
		ReplyToMessage: &gotgbot.Message{From: &gotgbot.User{Id: 99}},
	}

	if got, err := extractTargetUserID("en", msg, []string{"42"}); err != nil || got != 42 {
		t.Fatalf("extractTargetUserID(args) = (%d, %v), want (42, nil)", got, err)
	}
	if got, err := extractTargetUserID("en", msg, nil); err != nil || got != 99 {
		t.Fatalf("extractTargetUserID(reply) = (%d, %v), want (99, nil)", got, err)
	}
	if _, err := extractTargetUserID("en", msg, []string{"bad"}); err == nil {
		t.Fatal("extractTargetUserID() error = nil, want invalid user id error")
	}
	if _, err := extractTargetUserID("en", &gotgbot.Message{}, nil); err == nil {
		t.Fatal("extractTargetUserID() error = nil, want missing target error")
	}
}

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	if got, want := escapeHTML("<a&b>"), "&lt;a&amp;b&gt;"; got != want {
		t.Fatalf("escapeHTML() = %q, want %q", got, want)
	}
}

func TestBuildCheckTextIncludesOptionalFields(t *testing.T) {
	t.Parallel()

	got := buildCheckText(&gotgbot.User{
		FirstName: "Alice",
		LastName:  "Bob",
		Username:  "alicebob",
	})
	if got != "Alice Bob alicebob" {
		t.Fatalf("buildCheckText() = %q, want %q", got, "Alice Bob alicebob")
	}
}

func TestMaskNameUsesFallbackForEmptyOrInvalidInput(t *testing.T) {
	t.Parallel()

	if got := maskName("Alice", "en"); got != "A**" {
		t.Fatalf("maskName() = %q, want %q", got, "A**")
	}
	if got := maskName("", "en"); got != "User" {
		t.Fatalf("maskName(empty) = %q, want %q", got, "User")
	}
	if got := maskName(string([]byte{0xff}), "zh-cn"); got != "用户" {
		t.Fatalf("maskName(invalid utf8) = %q, want %q", got, "用户")
	}
}

func TestNewVerificationRandomToken(t *testing.T) {
	t.Parallel()

	token, err := newVerificationRandomToken(32)
	if err != nil {
		t.Fatalf("newVerificationRandomToken() error = %v", err)
	}
	if len(token) != 32 {
		t.Fatalf("len(token) = %d, want 32", len(token))
	}
	for _, ch := range token {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", ch) {
			t.Fatalf("token contains unexpected character %q", ch)
		}
	}
	if _, err := newVerificationRandomToken(0); err == nil {
		t.Fatal("newVerificationRandomToken() error = nil, want invalid length error")
	}
}

func TestMatchUserAgainstBlacklistMatchesDisplayFields(t *testing.T) {
	t.Parallel()

	b := &Bot{Blacklist: blacklist.New()}
	b.Blacklist.Add("spam")

	got := b.matchUserAgainstBlacklist(nil, -100123, &gotgbot.User{
		FirstName: "Spam",
		LastName:  "Account",
	})
	if got != "spam" {
		t.Fatalf("matchUserAgainstBlacklist() = %q, want %q", got, "spam")
	}
}

func TestUserLanguageHelpersPreferPendingThenPreferenceThenDefault(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := &Bot{Store: store}
	if got := userLanguageFromPending(nil); got != "zh-cn" {
		t.Fatalf("userLanguageFromPending(nil) = %q, want %q", got, "zh-cn")
	}
	if got := userLanguageFromPending(&storepkg.PendingVerification{UserLanguage: "en-us"}); got != "en" {
		t.Fatalf("userLanguageFromPending() = %q, want %q", got, "en")
	}

	if err := store.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}
	if got := b.targetUserLanguage(-100123, 42); got != "en" {
		t.Fatalf("targetUserLanguage(preference) = %q, want %q", got, "en")
	}

	if err := store.SetPending(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "zh-cn",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if got := b.targetUserLanguage(-100123, 42); got != "zh-cn" {
		t.Fatalf("targetUserLanguage(pending) = %q, want %q", got, "zh-cn")
	}
	if got := b.targetUserLanguage(-100123, 99); got != "zh-cn" {
		t.Fatalf("targetUserLanguage(default) = %q, want %q", got, "zh-cn")
	}
}

func TestLocalizedLanguageName(t *testing.T) {
	t.Parallel()

	if got := localizedLanguageName("en", "zh-cn"); got != "English" {
		t.Fatalf("localizedLanguageName(en, zh-cn) = %q, want %q", got, "English")
	}
	if got := localizedLanguageName("zh-cn", "en"); got != "Simplified Chinese" {
		t.Fatalf("localizedLanguageName(zh-cn, en) = %q, want %q", got, "Simplified Chinese")
	}
}

func TestIsGetUpdatesDeadlineExceeded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "other error", err: fmt.Errorf("sendMessage failed: %w", context.DeadlineExceeded), want: false},
		{name: "wrapped deadline", err: fmt.Errorf("getUpdates failed: %w", context.DeadlineExceeded), want: true},
		{name: "plain string", err: fmt.Errorf("telegram getUpdates context deadline exceeded"), want: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isGetUpdatesDeadlineExceeded(tc.err); got != tc.want {
				t.Fatalf("isGetUpdatesDeadlineExceeded(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUpdaterErrorThrottlerHandleSuppressesRepeatedDeadlineLogs(t *testing.T) {
	client := bytes.NewBuffer(nil)
	origWriter := log.Writer()
	log.SetOutput(client)
	defer log.SetOutput(origWriter)

	throttler := newUpdaterErrorThrottler()
	err := fmt.Errorf("getUpdates failed: %w", context.DeadlineExceeded)

	throttler.Handle(err)
	firstLog := client.String()
	if !strings.Contains(firstLog, "failed to get updates") {
		t.Fatalf("first Handle() log = %q, want throttled deadline message", firstLog)
	}

	client.Reset()
	throttler.Handle(err)
	if client.Len() != 0 {
		t.Fatalf("second Handle() log = %q, want suppressed repeated deadline log", client.String())
	}

	client.Reset()
	throttler.Handle(io.EOF)
	if !strings.Contains(client.String(), "updater error") {
		t.Fatalf("non-deadline Handle() log = %q, want generic updater error log", client.String())
	}
}

func TestPendingForOriginalMessageDeletionSkipsVerifiedUsers(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := newModerationFlowBot(t, store)
	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Add(-time.Hour).Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 777,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if err := store.SetVerified(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	got, shouldPersist, err := b.pendingForOriginalMessageDeletion(&pending, false)
	if err != nil {
		t.Fatalf("pendingForOriginalMessageDeletion() error = %v", err)
	}
	if got != nil || shouldPersist {
		t.Fatalf("pendingForOriginalMessageDeletion() = (%+v, %v), want (nil, false)", got, shouldPersist)
	}
}

func TestDeletePendingOriginalMessagePersistsClearedOriginalMessage(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	client := &recordingBotClient{}
	b := newModerationFlowBot(t, store)
	b.Bot = newRecordingTelegramBot(client)

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Add(-time.Hour).Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 777,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	if err := b.deletePendingOriginalMessage(b.Bot, &pending, false); err != nil {
		t.Fatalf("deletePendingOriginalMessage() error = %v", err)
	}

	got, err := store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if got == nil || got.OriginalMessageID != 0 {
		t.Fatalf("GetPending() = %+v, want original_message_id cleared", got)
	}

	requests := client.Requests()
	if len(requests) != 1 || requests[0].method != "deleteMessage" {
		t.Fatalf("bot requests = %+v, want one deleteMessage request", requests)
	}
}

func TestApproveUserSetsVerifiedWhenNoPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := newModerationFlowBot(t, store)
	if err := store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	if err := b.approveUser(-100123, 42); err != nil {
		t.Fatalf("approveUser() error = %v", err)
	}

	verified, err := store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after approveUser")
	}
	rejected, err := store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if rejected {
		t.Fatal("IsRejected() = true, want false after approveUser")
	}
	warnings, err := store.GetWarningCount(-100123, 42)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 0 {
		t.Fatalf("GetWarningCount() = %d, want 0 after approveUser", warnings)
	}
}

func TestApproveUserFallsBackWhenPendingResolutionDoesNotMatch(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	b := newTestBot(t, &hookedStore{
		Store: base,
		resolvePendingByTokenHook: func(chatID, userID int64, timestamp int64, randomToken string, action storepkg.PendingAction, maxWarnings int) (storepkg.PendingResolutionResult, error) {
			return storepkg.PendingResolutionResult{Action: action}, nil
		},
	}, &recordingBotClient{})

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-fallback",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := b.Store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if err := b.Store.SetRejected(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := b.Store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	if err := b.approveUser(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("approveUser() error = %v", err)
	}

	verified, err := b.Store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want fallback approval to verify the user")
	}
}

func TestRejectUserResolvesPendingAndDeletesMessages(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	client := &recordingBotClient{}
	b := newModerationFlowBot(t, store)
	b.Bot = newRecordingTelegramBot(client)

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 7001,
		PrivateMessageID:  8001,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	if err := b.rejectUser(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("rejectUser() error = %v", err)
	}

	rejected, err := store.IsRejected(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if !rejected {
		t.Fatal("IsRejected() = false, want true after rejectUser")
	}
	if pending, err := store.GetPending(pending.ChatID, pending.UserID); err != nil || pending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", pending, err)
	}

	requests := client.Requests()
	deleteCount := 0
	for _, request := range requests {
		if request.method == "deleteMessage" {
			deleteCount++
		}
	}
	if deleteCount != 2 {
		t.Fatalf("deleteMessage request count = %d, want 2", deleteCount)
	}
}

func TestUnrejectUserClearsRejectedStateAndWarnings(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	b := newModerationFlowBot(t, store)
	if err := store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	if err := b.unrejectUser(-100123, 42); err != nil {
		t.Fatalf("unrejectUser() error = %v", err)
	}

	rejected, err := store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if rejected {
		t.Fatal("IsRejected() = true, want false after unrejectUser")
	}
	warnings, err := store.GetWarningCount(-100123, 42)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 0 {
		t.Fatalf("GetWarningCount() = %d, want 0 after unrejectUser", warnings)
	}
}

func TestOnVerifyWindowExpiredBansAfterMaxWarnings(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	client := &recordingBotClient{}
	b := newModerationFlowBot(t, store)
	b.Bot = newRecordingTelegramBot(client)

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		OriginalMessageID: 7001,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount(first) error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount(second) error = %v", err)
	}

	b.onVerifyWindowExpired(b.Bot, pending)

	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 3 {
		t.Fatalf("GetWarningCount() = %d, want 3 after expiry increments warning", warnings)
	}

	requests := client.Requests()
	var deleteCount, banCount int
	for _, request := range requests {
		switch request.method {
		case "deleteMessage":
			deleteCount++
		case "banChatMember":
			banCount++
		}
	}
	if deleteCount != 1 || banCount != 1 {
		t.Fatalf("bot requests = %+v, want one deleteMessage and one banChatMember", requests)
	}
}

func TestOnVerifyWindowExpiredStopsForUnmatchedAndVerifiedResults(t *testing.T) {
	t.Parallel()

	t.Run("unmatched", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newModerationFlowBot(t, &moderationFlowStoreStub{
			resolve: storepkg.PendingResolutionResult{Matched: false},
		})
		b.Bot = newRecordingTelegramBot(client)

		pending := storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a"}
		b.onVerifyWindowExpired(b.Bot, pending)

		if got := len(client.Requests()); got != 0 {
			t.Fatalf("bot requests = %+v, want none for unmatched expiry", client.Requests())
		}
	})

	t.Run("verified", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newModerationFlowBot(t, &moderationFlowStoreStub{
			resolve: storepkg.PendingResolutionResult{Matched: true, Verified: true},
		})
		b.Bot = newRecordingTelegramBot(client)

		pending := storepkg.PendingVerification{ChatID: -100123, UserID: 42, Timestamp: 1, RandomToken: "token-a"}
		b.onVerifyWindowExpired(b.Bot, pending)

		if got := len(client.Requests()); got != 0 {
			t.Fatalf("bot requests = %+v, want none for verified expiry", client.Requests())
		}
	})
}

func TestHandleVerificationSuccessSendsConfirmationAndClearsPending(t *testing.T) {
	t.Parallel()

	store, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	client := &recordingBotClient{}
	b := newModerationFlowBot(t, store)
	b.Bot = newRecordingTelegramBot(client)

	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		ReminderMessageID: 7001,
		PrivateMessageID:  8001,
	}
	if err := store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := store.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}
	b.scheduleUserTimer(pending.ChatID, pending.UserID, time.Hour, func() {})

	b.HandleVerificationSuccess(captcha.VerifiedToken{
		ChatID:      pending.ChatID,
		UserID:      pending.UserID,
		Timestamp:   pending.Timestamp,
		RandomToken: pending.RandomToken,
	})

	verified, err := store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after HandleVerificationSuccess")
	}
	if gotPending, err := store.GetPending(pending.ChatID, pending.UserID); err != nil || gotPending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", gotPending, err)
	}
	warnings, err := store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 0 {
		t.Fatalf("GetWarningCount() = %d, want 0 after HandleVerificationSuccess", warnings)
	}
	if len(b.timers) != 0 {
		t.Fatalf("timers map = %+v, want empty after HandleVerificationSuccess", b.timers)
	}

	requests := client.Requests()
	var deleteCount, sendCount int
	var lastSendText string
	for _, request := range requests {
		switch request.method {
		case "deleteMessage":
			deleteCount++
		case "sendMessage":
			sendCount++
			if text, ok := request.params["text"].(string); ok {
				lastSendText = text
			}
		}
	}
	if deleteCount != 2 || sendCount != 1 {
		t.Fatalf("bot requests = %+v, want two deleteMessage and one sendMessage", requests)
	}
	if !strings.Contains(lastSendText, "completed the human verification") {
		t.Fatalf("verification success text = %q, want English success message", lastSendText)
	}
}
