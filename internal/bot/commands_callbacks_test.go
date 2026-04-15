package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/captcha"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func requestText(request recordedBotRequest) string {
	if text, ok := request.params["text"].(string); ok {
		return text
	}
	return ""
}

func requestParamString(t *testing.T, request recordedBotRequest, key string) string {
	t.Helper()

	value, ok := request.params[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%q) error = %v", key, err)
	}
	return string(data)
}

func TestCmdAddBlocklistPrivateAddsWordAndPersists(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	msg := &gotgbot.Message{
		MessageId: 1001,
		Chat:      gotgbot.Chat{Id: 7, Type: "private"},
		From:      &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text:      "/addblocklist Spam<Tag>",
	}

	if err := b.cmdAddBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdAddBlocklist() error = %v", err)
	}

	if got := b.Blacklist.List(); len(got) != 1 || got[0] != "spam<tag>" {
		t.Fatalf("Blacklist.List() = %v, want [spam<tag>]", got)
	}

	words, err := b.Store.GetBlacklistWords(0)
	if err != nil {
		t.Fatalf("GetBlacklistWords() error = %v", err)
	}
	if len(words) != 1 || words[0] != "spam<tag>" {
		t.Fatalf("store blacklist words = %v, want [spam<tag>]", words)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if !strings.Contains(requestText(requests[0]), "Spam&lt;Tag&gt;") {
		t.Fatalf("reply text = %q, want HTML-escaped keyword", requestText(requests[0]))
	}
}

func TestCmdAddBlocklistShowsUsageWhenKeywordMissing(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/addblocklist",
	}

	if err := b.cmdAddBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdAddBlocklist() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "usage_addblocklist"); got != want {
		t.Fatalf("usage text = %q, want %q", got, want)
	}
}

func TestCmdAddBlocklistRollsBackOnPersistenceFailure(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, &hookedStore{
		Store: base,
		addBlacklistWordHook: func(chatID int64, word, addedBy string) error {
			return errors.New("boom")
		},
	}, client)

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/addblocklist spam",
	}
	if err := b.cmdAddBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdAddBlocklist() error = %v", err)
	}

	if got := b.Blacklist.List(); len(got) != 0 {
		t.Fatalf("Blacklist.List() = %v, want rollback to empty", got)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "blacklist_persist_failed"); got != want {
		t.Fatalf("failure text = %q, want %q", got, want)
	}
}

func TestCmdDelBlocklistRollsBackGroupWordOnPersistenceFailure(t *testing.T) {
	t.Parallel()

	base := mustNewSQLiteStore(t)
	client := &recordingBotClient{}
	b := newTestBot(t, &hookedStore{
		Store: base,
		removeBlacklistWordHook: func(chatID int64, word string) error {
			return errors.New("boom")
		},
	}, client)
	b.Blacklist.AddGroup(-100123, "spam")

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/delblocklist spam",
	}
	if err := b.cmdDelBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdDelBlocklist() error = %v", err)
	}

	if got := b.Blacklist.ListGroup(-100123); len(got) != 1 || got[0] != "spam" {
		t.Fatalf("Blacklist.ListGroup() = %v, want restored [spam]", got)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "blacklist_delete_failed"); got != want {
		t.Fatalf("failure text = %q, want %q", got, want)
	}
}

func TestCmdDelBlocklistReportsMissingWordInEachScope(t *testing.T) {
	t.Parallel()

	t.Run("global", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 7, Type: "private"},
			From: &gotgbot.User{Id: 7, LanguageCode: "en"},
			Text: "/delblocklist spam",
		}

		if err := b.cmdDelBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdDelBlocklist() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "blacklist_not_found_global"); got != want {
			t.Fatalf("missing-global text = %q, want %q", got, want)
		}
	})

	t.Run("group", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
			From: &gotgbot.User{Id: 7, LanguageCode: "en"},
			Text: "/delblocklist spam",
		}

		if err := b.cmdDelBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdDelBlocklist() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "blacklist_not_found_group"); got != want {
			t.Fatalf("missing-group text = %q, want %q", got, want)
		}
	})
}

func TestCmdListBlocklistRendersEscapedGroupWords(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	b.Blacklist.AddGroup(-100123, "spam<tag>")

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/listblocklist",
	}
	if err := b.cmdListBlocklist(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdListBlocklist() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if !strings.Contains(requestText(requests[0]), "<code>spam&lt;tag&gt;</code>") {
		t.Fatalf("list text = %q, want escaped rendered item", requestText(requests[0]))
	}
}

