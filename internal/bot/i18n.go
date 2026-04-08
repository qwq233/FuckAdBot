package bot

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"path"
	"strings"
	"time"

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

var supportedUserLanguages = []string{defaultUserLanguage, englishUserLanguage}

func resolveSupportedUserLanguage(code string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if normalized == "" {
		return "", false
	}

	switch {
	case strings.HasPrefix(normalized, englishUserLanguage):
		return englishUserLanguage, true
	case normalized == "zh", strings.HasPrefix(normalized, "zh-"):
		return defaultUserLanguage, true
	default:
		return "", false
	}
}

func normalizeUserLanguage(code string) string {
	if normalized, ok := resolveSupportedUserLanguage(code); ok {
		return normalized
	}

	return defaultUserLanguage
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

func (b *Bot) preferredUserLanguage(userID int64) string {
	if b == nil || b.Store == nil || userID == 0 {
		return ""
	}

	now := time.Now()
	if entry, ok := b.cache.getLanguagePreference(userID, now); ok {
		if entry.hasPreference {
			return entry.language
		}
		return ""
	}

	preferredLanguage, err := b.Store.GetUserLanguagePreference(userID)
	if err != nil {
		log.Printf("[bot] store.GetUserLanguagePreference error: %v", err)
		return ""
	}

	cacheEntry := cachedLanguagePreference{
		hasPreference: preferredLanguage != "",
		expiresAt:     now.Add(preferredUserLanguageCacheTTL),
	}
	if preferredLanguage == "" {
		b.cache.setLanguagePreference(userID, cacheEntry)
		return ""
	}

	cacheEntry.language = normalizeUserLanguage(preferredLanguage)
	b.cache.setLanguagePreference(userID, cacheEntry)
	return cacheEntry.language
}

func (b *Bot) requestLanguageForUser(user *gotgbot.User) string {
	if user == nil {
		return defaultUserLanguage
	}

	if preferredLanguage := b.preferredUserLanguage(user.Id); preferredLanguage != "" {
		return preferredLanguage
	}

	return userLanguageFromUser(user)
}

func (b *Bot) applyUserLanguagePreference(userID int64, input string) (string, bool, error) {
	language, ok := resolveSupportedUserLanguage(input)
	if !ok {
		return "", false, nil
	}

	if err := b.Store.SetUserLanguagePreference(userID, language); err != nil {
		return "", false, err
	}

	b.cache.setLanguagePreference(userID, cachedLanguagePreference{
		language:      language,
		hasPreference: true,
		expiresAt:     time.Now().Add(preferredUserLanguageCacheTTL),
	})

	return language, true, nil
}

func (b *Bot) targetUserLanguage(chatID, userID int64) string {
	pending, err := b.Store.GetPending(chatID, userID)
	if err == nil && pending != nil {
		return userLanguageFromPending(pending)
	}

	if preferredLanguage := b.preferredUserLanguage(userID); preferredLanguage != "" {
		return preferredLanguage
	}

	return defaultUserLanguage
}

func localizedLanguageName(targetLanguage, viewerLanguage string) string {
	return tr(viewerLanguage, languageNameKey(targetLanguage))
}

func formatDetectedLanguageLine(_, _ string) string {
	return ""
}

func appendDetectedLanguageLine(text, _, _ string) string {
	// Keep this helper as a no-op so existing call sites stop appending
	// "Language: XXX" / "语言: XXX" while remaining source-compatible.
	return text
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
