package bot

import (
	"context"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type fakePollingUpdater struct {
	dispatcher      ext.UpdateDispatcher
	started         chan struct{}
	idleReleased    chan struct{}
	stopCalls       int
	startPollingErr error
}

func newFakePollingUpdater() *fakePollingUpdater {
	return &fakePollingUpdater{
		started:      make(chan struct{}),
		idleReleased: make(chan struct{}),
	}
}

func (f *fakePollingUpdater) StartPolling(_ *gotgbot.Bot, _ *ext.PollingOpts) error {
	close(f.started)
	return f.startPollingErr
}

func (f *fakePollingUpdater) Stop() error {
	f.stopCalls++
	select {
	case <-f.idleReleased:
	default:
		close(f.idleReleased)
	}
	return nil
}

func (f *fakePollingUpdater) Idle() {
	<-f.idleReleased
}

func TestNewWithTelegramFactoryUsesInjectedBot(t *testing.T) {
	st, err := storepkg.NewSQLiteStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer st.Close()

	cfg := newTestConfig()
	cfg.Bot.Token = "123:test-token"
	client := &recordingBotClient{}
	telegramBot := newRecordingTelegramBot(client)

	b, err := newWithTelegramFactory(cfg, st, blacklist.New(), nil, func(token string, _ *gotgbot.BotOpts) (*gotgbot.Bot, error) {
		if token != cfg.Bot.Token {
			t.Fatalf("token = %q, want %q", token, cfg.Bot.Token)
		}
		return telegramBot, nil
	})
	if err != nil {
		t.Fatalf("newWithTelegramFactory() error = %v", err)
	}

	if b.Bot != telegramBot {
		t.Fatal("Bot.Bot was not initialized with injected telegram bot")
	}
	if b.Captcha != nil {
		t.Fatal("Bot.Captcha = non-nil, want nil")
	}
	if b.timers == nil || b.backgroundTimers == nil {
		t.Fatal("Bot runtime state was not initialized")
	}
	if b.newUpdater == nil {
		t.Fatal("Bot updater factory was not initialized")
	}
}

func TestStartRegistersHandlersAndStopsOnContextCancel(t *testing.T) {
	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	updater := newFakePollingUpdater()
	b.newUpdater = func(dispatcher ext.UpdateDispatcher, _ *ext.UpdaterOpts) pollingUpdater {
		updater.dispatcher = dispatcher
		return updater
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Start(ctx)
	}()

	<-updater.started

	dispatcher, ok := updater.dispatcher.(*ext.Dispatcher)
	if !ok {
		t.Fatalf("dispatcher type = %T, want *ext.Dispatcher", updater.dispatcher)
	}

	update := &gotgbot.Update{
		UpdateId: 1,
		Message: &gotgbot.Message{
			Text: "/stats",
			Chat: gotgbot.Chat{Id: 7, Type: "private"},
			From: &gotgbot.User{Id: 7, FirstName: "Admin"},
		},
	}
	if err := dispatcher.ProcessUpdate(b.Bot, update, nil); err != nil {
		t.Fatalf("dispatcher.ProcessUpdate() error = %v", err)
	}

	if got := len(client.RequestsByMethod("sendMessage")); got == 0 {
		t.Fatal("sendMessage was not called, command handlers were not registered")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start() did not return after context cancellation")
	}

	if updater.stopCalls != 1 {
		t.Fatalf("updater.Stop() calls = %d, want 1", updater.stopCalls)
	}
}

func TestStartRestoresPendingVerificationsAndCleansBackgroundTasks(t *testing.T) {
	client := &recordingBotClient{}
	b := newTestBot(t, nil, client)
	b.Config.Moderation.OriginalMessageTTL = "10ms"
	b.Config.Bot.PendingSweeperInterval = "10ms"

	updater := newFakePollingUpdater()
	b.newUpdater = func(dispatcher ext.UpdateDispatcher, _ *ext.UpdaterOpts) pollingUpdater {
		updater.dispatcher = dispatcher
		return updater
	}

	now := time.Now().UTC()
	pending := storepkg.PendingVerification{
		ChatID:            -100123,
		UserID:            42,
		UserLanguage:      "en",
		Timestamp:         now.Unix(),
		RandomToken:       "abc1234",
		ExpireAt:          now.Add(40 * time.Millisecond),
		OriginalMessageID: 9001,
	}
	if err := b.Store.SetPending(pending); err != nil {
		t.Fatalf("SetPending() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Start(ctx)
	}()

	<-updater.started
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start() did not return after restored timers fired")
	}

	warnings, err := b.Store.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		t.Fatalf("GetWarningCount() error = %v", err)
	}
	if warnings != 1 {
		t.Fatalf("warning count = %d, want 1 after restored expiry", warnings)
	}

	deleteRequests := client.RequestsByMethod("deleteMessage")
	if len(deleteRequests) == 0 {
		t.Fatal("deleteMessage was not called for restored original message cleanup")
	}

	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	if len(b.timers) != 0 {
		t.Fatalf("tracked user timers = %d, want 0 after shutdown", len(b.timers))
	}
	if len(b.backgroundTimers) != 0 {
		t.Fatalf("tracked background timers = %d, want 0 after shutdown", len(b.backgroundTimers))
	}
	if len(b.shutdownStops) != 0 {
		t.Fatalf("shutdown stop hooks = %d, want 0 after shutdown", len(b.shutdownStops))
	}
}
