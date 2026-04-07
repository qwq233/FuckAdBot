package bot

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/store"
	"gopkg.in/yaml.v3"
)

const (
	defaultUserLanguage = "zh-cn"
	englishUserLanguage = "en"
)

// Embed locale catalogs into the binary at build time.
//
//go:embed locales/*.yaml
var localeCatalogs embed.FS

var translations = mustLoadTranslations()

func normalizeUserLanguage(code string) string {
	normalized := strings.ToLower(strings.TrimSpace(code))
	normalized = strings.ReplaceAll(normalized, "_", "-")

	switch {
	case strings.HasPrefix(normalized, englishUserLanguage):
		return englishUserLanguage
	case normalized == "", strings.HasPrefix(normalized, "zh"):
		return defaultUserLanguage
	default:
		return defaultUserLanguage
	}
}

func userLanguageFromUser(user *gotgbot.User) string {
	if user == nil {
		return defaultUserLanguage
	}
	return normalizeUserLanguage(user.LanguageCode)
}

func userLanguageFromPending(pending *store.PendingVerification) string {
	if pending == nil {
		return defaultUserLanguage
	}
	return normalizeUserLanguage(pending.UserLanguage)
}

func (b *Bot) targetUserLanguage(chatID, userID int64) string {
	pending, err := b.Store.GetPending(chatID, userID)
	if err != nil || pending == nil {
		return defaultUserLanguage
	}
	return userLanguageFromPending(pending)
}

func localizedLanguageName(targetLanguage, viewerLanguage string) string {
	return tr(viewerLanguage, languageNameKey(targetLanguage))
}

func formatDetectedLanguageLine(targetLanguage, viewerLanguage string) string {
	return tr(viewerLanguage, "detected_language_line", localizedLanguageName(targetLanguage, viewerLanguage))
}

func appendDetectedLanguageLine(text, targetLanguage, viewerLanguage string) string {
	return text + "\n" + formatDetectedLanguageLine(targetLanguage, viewerLanguage)
}

func tr(locale, key string, args ...any) string {
	template := translationFor(normalizeUserLanguage(locale), key)

	if len(args) == 0 {
		return template
	}

	return fmt.Sprintf(template, args...)
}

func translationFor(locale, key string) string {
	if text, ok := translations[locale][key]; ok {
		return text
	}

	if text, ok := translations[defaultUserLanguage][key]; ok {
		return text
	}

	return key
}

func mustLoadTranslations() map[string]map[string]string {
	messages, err := loadTranslations()
	if err != nil {
		panic(err)
	}
	return messages
}

func loadTranslations() (map[string]map[string]string, error) {
	matchedFiles, err := fs.Glob(localeCatalogs, "locales/*.yaml")
	if err != nil {
		return nil, fmt.Errorf("glob locale catalogs: %w", err)
	}
	if len(matchedFiles) == 0 {
		return nil, fmt.Errorf("no locale catalogs embedded")
	}

	loaded := make(map[string]map[string]string, len(matchedFiles))
	for _, fileName := range matchedFiles {
		content, err := localeCatalogs.ReadFile(fileName)
		if err != nil {
			return nil, fmt.Errorf("read locale catalog %s: %w", fileName, err)
		}

		var messages map[string]string
		if err := yaml.Unmarshal(content, &messages); err != nil {
			return nil, fmt.Errorf("parse locale catalog %s: %w", fileName, err)
		}

		locale := normalizeUserLanguage(strings.TrimSuffix(path.Base(fileName), path.Ext(fileName)))
		if _, exists := loaded[locale]; exists {
			return nil, fmt.Errorf("duplicate locale catalog for %s", locale)
		}

		loaded[locale] = messages
	}

	if _, ok := loaded[defaultUserLanguage]; !ok {
		return nil, fmt.Errorf("missing default locale catalog %s", defaultUserLanguage)
	}

	return loaded, nil
}

func languageNameKey(locale string) string {
	switch normalizeUserLanguage(locale) {
	case englishUserLanguage:
		return "language_name_en"
	default:
		return "language_name_zh_cn"
	}
}