func TestCmdApproveUsesReplyTargetAndSendsConfirmation(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	if err := b.Store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := b.Store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

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

	verified, err := b.Store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after /approve")
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "approve_result", int64(42)); got != want {
		t.Fatalf("approve confirmation = %q, want %q", got, want)
	}
}

func TestCmdRejectUsesReplyTargetAndSendsConfirmation(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	if _, err := b.Store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}
	if err := b.Store.SetPending(storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Unix(),
		RandomToken:       "token-a",
		ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
		OriginalMessageID: 7001,
		PrivateMessageID:  8001,
	}); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

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

	rejected, err := b.Store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if !rejected {
		t.Fatal("IsRejected() = false, want true after /reject")
	}
	if pending, err := b.Store.GetPending(-100123, 42); err != nil || pending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", pending, err)
	}
	if warnings, err := b.Store.GetWarningCount(-100123, 42); err != nil || warnings != 0 {
		t.Fatalf("GetWarningCount() = (%d, %v), want (0, nil)", warnings, err)
	}

	if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
		t.Fatalf("deleteMessage request count = %d, want 2", got)
	}
	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "reject_result", int64(42)); got != want {
		t.Fatalf("reject confirmation = %q, want %q", got, want)
	}
}

func TestCmdApproveSuccessIgnoresConfirmationSendErrors(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	client.SetError("sendMessage", errors.New("send failed"))
	b := newTestBot(t, nil, client)
	if err := b.Store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/approve 42",
	}
	if err := b.cmdApprove(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdApprove() error = %v", err)
	}

	verified, err := b.Store.IsVerified(-100123, 42)
	if err != nil {
		t.Fatalf("IsVerified() error = %v", err)
	}
	if !verified {
		t.Fatal("IsVerified() = false, want true after /approve even when confirmation send fails")
	}
	if got := len(client.RequestsByMethod("deleteMessage")); got != 0 {
		t.Fatalf("deleteMessage request count = %d, want 0 when confirmation send fails", got)
	}
}

func TestCmdRejectSuccessIgnoresConfirmationSendErrors(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	client.SetError("sendMessage", errors.New("send failed"))
	b := newTestBot(t, nil, client)
	if err := b.Store.SetPending(storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "token-a",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/reject 42",
	}
	if err := b.cmdReject(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdReject() error = %v", err)
	}

	rejected, err := b.Store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if !rejected {
		t.Fatal("IsRejected() = false, want true after /reject even when confirmation send fails")
	}
	if got := len(client.RequestsByMethod("deleteMessage")); got != 0 {
		t.Fatalf("deleteMessage request count = %d, want 0 when confirmation send fails", got)
	}
}

func TestCmdUnrejectClearsRejectedStateAndWarnings(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	if err := b.Store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if _, err := b.Store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/unreject 42",
	}
	if err := b.cmdUnreject(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdUnreject() error = %v", err)
	}

	rejected, err := b.Store.IsRejected(-100123, 42)
	if err != nil {
		t.Fatalf("IsRejected() error = %v", err)
	}
	if rejected {
		t.Fatal("IsRejected() = true, want false after /unreject")
	}
	if warnings, err := b.Store.GetWarningCount(-100123, 42); err != nil || warnings != 0 {
		t.Fatalf("GetWarningCount() = (%d, %v), want (0, nil)", warnings, err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "unreject_result", int64(42)); got != want {
		t.Fatalf("unreject confirmation = %q, want %q", got, want)
	}
}

func TestCmdResetAllVerifyClearsStateAndCancelsTimers(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	if err := b.Store.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}
	if err := b.Store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}
	if err := b.Store.SetPending(storepkg.PendingVerification{
		ChatID:      -100123,
		UserID:      42,
		Timestamp:   time.Now().UTC().Unix(),
		RandomToken: "token-a",
		ExpireAt:    time.Now().UTC().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}
	if _, err := b.Store.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}
	b.scheduleUserTimer(-100123, 42, time.Hour, func() {})

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/resetverify 42",
	}
	if err := b.cmdResetAllVerify(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdResetAllVerify() error = %v", err)
	}

	if pending, err := b.Store.GetPending(-100123, 42); err != nil || pending != nil {
		t.Fatalf("GetPending() = (%+v, %v), want (nil, nil)", pending, err)
	}
	if warnings, err := b.Store.GetWarningCount(-100123, 42); err != nil || warnings != 0 {
		t.Fatalf("GetWarningCount() = (%d, %v), want (0, nil)", warnings, err)
	}
	if len(b.timers) != 0 {
		t.Fatalf("timers map = %+v, want empty after /resetverify", b.timers)
	}
}

