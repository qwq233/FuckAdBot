package bot

import (
	"log"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

const (
	preferredUserLanguageCacheTTL = 10 * time.Minute
	userChatInfoCacheTTL          = 10 * time.Minute
	userChatInfoErrorCacheTTL     = 1 * time.Minute
)

type cachedLanguagePreference struct {
	language      string
	hasPreference bool
	expiresAt     time.Time
}

type cachedUserChatInfo struct {
	chat      *gotgbot.ChatFullInfo
	expiresAt time.Time
}

type adminCacheKey struct {
	chatID int64
	userID int64
}

type cachedAdminStatus struct {
	isAdmin   bool
	expiresAt time.Time
}

// botCache holds short-lived in-process caches for Telegram API lookups.
// The three maps are guarded by separate RWMutexes so that concurrent handler
// goroutines do not contend on unrelated cache domains.
type botCache struct {
	languagesMu sync.RWMutex
	languages   map[int64]cachedLanguagePreference

	userChatsMu sync.RWMutex
	userChats   map[int64]cachedUserChatInfo

	adminStatusMu sync.RWMutex
	adminStatus   map[adminCacheKey]cachedAdminStatus
}

func (c *botCache) getLanguagePreference(userID int64, now time.Time) (cachedLanguagePreference, bool) {
	c.languagesMu.RLock()
	defer c.languagesMu.RUnlock()

	if c.languages == nil {
		return cachedLanguagePreference{}, false
	}

	entry, ok := c.languages[userID]
	if !ok || !entry.expiresAt.After(now) {
		return cachedLanguagePreference{}, false
	}

	return entry, true
}

func (c *botCache) setLanguagePreference(userID int64, entry cachedLanguagePreference) {
	c.languagesMu.Lock()
	defer c.languagesMu.Unlock()

	if c.languages == nil {
		c.languages = make(map[int64]cachedLanguagePreference)
	}

	c.languages[userID] = entry
}

func (c *botCache) getUserChat(userID int64, now time.Time) (*gotgbot.ChatFullInfo, bool) {
	c.userChatsMu.RLock()
	defer c.userChatsMu.RUnlock()

	if c.userChats == nil {
		return nil, false
	}

	entry, ok := c.userChats[userID]
	if !ok || !entry.expiresAt.After(now) {
		return nil, false
	}

	return entry.chat, true
}

func (c *botCache) setUserChat(userID int64, chat *gotgbot.ChatFullInfo, expiresAt time.Time) {
	c.userChatsMu.Lock()
	defer c.userChatsMu.Unlock()

	if c.userChats == nil {
		c.userChats = make(map[int64]cachedUserChatInfo)
	}

	c.userChats[userID] = cachedUserChatInfo{
		chat:      chat,
		expiresAt: expiresAt,
	}
}

func (c *botCache) getAdminStatus(chatID, userID int64, now time.Time) (bool, bool) {
	c.adminStatusMu.RLock()
	defer c.adminStatusMu.RUnlock()

	if c.adminStatus == nil {
		return false, false
	}

	entry, ok := c.adminStatus[adminCacheKey{chatID: chatID, userID: userID}]
	if !ok || !entry.expiresAt.After(now) {
		return false, false
	}

	return entry.isAdmin, true
}

func (c *botCache) setAdminStatus(chatID, userID int64, isAdmin bool, expiresAt time.Time) {
	c.adminStatusMu.Lock()
	defer c.adminStatusMu.Unlock()

	if c.adminStatus == nil {
		c.adminStatus = make(map[adminCacheKey]cachedAdminStatus)
	}

	c.adminStatus[adminCacheKey{chatID: chatID, userID: userID}] = cachedAdminStatus{
		isAdmin:   isAdmin,
		expiresAt: expiresAt,
	}
}

func (b *Bot) cachedUserChat(userID int64, fetch func(int64) (*gotgbot.ChatFullInfo, error)) *gotgbot.ChatFullInfo {
	if b == nil || userID == 0 || fetch == nil {
		return nil
	}

	now := time.Now()
	if chat, ok := b.cache.getUserChat(userID, now); ok {
		return chat
	}

	chat, err := fetch(userID)
	if err != nil {
		log.Printf("[bot] GetChat error: %v", err)
		b.cache.setUserChat(userID, nil, now.Add(userChatInfoErrorCacheTTL))
		return nil
	}

	b.cache.setUserChat(userID, chat, now.Add(userChatInfoCacheTTL))
	return chat
}

const cacheCleanupInterval = 5 * time.Minute

func (c *botCache) evictExpired(now time.Time) {
	// Collect stale language keys under RLock to minimize write-lock duration.
	var staleLang []int64
	c.languagesMu.RLock()
	for k, v := range c.languages {
		if !v.expiresAt.After(now) {
			staleLang = append(staleLang, k)
		}
	}
	c.languagesMu.RUnlock()
	if len(staleLang) > 0 {
		c.languagesMu.Lock()
		for _, k := range staleLang {
			delete(c.languages, k)
		}
		c.languagesMu.Unlock()
	}

	var staleChats []int64
	c.userChatsMu.RLock()
	for k, v := range c.userChats {
		if !v.expiresAt.After(now) {
			staleChats = append(staleChats, k)
		}
	}
	c.userChatsMu.RUnlock()
	if len(staleChats) > 0 {
		c.userChatsMu.Lock()
		for _, k := range staleChats {
			delete(c.userChats, k)
		}
		c.userChatsMu.Unlock()
	}

	var staleAdmin []adminCacheKey
	c.adminStatusMu.RLock()
	for k, v := range c.adminStatus {
		if !v.expiresAt.After(now) {
			staleAdmin = append(staleAdmin, k)
		}
	}
	c.adminStatusMu.RUnlock()
	if len(staleAdmin) > 0 {
		c.adminStatusMu.Lock()
		for _, k := range staleAdmin {
			delete(c.adminStatus, k)
		}
		c.adminStatusMu.Unlock()
	}
}
