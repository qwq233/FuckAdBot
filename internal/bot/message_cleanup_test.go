package bot

import (
	"errors"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func TestScheduleMessageDeletionSkipsInvalidInputs(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	bot := newRecordingTelegramBot(client)

	scheduleMessageDeletion(nil, 42, 99, 10*time.Millisecond, "nil bot")
	scheduleMessageDeletion(bot, 0, 99, 10*time.Millisecond, "zero chat")
	scheduleMessageDeletion(bot, 42, 0, 10*time.Millisecond, "zero message")
	scheduleMessageDeletion(bot, 42, 99, 0, "zero delay")

	time.Sleep(40 * time.Millisecond)
	if got := len(client.Requests()); got != 0 {
		t.Fatalf("bot requests = %d, want 0 for invalid scheduleMessageDeletion inputs", got)
	}
}

func TestScheduleMessageDeletionDeletesMessageAfterDelay(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	bot := newRecordingTelegramBot(client)

	scheduleMessageDeletion(bot, 42, 99, 10*time.Millisecond, "cleanup")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		requests := client.RequestsByMethod("deleteMessage")
		if len(requests) == 1 {
			if got := toInt64(requests[0].params["chat_id"]); got != 42 {
				t.Fatalf("deleteMessage chat_id = %d, want 42", got)
			}
			if got := toInt64(requests[0].params["message_id"]); got != 99 {
				t.Fatalf("deleteMessage message_id = %d, want 99", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("deleteMessage request was not observed before timeout")
}

func TestDeleteMessageIfExistsIgnoresKnownMissingMessageError(t *testing.T) {
	t.Parallel()

	client := &recordingBotClient{}
	client.SetError("deleteMessage", errors.New("Bad Request: message to delete not found"))
	bot := newRecordingTelegramBot(client)

	deleteMessageIfExists(bot, 42, 99, "cleanup")

	if got := len(client.RequestsByMethod("deleteMessage")); got != 1 {
		t.Fatalf("deleteMessage request count = %d, want 1", got)
	}
}

func TestEditReplyMarkupWithLogHandlesNilAndSuccess(t *testing.T) {
	t.Parallel()

	t.Run("nil message", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		bot := newRecordingTelegramBot(client)

		editReplyMarkupWithLog(bot, nil, nil, "nil")

		if got := len(client.Requests()); got != 0 {
			t.Fatalf("bot requests = %d, want 0 for nil message", got)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client := &recordingBotClient{}
		bot := newRecordingTelegramBot(client)
		message := &gotgbot.Message{
			MessageId: 99,
			Chat:      gotgbot.Chat{Id: 42, Type: "private"},
		}

		editReplyMarkupWithLog(bot, message, nil, "success")

		if got := len(client.RequestsByMethod("editMessageReplyMarkup")); got != 1 {
			t.Fatalf("editMessageReplyMarkup request count = %d, want 1", got)
		}
	})
}