func TestCmdResetAllVerifyShowsUsageWhenTargetIsMissing(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)

	msg := &gotgbot.Message{
		Chat: gotgbot.Chat{Id: 7, Type: "private"},
		From: &gotgbot.User{Id: 7, LanguageCode: "en"},
		Text: "/resetverify",
	}
	if err := b.cmdResetAllVerify(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("cmdResetAllVerify() error = %v", err)
	}

	requests := client.RequestsByMethod("sendMessage")
	if len(requests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(requests))
	}
	if got, want := requestText(requests[0]), tr("en", "resetverify_usage"); got != want {
		t.Fatalf("usage text = %q, want %q", got, want)
	}
}

func TestCmdLangHandlesPromptInvalidStoreFailureAndSuccess(t *testing.T) {
	t.Parallel()

	t.Run("prompt", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
			Text: "/lang",
		}
		if err := b.cmdLang(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdLang() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_prompt"); got != want {
			t.Fatalf("prompt text = %q, want %q", got, want)
		}
		if markup := requestParamString(t, requests[0], "reply_markup"); !strings.Contains(markup, BuildLanguagePreferenceCallbackData("en")) {
			t.Fatalf("reply_markup = %q, want language buttons", markup)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
			Text: "/lang fr",
		}
		if err := b.cmdLang(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdLang() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		want := tr("en", "lang_invalid") + "\n\n" + tr("en", "lang_prompt")
		if got := requestText(requests[0]); got != want {
			t.Fatalf("invalid text = %q, want %q", got, want)
		}
	})

	t.Run("store failure", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		b := newTestBot(t, &hookedStore{
			Store: base,
			setUserLanguagePreferenceHook: func(userID int64, language string) error {
				return errors.New("boom")
			},
		}, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
			Text: "/lang en",
		}
		if err := b.cmdLang(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdLang() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_update_failed"); got != want {
			t.Fatalf("store failure text = %q, want %q", got, want)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "zh-cn"},
			Text: "/lang en-us",
		}
		if err := b.cmdLang(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdLang() error = %v", err)
		}

		language, err := b.Store.GetUserLanguagePreference(42)
		if err != nil {
			t.Fatalf("GetUserLanguagePreference() error = %v", err)
		}
		if language != "en" {
			t.Fatalf("stored language = %q, want %q", language, "en")
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		want := tr("en", "lang_updated", localizedLanguageName("en", "en"))
		if got := requestText(requests[0]); got != want {
			t.Fatalf("success text = %q, want %q", got, want)
		}
	})
}

func TestCmdStatsBotAdminOnlyAndRendersRuntimeDetails(t *testing.T) {
	t.Parallel()

	t.Run("bot admin", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		b.Blacklist.Add("spam")
		if err := b.Store.SetPending(storepkg.PendingVerification{
			ChatID:      -100123,
			UserID:      42,
			Timestamp:   time.Now().UTC().Unix(),
			RandomToken: "token-a",
			ExpireAt:    time.Now().UTC().Add(5 * time.Minute),
		}); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}
		b.runtimeStats.recordError("unit test error")

		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 7, Type: "private"},
			From: &gotgbot.User{Id: 7, LanguageCode: "en"},
			Text: "/stats",
		}
		if err := b.cmdStats(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdStats() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		text := requestText(requests[0])
		if !strings.Contains(text, "Runtime Statistics") || !strings.Contains(text, "Pending backlog") || !strings.Contains(text, "Recent errors") {
			t.Fatalf("stats text = %q, want runtime diagnostics output", text)
		}
	})

	t.Run("group admin is not enough", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		client.SetResponder("getChatMember", func(params map[string]any) (json.RawMessage, error) {
			userID := toInt64(params["user_id"])
			return json.RawMessage(fmt.Sprintf(`{"status":"administrator","user":{"id":%d,"is_bot":false,"first_name":"Admin"}}`, userID)), nil
		})

		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: -100123, Type: "supergroup"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
			Text: "/stats",
		}
		if err := b.cmdStats(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdStats() error = %v", err)
		}

		if got := len(client.RequestsByMethod("sendMessage")); got != 0 {
			t.Fatalf("sendMessage request count = %d, want 0 for non-bot-admin", got)
		}
	})
}

