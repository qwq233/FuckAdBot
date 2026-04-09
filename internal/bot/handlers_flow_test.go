package bot

import (
	"errors"
	"strings"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func TestHandleMessageCreatesVerificationReminderForUnverifiedUser(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	msg := &gotgbot.Message{
		MessageId:       1001,
		MessageThreadId: 7,
		Chat:            gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From:            &gotgbot.User{Id: 42, FirstName: "Alice", LanguageCode: "en"},
		Text:            "hello",
	}
	t.Cleanup(func() { b.cancelUserTimers(msg.Chat.Id, msg.From.Id) })

	if err := b.handleMessage(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	pending, err := b.Store.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending == nil {
		t.Fatal("GetPending() = nil, want active verification record")
	}
	if pending.ReminderMessageID == 0 || pending.OriginalMessageID != msg.MessageId || pending.MessageThreadID != msg.MessageThreadId {
		t.Fatalf("pending = %+v, want reminder/original/thread metadata persisted", pending)
	}

	sendRequests := client.RequestsByMethod("sendMessage")
	if len(sendRequests) != 1 {
		t.Fatalf("sendMessage request count = %d, want 1", len(sendRequests))
	}
	if got := requestText(sendRequests[0]); !strings.Contains(got, "A**") {
		t.Fatalf("reminder text = %q, want masked name", got)
	}
	if got := len(client.RequestsByMethod("editMessageReplyMarkup")); got != 1 {
		t.Fatalf("editMessageReplyMarkup request count = %d, want 1", got)
	}
}

func TestHandleMessageDeletesRejectedUserMessage(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	if err := b.Store.SetRejected(-100123, 42); err != nil {
		t.Fatalf("SetRejected() error = %v", err)
	}

	msg := &gotgbot.Message{
		MessageId: 1001,
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From:      &gotgbot.User{Id: 42, LanguageCode: "en"},
		Text:      "hello",
	}
	if err := b.handleMessage(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
		t.Fatalf("deleteMessage request count = %d, want 1", got)
	}
	if got := len(client.RequestsByMethod("sendMessage")); got != 0 {
		t.Fatalf("sendMessage request count = %d, want 0 for rejected user", got)
	}
}

func TestHandleMessageBlacklistHitBansMatchedUser(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	b.Blacklist.Add("spam")

	msg := &gotgbot.Message{
		MessageId: 1001,
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		From:      &gotgbot.User{Id: 42, FirstName: "Spammer", LanguageCode: "en"},
		Text:      "hello",
	}
	if err := b.handleMessage(b.Bot, newMessageContext(b.Bot, msg)); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got := len(client.RequestsByMethod("getChatMember")); got != 1 {
		t.Fatalf("getChatMember request count = %d, want 1 admin check", got)
	}
	if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
		t.Fatalf("deleteMessage request count = %d, want 1", got)
	}
	if got := len(client.RequestsByMethod("banChatMember")); got != 1 {
		t.Fatalf("banChatMember request count = %d, want 1", got)
	}
}

func TestMessageCleanupHelpersHandleExpectedErrors(t *testing.T) {
	t.Parallel()

	t.Run("delete ignores not found", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetError("deleteMessage", errors.New("Bad Request: message to delete not found"))
		bot := newRecordingTelegramBot(client)

		deleteMessageIfExists(bot, -100123, 1001, "cleanup")

		if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
			t.Fatalf("deleteMessage request count = %d, want 1", got)
		}
	})

	t.Run("ban returns false on error", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetError("banChatMember", errors.New("boom"))
		bot := newRecordingTelegramBot(client)

		if ok := banChatMember(bot, -100123, 42, "cleanup"); ok {
			t.Fatal("banChatMember() = true, want false on request error")
		}
	})

	t.Run("send helper surfaces error", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetError("sendMessage", errors.New("boom"))
		bot := newRecordingTelegramBot(client)

		if msg, err := sendMessageWithLog(bot, 42, "hello", nil, "cleanup"); err == nil || msg != nil {
			t.Fatalf("sendMessageWithLog() = (%+v, %v), want (nil, error)", msg, err)
		}
	})

	t.Run("edit helper returns false on error", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		client.SetError("editMessageText", errors.New("boom"))
		bot := newRecordingTelegramBot(client)
		message := &gotgbot.Message{
			MessageId: 1001,
			Chat:      gotgbot.Chat{Id: 42, Type: "private"},
		}

		if ok := editTextWithLog(bot, message, "updated", nil, "cleanup"); ok {
			t.Fatal("editTextWithLog() = true, want false on request error")
		}
	})
}
