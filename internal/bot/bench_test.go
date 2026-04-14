package bot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

func newBenchmarkBot(b *testing.B, client *recordingBotClient) *Bot {
	b.Helper()

	if client == nil {
		client = &recordingBotClient{}
	}

	st, err := storepkg.NewSQLiteStore(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("NewSQLiteStore() error = %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	botInstance := &Bot{
		Bot:       newRecordingTelegramBot(client),
		Config:    newTestConfig(),
		Store:     st,
		Blacklist: blacklist.New(),
		timers:    make(map[timerKey][]*time.Timer),
	}
	botInstance.ensureRuntimeState()
	return botInstance
}

func BenchmarkIsGroupAdmin(b *testing.B) {
	b.Run("cache_hit", func(b *testing.B) {
		client := &recordingBotClient{}
		botInstance := newBenchmarkBot(b, client)
		botInstance.cache.setAdminStatus(-100123, 42, true, time.Now().UTC().Add(time.Minute))

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if !botInstance.isGroupAdmin(botInstance.Bot, -100123, 42) {
				b.Fatal("isGroupAdmin() = false, want cached true")
			}
		}
	})

	b.Run("cache_miss", func(b *testing.B) {
		client := &recordingBotClient{}
		client.SetResponder("getChatMember", func(params map[string]any) (json.RawMessage, error) {
			userID := toInt64(params["user_id"])
			return json.RawMessage(fmt.Sprintf(`{"status":"administrator","user":{"id":%d,"is_bot":false,"first_name":"Admin"}}`, userID)), nil
		})
		botInstance := newBenchmarkBot(b, client)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if !botInstance.isGroupAdmin(botInstance.Bot, -100123, int64(i+1)) {
				b.Fatal("isGroupAdmin() = false, want true on miss path")
			}
		}
	})
}

func BenchmarkMatchUserAgainstBlacklist(b *testing.B) {
	b.Run("username_only", func(b *testing.B) {
		botInstance := newBenchmarkBot(b, nil)
		botInstance.Blacklist.Add("spam")
		user := &gotgbot.User{Id: 42, FirstName: "Alice", Username: "spam_user"}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = botInstance.matchUserAgainstBlacklist(botInstance.Bot, -100123, user)
		}
	})

	b.Run("cached_bio", func(b *testing.B) {
		botInstance := newBenchmarkBot(b, nil)
		botInstance.Blacklist.Add("promo")
		user := &gotgbot.User{Id: 42, FirstName: "Alice", Username: "alice"}
		botInstance.cache.setUserChat(user.Id, &gotgbot.ChatFullInfo{Bio: "promo content"}, time.Now().UTC().Add(time.Minute))

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = botInstance.matchUserAgainstBlacklist(botInstance.Bot, -100123, user)
		}
	})
}

func BenchmarkHandleVerificationRequiredMessage(b *testing.B) {
	b.Run("fresh_pending", func(b *testing.B) {
		client := &recordingBotClient{}
		botInstance := newBenchmarkBot(b, client)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			incoming := &moderatedMessage{
				message: &gotgbot.Message{
					MessageId: int64(1000 + i),
					Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
					From:      &gotgbot.User{Id: int64(i + 1), FirstName: "Alice", LanguageCode: "en"},
					Text:      "hello",
				},
				user:         &gotgbot.User{Id: int64(i + 1), FirstName: "Alice", LanguageCode: "en"},
				chatID:       -100123,
				userLanguage: "en",
				verifyWindow: 5 * time.Minute,
				maxWarnings:  3,
			}
			botInstance.handleVerificationRequiredMessage(botInstance.Bot, incoming)
		}
	})

	b.Run("active_pending", func(b *testing.B) {
		client := &recordingBotClient{}
		botInstance := newBenchmarkBot(b, client)
		pending := storepkg.PendingVerification{
			ChatID:            -100123,
			UserID:            42,
			UserLanguage:      "en",
			Timestamp:         time.Now().UTC().Unix(),
			RandomToken:       "bench-active",
			ExpireAt:          time.Now().UTC().Add(5 * time.Minute),
			OriginalMessageID: 9001,
		}
		if err := botInstance.Store.SetPending(pending); err != nil {
			b.Fatalf("SetPending() error = %v", err)
		}

		incoming := &moderatedMessage{
			message: &gotgbot.Message{
				MessageId: 8001,
				Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
				From:      &gotgbot.User{Id: 42, FirstName: "Alice", LanguageCode: "en"},
				Text:      "hello",
			},
			user:         &gotgbot.User{Id: 42, FirstName: "Alice", LanguageCode: "en"},
			chatID:       -100123,
			userLanguage: "en",
			verifyWindow: 5 * time.Minute,
			maxWarnings:  3,
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			botInstance.handleVerificationRequiredMessage(botInstance.Bot, incoming)
		}
	})
}

func BenchmarkDiagnosticsRendering(b *testing.B) {
	botInstance := newBenchmarkBot(b, nil)
	botInstance.runtimeStats.recordError("bench")
	botInstance.Blacklist.Add("spam")

	cfg := &config.Config{
		Bot: config.BotConfig{
			Admins: []int64{7},
		},
		Moderation: config.ModerationConfig{
			MaxWarnings:        3,
			ReminderTTL:        30,
			VerifyWindow:       "5m",
			OriginalMessageTTL: "1m",
		},
	}
	botInstance.Config = cfg

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = botInstance.renderStatsReply("en")
	}
}