func TestCmdStartAndHandleVerificationStartScenarios(t *testing.T) {
	t.Parallel()

	t.Run("start help", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
			Text: "/start",
		}

		if err := b.cmdStart(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
			t.Fatalf("cmdStart() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "start_help"); got != want {
			t.Fatalf("help text = %q, want %q", got, want)
		}
	})

	t.Run("invalid payload", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
		}

		if err := b.handleVerificationStart(b.Bot, msg, "bad-payload"); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "verify_params_invalid"); got != want {
			t.Fatalf("invalid payload text = %q, want %q", got, want)
		}
	})

	t.Run("wrong user", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 41, Type: "private"},
			From: &gotgbot.User{Id: 41, LanguageCode: "en"},
		}

		payload := BuildVerificationStartPayload(-100123, 42, 7001)
		if err := b.handleVerificationStart(b.Bot, msg, payload); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "verify_link_not_yours"); got != want {
			t.Fatalf("wrong-user text = %q, want %q", got, want)
		}
	})

	t.Run("expired pending", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
		}
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Add(-time.Minute).Unix(),
			RandomToken:       "token-a",
			ExpireAt:          time.Now().UTC().Add(-time.Second),
			ReminderMessageID: 7001,
		}
		if err := b.Store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}

		payload := BuildVerificationStartPayload(pending.ChatID, pending.UserID, pending.ReminderMessageID)
		if err := b.handleVerificationStart(b.Bot, msg, payload); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "verify_link_expired"); got != want {
			t.Fatalf("expired text = %q, want %q", got, want)
		}
	})

	t.Run("blacklist hit cancels and bans", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		b.Blacklist.Add("spam")
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en", FirstName: "Spam"},
		}
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Unix(),
			RandomToken:       "token-a",
			ExpireAt:          time.Now().UTC().Add(5 * time.Minute),
			ReminderMessageID: 7001,
			OriginalMessageID: 8001,
		}
		if err := b.Store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}

		payload := BuildVerificationStartPayload(pending.ChatID, pending.UserID, pending.ReminderMessageID)
		if err := b.handleVerificationStart(b.Bot, msg, payload); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		if got, err := b.Store.GetPending(pending.ChatID, pending.UserID); err != nil || got != nil {
			t.Fatalf("GetPending() = (%+v, %v), want (nil, nil) after blacklist cancel", got, err)
		}

		if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
			t.Fatalf("deleteMessage request count = %d, want 2", got)
		}
		if got := len(client.RequestsByMethod("banChatMember")); got != 1 {
			t.Fatalf("banChatMember request count = %d, want 1", got)
		}
	})

	t.Run("captcha disabled", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
		}
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Unix(),
			RandomToken:       "token-a",
			ExpireAt:          time.Now().UTC().Add(5 * time.Minute),
			ReminderMessageID: 7001,
		}
		if err := b.Store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}

		payload := BuildVerificationStartPayload(pending.ChatID, pending.UserID, pending.ReminderMessageID)
		if err := b.handleVerificationStart(b.Bot, msg, payload); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		requests := client.RequestsByMethod("sendMessage")
		if len(requests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "verify_service_disabled"); got != want {
			t.Fatalf("captcha-disabled text = %q, want %q", got, want)
		}
	})

	t.Run("success updates private message metadata", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		b.Captcha = captcha.NewServer(&b.Config.Turnstile, b.Store, b.Config.Moderation.GetVerifyWindow(), b.Bot.Token, nil)
		msg := &gotgbot.Message{
			Chat: gotgbot.Chat{Id: 42, Type: "private"},
			From: &gotgbot.User{Id: 42, LanguageCode: "en"},
		}
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "zh-cn",
			Timestamp:         time.Now().UTC().Unix(),
			RandomToken:       "token-a",
			ExpireAt:          time.Now().UTC().Add(5 * time.Minute),
			ReminderMessageID: 7001,
			PrivateMessageID:  9001,
		}
		if err := b.Store.SetPending(pending); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}

		payload := BuildVerificationStartPayload(pending.ChatID, pending.UserID, pending.ReminderMessageID)
		if err := b.handleVerificationStart(b.Bot, msg, payload); err != nil {
			t.Fatalf("handleVerificationStart() error = %v", err)
		}

		updated, err := b.Store.GetPending(pending.ChatID, pending.UserID)
		if err != nil {
			t.Fatalf("GetPending() error = %v", err)
		}
		if updated == nil || updated.PrivateMessageID == 0 || updated.UserLanguage != "en" {
			t.Fatalf("updated pending = %+v, want private message id + request language", updated)
		}

		deleteRequests := client.RequestsByMethod("deleteMessage")
		if len(deleteRequests) != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1 for previous private message", len(deleteRequests))
		}
		sendRequests := client.RequestsByMethod("sendMessage")
		if len(sendRequests) != 1 {
			t.Fatalf("sendMessage request count = %d, want 1", len(sendRequests))
		}
		if markup := requestParamString(t, sendRequests[0], "reply_markup"); !strings.Contains(markup, "https://verify.example.com/verify?") {
			t.Fatalf("reply_markup = %q, want verify URL button", markup)
		}
	})
}

