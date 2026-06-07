package bot

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type noopBotClient struct{}

func (noopBotClient) RequestWithContext(context.Context, string, string, map[string]any, *gotgbot.RequestOpts) (json.RawMessage, error) {
	return nil, errors.New("unexpected telegram api request")
}

func (noopBotClient) GetAPIURL(*gotgbot.RequestOpts) string { return "" }

func (noopBotClient) FileURL(string, string, *gotgbot.RequestOpts) string { return "" }

type updateMetadataFailStore struct {
	*storepkg.SQLiteStore
}

func (s updateMetadataFailStore) UpdatePendingMetadataByToken(storepkg.PendingVerification) (bool, error) {
	return false, errors.New("forced metadata update failure")
}

func newRiskTestBot(t *testing.T) (*Bot, *storepkg.SQLiteStore, *[]int64, *[]int64) {
	t.Helper()

	st, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	deleted := make([]int64, 0)
	banned := make([]int64, 0)
	b := &Bot{
		Bot: &gotgbot.Bot{
			Token:     "1:test",
			User:      gotgbot.User{Id: 1, IsBot: true, Username: "testbot"},
			BotClient: noopBotClient{},
		},
		Config: &config.Config{
			Moderation: config.ModerationConfig{
				MaxWarnings:        3,
				ReminderTTL:        1,
				VerifyWindow:       "30ms",
				OriginalMessageTTL: "150s",
			},
		},
		Store:     st,
		Blacklist: blacklist.New(),
		timers:    make(map[timerKey][]*time.Timer),
		deleteMessage: func(_ *gotgbot.Bot, _ int64, messageID int64) error {
			deleted = append(deleted, messageID)
			return nil
		},
		banMember: func(_ *gotgbot.Bot, _ int64, userID int64) error {
			banned = append(banned, userID)
			return nil
		},
		sendMessage: func(_ *gotgbot.Bot, chatID int64, text string, opts *gotgbot.SendMessageOpts) (*gotgbot.Message, error) {
			return &gotgbot.Message{
				MessageId:       9001,
				MessageThreadId: opts.MessageThreadId,
				Chat:            gotgbot.Chat{Id: chatID, Type: "supergroup"},
				Text:            text,
			}, nil
		},
		getChatMember: func(_ *gotgbot.Bot, _ int64, _ int64) (gotgbot.ChatMember, error) {
			return gotgbot.ChatMemberMember{}, nil
		},
		getChat: func(_ *gotgbot.Bot, _ int64) (*gotgbot.ChatFullInfo, error) {
			return &gotgbot.ChatFullInfo{}, nil
		},
	}

	return b, st, &deleted, &banned
}

func TestDetectMessageRiskCoversTMeInlineAndGuestBot(t *testing.T) {
	t.Parallel()

	tme := gotgbot.Message{
		Text: "join",
		Entities: []gotgbot.MessageEntity{{
			Type: "text_link",
			Url:  "https://T.ME/spam",
		}},
	}
	if risk := detectMessageRisk(&tme); risk.kind != storepkg.PendingRiskTMe {
		t.Fatalf("detectMessageRisk(t.me) = %+v, want tme risk", risk)
	}

	inline := gotgbot.Message{ViaBot: &gotgbot.User{Id: 100, IsBot: true}}
	if risk := detectMessageRisk(&inline); risk.kind != storepkg.PendingRiskInline {
		t.Fatalf("detectMessageRisk(inline) = %+v, want inline risk", risk)
	}

	guest := gotgbot.Message{
		From:               &gotgbot.User{Id: 300, IsBot: true},
		GuestBotCallerUser: &gotgbot.User{Id: 42},
	}
	risk := detectMessageRisk(&guest)
	if risk.kind != storepkg.PendingRiskGuestBot || len(risk.guestBotUserIDs) != 1 || risk.guestBotUserIDs[0] != 300 {
		t.Fatalf("detectMessageRisk(guestbot) = %+v, want guestbot risk with guest bot id", risk)
	}
}

func TestRiskMessageCreatesOneChancePendingAndDeletesOriginal(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)

	msg := &gotgbot.Message{
		MessageId: 77,
		From:      &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:      "https://t.me/spam",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*deleted) != 1 || (*deleted)[0] != 77 {
		t.Fatalf("deleted messages = %v, want [77]", *deleted)
	}
	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none before verify timeout", *banned)
	}

	pending, err := st.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending == nil {
		t.Fatal("GetPending() = nil, want risk pending")
	}
	if pending.RiskKind != storepkg.PendingRiskTMe || pending.OriginalMessageID != 0 {
		t.Fatalf("pending = %+v, want t.me risk and original marked deleted", *pending)
	}
}

func TestRiskPendingExpiryBansUserAndLinkedGuestBot(t *testing.T) {
	b, st, _, banned := newRiskTestBot(t)

	pending := storepkg.PendingVerification{
		ChatID:          -100123,
		UserID:          42,
		UserLanguage:    "en",
		Timestamp:       time.Now().UTC().Unix(),
		RandomToken:     "riskexp",
		ExpireAt:        time.Now().UTC().Add(-time.Second),
		RiskKind:        storepkg.PendingRiskGuestBot,
		GuestBotUserIDs: []int64{300},
	}
	if err := st.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	b.onVerifyWindowExpired(b.Bot, pending)

	if len(*banned) != 2 || (*banned)[0] != 42 || (*banned)[1] != 300 {
		t.Fatalf("banned users = %v, want [42 300]", *banned)
	}
}

