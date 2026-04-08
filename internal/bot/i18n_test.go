package bot

import "testing"

func TestFormatDetectedLanguageLineIsBlank(t *testing.T) {
	t.Parallel()

	if got := formatDetectedLanguageLine("en", "zh-cn"); got != "" {
		t.Fatalf("formatDetectedLanguageLine() = %q, want empty string", got)
	}
}

func TestAppendDetectedLanguageLineIsNoop(t *testing.T) {
	t.Parallel()

	text := tr("zh-cn", "approve_result", 42)
	if got := appendDetectedLanguageLine(text, "en", "zh-cn"); got != text {
		t.Fatalf("appendDetectedLanguageLine() = %q, want %q", got, text)
	}
}