func TestHandleModerationCallbackScenarios(t *testing.T) {
	t.Parallel()

	t.Run("invalid data", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-1",
			From: gotgbot.User{Id: 42, LanguageCode: "en"},
			Data: "bad",
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "invalid_review_button"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("non admin", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-2",
			From: gotgbot.User{Id: 42, LanguageCode: "en"},
			Data: BuildModerationCallbackData("a", -100123, 50),
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "admin_only_button"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("approve success edits message", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-3",
			From: gotgbot.User{Id: 7, LanguageCode: "en"},
			Data: BuildModerationCallbackData("a", -100123, 42),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
			},
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		verified, err := b.Store.IsVerified(-100123, 42)
		if err != nil {
			t.Fatalf("IsVerified() error = %v", err)
		}
		if !verified {
			t.Fatal("IsVerified() = false, want true after approve callback")
		}

		if got := len(client.RequestsByMethod("editMessageText")); got != 1 {
			t.Fatalf("editMessageText request count = %d, want 1", got)
		}
		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "callback_answer_approved"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("reject success edits message", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		if err := b.Store.SetPending(storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Unix(),
			RandomToken:       "token-a",
			ExpireAt:          time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
			OriginalMessageID: 7001,
			PrivateMessageID:  8001,
		}); err != nil {
			t.Fatalf("SetPending() error = %v", err)
		}

		cq := &gotgbot.CallbackQuery{
			Id:   "cb-4",
			From: gotgbot.User{Id: 7, LanguageCode: "en"},
			Data: BuildModerationCallbackData("r", -100123, 42),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
			},
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		rejected, err := b.Store.IsRejected(-100123, 42)
		if err != nil {
			t.Fatalf("IsRejected() error = %v", err)
		}
		if !rejected {
			t.Fatal("IsRejected() = false, want true after reject callback")
		}
		if got := len(client.RequestsByMethod("deleteMessage")); got != 2 {
			t.Fatalf("deleteMessage request count = %d, want 2", got)
		}
		if got := len(client.RequestsByMethod("editMessageText")); got != 1 {
			t.Fatalf("editMessageText request count = %d, want 1", got)
		}
		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "callback_answer_rejected"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("chat mismatch", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-5",
			From: gotgbot.User{Id: 7, LanguageCode: "en"},
			Data: BuildModerationCallbackData("a", -100123, 42),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: -100999, Type: "supergroup"},
			},
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "button_chat_mismatch"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("approve failure", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		b := newTestBot(t, &hookedStore{
			Store: base,
			getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
				return nil, errors.New("boom")
			},
		}, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-6",
			From: gotgbot.User{Id: 7, LanguageCode: "en"},
			Data: BuildModerationCallbackData("a", -100123, 42),
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "callback_approve_failed"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
		if got := len(client.RequestsByMethod("editMessageText")); got != 0 {
			t.Fatalf("editMessageText request count = %d, want 0", got)
		}
	})

	t.Run("reject failure", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		b := newTestBot(t, &hookedStore{
			Store: base,
			getPendingHook: func(chatID, userID int64) (*storepkg.PendingVerification, error) {
				return nil, errors.New("boom")
			},
		}, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "cb-7",
			From: gotgbot.User{Id: 7, LanguageCode: "en"},
			Data: BuildModerationCallbackData("r", -100123, 42),
		}

		if err := b.handleModerationCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleModerationCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "callback_reject_failed"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
		if got := len(client.RequestsByMethod("editMessageText")); got != 0 {
			t.Fatalf("editMessageText request count = %d, want 0", got)
		}
	})
}