func TestGuestBotMessageDuringPendingIsDeletedAndLinked(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)

	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    time.Now().UTC().Unix(),
		RandomToken:  "active1",
		ExpireAt:     time.Now().UTC().Add(time.Minute),
	}
	if err := st.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	msg := &gotgbot.Message{
		MessageId:          88,
		From:               &gotgbot.User{Id: 300, IsBot: true},
		GuestBotCallerUser: &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:               gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:               "guestbot response",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*deleted) != 1 || (*deleted)[0] != 88 {
		t.Fatalf("deleted messages = %v, want [88]", *deleted)
	}
	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none before expiry", *banned)
	}

	got, err := st.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetPending() = nil, want active pending")
	}
	if got.RiskKind != storepkg.PendingRiskGuestBot || len(got.GuestBotUserIDs) != 1 || got.GuestBotUserIDs[0] != 300 {
		t.Fatalf("pending = %+v, want guestbot risk linked to bot 300", *got)
	}
}

func TestRiskUpgradeDuringPendingExpiresWithOneChance(t *testing.T) {
	b, st, _, banned := newRiskTestBot(t)

	createdAt := time.Now().UTC()
	pending := storepkg.PendingVerification{
		ChatID:       -100123,
		UserID:       42,
		UserLanguage: "en",
		Timestamp:    createdAt.Unix(),
		RandomToken:  "upgrade",
		ExpireAt:     createdAt.Add(time.Minute),
	}
	if err := st.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	msg := &gotgbot.Message{
		MessageId:          89,
		From:               &gotgbot.User{Id: 300, IsBot: true},
		GuestBotCallerUser: &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:               gotgbot.Chat{Id: -100123, Type: "supergroup"},
	}
	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	b.onVerifyWindowExpired(b.Bot, pending)

	if len(*banned) != 2 || (*banned)[0] != 42 || (*banned)[1] != 300 {
		t.Fatalf("banned users = %v, want [42 300]", *banned)
	}
}

func TestRiskMessageWithExistingWarningGetsPendingBeforeBan(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)

	if _, err := st.IncrWarningCount(-100123, 42); err != nil {
		t.Fatalf("IncrWarningCount() error = %v", err)
	}

	msg := &gotgbot.Message{
		MessageId: 90,
		From:      &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:      "https://t.me/spam",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*deleted) != 1 || (*deleted)[0] != 90 {
		t.Fatalf("deleted messages = %v, want [90]", *deleted)
	}
	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none before risk pending expiry", *banned)
	}

	pending, err := st.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending == nil || pending.RiskKind != storepkg.PendingRiskTMe {
		t.Fatalf("pending = %+v, want t.me risk pending", pending)
	}

	b.onVerifyWindowExpired(b.Bot, *pending)

	if len(*banned) != 1 || (*banned)[0] != 42 {
		t.Fatalf("banned users after expiry = %v, want [42]", *banned)
	}
}

func TestRiskMessageAfterExpiredNormalPendingGetsNewChanceBeforeBan(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)

	oldPending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         time.Now().UTC().Add(-time.Minute).Unix(),
		RandomToken:       "expired",
		ExpireAt:          time.Now().UTC().Add(-time.Second),
		OriginalMessageID: 91,
	}
	if err := st.SetPending(oldPending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	msg := &gotgbot.Message{
		MessageId: 92,
		From:      &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:      "https://t.me/spam",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none before new risk pending expiry", *banned)
	}
	if len(*deleted) == 0 || (*deleted)[len(*deleted)-1] != 92 {
		t.Fatalf("deleted messages = %v, want current risk message deleted", *deleted)
	}

	pending, err := st.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending == nil || pending.RiskKind != storepkg.PendingRiskTMe || pending.RandomToken == oldPending.RandomToken {
		t.Fatalf("pending = %+v, want new t.me risk pending", pending)
	}

	b.onVerifyWindowExpired(b.Bot, *pending)

	if len(*banned) != 1 || (*banned)[0] != 42 {
		t.Fatalf("banned users after expiry = %v, want [42]", *banned)
	}
}

func TestRiskMessageDeletedEvenWhenMetadataUpdateFails(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)
	b.Store = updateMetadataFailStore{SQLiteStore: st}

	msg := &gotgbot.Message{
		MessageId: 93,
		From:      &gotgbot.User{Id: 42, FirstName: "Risk"},
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:      "https://t.me/spam",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*deleted) < 1 || (*deleted)[0] != 93 {
		t.Fatalf("deleted messages = %v, want risk original deleted before metadata failure cleanup", *deleted)
	}
	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none on metadata update failure", *banned)
	}

	pending, err := st.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending != nil {
		t.Fatalf("GetPending() = %+v, want canceled pending after metadata update failure", *pending)
	}
}

func TestPlainBotMessageIsIgnored(t *testing.T) {
	b, st, deleted, banned := newRiskTestBot(t)

	msg := &gotgbot.Message{
		MessageId: 94,
		From:      &gotgbot.User{Id: 300, IsBot: true, FirstName: "PlainBot"},
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
		Text:      "plain bot status",
	}

	if err := b.handleMessage(b.Bot, &ext.Context{EffectiveMessage: msg}); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(*deleted) != 0 {
		t.Fatalf("deleted messages = %v, want none for plain bot message", *deleted)
	}
	if len(*banned) != 0 {
		t.Fatalf("banned users = %v, want none for plain bot message", *banned)
	}
	pending, err := st.GetPending(msg.Chat.Id, msg.From.Id)
	if err != nil {
		t.Fatalf("GetPending() error = %v", err)
	}
	if pending != nil {
		t.Fatalf("GetPending() = %+v, want no pending for plain bot message", *pending)
	}
}
