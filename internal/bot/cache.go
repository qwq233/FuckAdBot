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

type botCache struct {
	mu        sync.RWMutex
	languages map[int64]cachedLanguagePreference
	userChats map[int64]cachedUserChatInfo
}

func (c *botCache) getLanguagePreference(userID int64, now time.Time) (cachedLanguagePreference, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

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
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.languages == nil {
		c.languages = make(map[int64]cachedLanguagePreference)
	}

	c.languages[userID] = entry
}

func (c *botCache) getUserChat(userID int64, now time.Time) (*gotgbot.ChatFullInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

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
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.userChats == nil {
		c.userChats = make(map[int64]cachedUserChatInfo)
	}

	c.userChats[userID] = cachedUserChatInfo{
		chat:      chat,
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