func TestHandleLanguagePreferenceCallbackScenarios(t *testing.T) {
	t.Parallel()

	t.Run("private only", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "lang-1",
			From: gotgbot.User{Id: 42, LanguageCode: "en"},
			Data: BuildLanguagePreferenceCallbackData("en"),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
			},
		}

		if err := b.handleLanguagePreferenceCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleLanguagePreferenceCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_private_only"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("invalid data", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "lang-2",
			From: gotgbot.User{Id: 42, LanguageCode: "en"},
			Data: "lang:fr",
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: 42, Type: "private"},
			},
		}

		if err := b.handleLanguagePreferenceCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleLanguagePreferenceCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_invalid"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("store failure", func(t *testing.T) {
		t.Parallel()

		base := mustNewSQLiteStore(t)
		client := &recordingBotClient{}
		b := newTestBot(t, &hookedStore{
			Store: base,
			setUserLanguagePreferenceHook: func(userID int64, language string) error {
				return errors.New("boom")
			},
		}, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "lang-3",
			From: gotgbot.User{Id: 42, LanguageCode: "en"},
			Data: BuildLanguagePreferenceCallbackData("en"),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: 42, Type: "private"},
			},
		}

		if err := b.handleLanguagePreferenceCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleLanguagePreferenceCallback() error = %v", err)
		}

		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_update_failed"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "lang-4",
			From: gotgbot.User{Id: 42, LanguageCode: "zh-cn"},
			Data: BuildLanguagePreferenceCallbackData("en"),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: 42, Type: "private"},
			},
		}

		if err := b.handleLanguagePreferenceCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleLanguagePreferenceCallback() error = %v", err)
		}

		language, err := b.Store.GetUserLanguagePreference(42)
		if err != nil {
			t.Fatalf("GetUserLanguagePreference() error = %v", err)
		}
		if language != "en" {
			t.Fatalf("stored language = %q, want %q", language, "en")
		}
		if got := len(client.RequestsByMethod("editMessageText")); got != 1 {
			t.Fatalf("editMessageText request count = %d, want 1", got)
		}
		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_callback_updated"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})

	t.Run("edit failure still answers success", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetError("editMessageText", errors.New("boom"))
		b := newTestBot(t, nil, client)
		cq := &gotgbot.CallbackQuery{
			Id:   "lang-5",
			From: gotgbot.User{Id: 42, LanguageCode: "zh-cn"},
			Data: BuildLanguagePreferenceCallbackData("en"),
			Message: &gotgbot.Message{
				MessageId: 9001,
				Chat:      gotgbot.Chat{Id: 42, Type: "private"},
			},
		}

		if err := b.handleLanguagePreferenceCallback(b.Bot, newCallbackContext(b.Bot, cq)); err != nil {
			t.Fatalf("handleLanguagePreferenceCallback() error = %v", err)
		}

		language, err := b.Store.GetUserLanguagePreference(42)
		if err != nil {
			t.Fatalf("GetUserLanguagePreference() error = %v", err)
		}
		if language != "en" {
			t.Fatalf("stored language = %q, want %q", language, "en")
		}
		if got := len(client.RequestsByMethod("editMessageText")); got != 1 {
			t.Fatalf("editMessageText request count = %d, want 1", got)
		}
		requests := client.RequestsByMethod("answerCallbackQuery")
		if len(requests) != 1 {
			t.Fatalf("answerCallbackQuery request count = %d, want 1", len(requests))
		}
		if got, want := requestText(requests[0]), tr("en", "lang_callback_updated"); got != want {
			t.Fatalf("callback answer = %q, want %q", got, want)
		}
	})
}

func mustNewSQLiteStore(t *testing.T) storepkg.Store {
	t.Helper()

	store, err := storepkg.NewSQLiteStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
