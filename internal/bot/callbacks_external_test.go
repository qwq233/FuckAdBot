package bot_test

import (
	"testing"

	botpkg "github.com/qwq233/fuckadbot/internal/bot"
)

func TestBuildReminderKeyboardLayout(t *testing.T) {
	t.Parallel()

	markup := botpkg.BuildReminderKeyboard("https://verify.example.com/verify?uid=1", -100123, 42, "zh-cn")

	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("row count = %d, want 2", len(markup.InlineKeyboard))
	}

	if len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("top row button count = %d, want 1", len(markup.InlineKeyboard[0]))
	}

	if markup.InlineKeyboard[0][0].Url == "" {
		t.Fatal("top row should contain verification URL button")
	}

	if len(markup.InlineKeyboard[1]) != 2 {
		t.Fatalf("bottom row button count = %d, want 2", len(markup.InlineKeyboard[1]))
	}

	if markup.InlineKeyboard[1][0].Text != "✅ 批准" {
		t.Fatalf("left admin button text = %q, want %q", markup.InlineKeyboard[1][0].Text, "✅ 批准")
	}

	if markup.InlineKeyboard[1][1].Text != "🚫 拒绝" {
		t.Fatalf("right admin button text = %q, want %q", markup.InlineKeyboard[1][1].Text, "🚫 拒绝")
	}
}

func TestBuildReminderKeyboardEnglishLayout(t *testing.T) {
	t.Parallel()

	markup := botpkg.BuildReminderKeyboard("https://verify.example.com/verify?uid=1", -100123, 42, "en-us")

	if markup.InlineKeyboard[0][0].Text != "🛡️ Verify" {
		t.Fatalf("top verify button text = %q, want %q", markup.InlineKeyboard[0][0].Text, "🛡️ Verify")
	}

	if markup.InlineKeyboard[1][0].Text != "✅ Approve" {
		t.Fatalf("left admin button text = %q, want %q", markup.InlineKeyboard[1][0].Text, "✅ Approve")
	}

	if markup.InlineKeyboard[1][1].Text != "🚫 Reject" {
		t.Fatalf("right admin button text = %q, want %q", markup.InlineKeyboard[1][1].Text, "🚫 Reject")
	}
}

func TestParseModerationCallbackData(t *testing.T) {
	t.Parallel()

	action, chatID, userID, err := botpkg.ParseModerationCallbackData(botpkg.BuildModerationCallbackData("a", -100123, 42))
	if err != nil {
		t.Fatalf("ParseModerationCallbackData() error = %v", err)
	}

	if action != "a" || chatID != -100123 || userID != 42 {
		t.Fatalf("parsed values = (%q, %d, %d), want (%q, %d, %d)", action, chatID, userID, "a", int64(-100123), int64(42))
	}
}

func TestParseModerationCallbackDataRejectsInvalidData(t *testing.T) {
	t.Parallel()

	cases := []string{
		"bad",
		"review:x:-100123:42",
		"review:a:not-a-chat:42",
		"review:a:-100123:not-a-user",
	}

	for _, data := range cases {
		data := data
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			if _, _, _, err := botpkg.ParseModerationCallbackData(data); err == nil {
				t.Fatalf("ParseModerationCallbackData(%q) error = nil, want error", data)
			}
		})
	}
}

func TestBuildLanguagePreferenceKeyboardLayout(t *testing.T) {
	t.Parallel()

	markup := botpkg.BuildLanguagePreferenceKeyboard("zh-cn")

	if len(markup.InlineKeyboard) != 1 {
		t.Fatalf("row count = %d, want 1", len(markup.InlineKeyboard))
	}

	if len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("button count = %d, want 2", len(markup.InlineKeyboard[0]))
	}

	if markup.InlineKeyboard[0][0].Text != "简体中文" {
		t.Fatalf("first language button text = %q, want %q", markup.InlineKeyboard[0][0].Text, "简体中文")
	}

	if markup.InlineKeyboard[0][1].Text != "English" {
		t.Fatalf("second language button text = %q, want %q", markup.InlineKeyboard[0][1].Text, "English")
	}
}

func TestParseLanguagePreferenceCallbackData(t *testing.T) {
	t.Parallel()

	language, err := botpkg.ParseLanguagePreferenceCallbackData(botpkg.BuildLanguagePreferenceCallbackData("en-us"))
	if err != nil {
		t.Fatalf("ParseLanguagePreferenceCallbackData() error = %v", err)
	}

	if language != "en" {
		t.Fatalf("language = %q, want %q", language, "en")
	}
}

func TestParseLanguagePreferenceCallbackDataRejectsInvalidData(t *testing.T) {
	t.Parallel()

	cases := []string{
		"bad",
		"lang:fr",
	}

	for _, data := range cases {
		data := data
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			if _, err := botpkg.ParseLanguagePreferenceCallbackData(data); err == nil {
				t.Fatalf("ParseLanguagePreferenceCallbackData(%q) error = nil, want error", data)
			}
		})
	}
}
