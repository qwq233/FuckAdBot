package bot

import (
	"sync"
	"testing"
	"time"
)

// TestCacheGetLanguagePreferenceReturnsFreshEntry verifies that a non-expired
// entry is returned correctly.
func TestCacheGetLanguagePreferenceReturnsFreshEntry(t *testing.T) {
	t.Parallel()

	var c botCache
	now := time.Now()
	c.setLanguagePreference(1, cachedLanguagePreference{
		language:      "en",
		hasPreference: true,
		expiresAt:     now.Add(time.Minute),
	})

	entry, ok := c.getLanguagePreference(1, now)
	if !ok {
		t.Fatal("getLanguagePreference() ok = false, want true for fresh entry")
	}
	if entry.language != "en" {
		t.Fatalf("entry.language = %q, want %q", entry.language, "en")
	}
	if !entry.hasPreference {
		t.Fatal("entry.hasPreference = false, want true")
	}
}

// TestCacheGetLanguagePreferenceRejectsExpiredEntry verifies that an already-expired
// entry is not returned.
func TestCacheGetLanguagePreferenceRejectsExpiredEntry(t *testing.T) {
	t.Parallel()

	var c botCache
	now := time.Now()
	c.setLanguagePreference(1, cachedLanguagePreference{
		language:      "en",
		hasPreference: true,
		expiresAt:     now.Add(-time.Second), // already expired
	})

	_, ok := c.getLanguagePreference(1, now)
	if ok {
		t.Fatal("getLanguagePreference() ok = true, want false for expired entry")
	}
}

// TestCacheGetUserChatReturnsFreshEntry verifies that a non-expired user chat
// entry is returned.
func TestCacheGetUserChatReturnsFreshEntry(t *testing.T) {
	t.Parallel()

	var c botCache
	now := time.Now()
	c.setUserChat(42, nil, now.Add(time.Minute))

	_, ok := c.getUserChat(42, now)
	if !ok {
		t.Fatal("getUserChat() ok = false, want true for fresh entry")
	}
}

// TestCacheGetUserChatRejectsExpiredEntry verifies that an expired user chat
// entry is not returned.
func TestCacheGetUserChatRejectsExpiredEntry(t *testing.T) {
	t.Parallel()

	var c botCache
	now := time.Now()
	c.setUserChat(42, nil, now.Add(-time.Second))

	_, ok := c.getUserChat(42, now)
	if ok {
		t.Fatal("getUserChat() ok = true, want false for expired entry")
	}
}

// TestCacheEvictExpiredRemovesExpiredKeepsValid verifies that evictExpired only
// removes entries that have passed their TTL.
func TestCacheEvictExpiredRemovesExpiredKeepsValid(t *testing.T) {
	t.Parallel()

	var c botCache
	now := time.Now()

	c.setLanguagePreference(1, cachedLanguagePreference{expiresAt: now.Add(-time.Second)}) // expired
	c.setLanguagePreference(2, cachedLanguagePreference{expiresAt: now.Add(time.Minute)})  // fresh
	c.setUserChat(10, nil, now.Add(-time.Second))                                          // expired
	c.setUserChat(20, nil, now.Add(time.Minute))                                           // fresh

	c.evictExpired(now)

	c.languagesMu.RLock()
	_, hasExp1 := c.languages[1]
	_, hasFresh2 := c.languages[2]
	c.languagesMu.RUnlock()

	c.userChatsMu.RLock()
	_, hasExp10 := c.userChats[10]
	_, hasFresh20 := c.userChats[20]
	c.userChatsMu.RUnlock()

	if hasExp1 {
		t.Error("expired language entry 1 should have been evicted")
	}
	if !hasFresh2 {
		t.Error("fresh language entry 2 should remain after eviction")
	}
	if hasExp10 {
		t.Error("expired userChat entry 10 should have been evicted")
	}
	if !hasFresh20 {
		t.Error("fresh userChat entry 20 should remain after eviction")
	}
}

// TestCacheEvictExpiredOnEmptyCache verifies that evictExpired does not panic
// on an uninitialised cache.
func TestCacheEvictExpiredOnEmptyCache(t *testing.T) {
	t.Parallel()

	var c botCache
	// Must not panic.
	c.evictExpired(time.Now())
}

// TestCacheConcurrentAccess exercises concurrent reads, writes, and eviction
// on the cache. Run with -race to detect data races.
func TestCacheConcurrentAccess(t *testing.T) {
	t.Parallel()

	var c botCache
	var wg sync.WaitGroup

	const goroutines = 100

	wg.Add(goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			userID := int64(id % 10)
			now := time.Now()

			switch id % 5 {
			case 0:
				c.setLanguagePreference(userID, cachedLanguagePreference{
					language:      "en",
					hasPreference: true,
					expiresAt:     now.Add(time.Minute),
				})
			case 1:
				c.getLanguagePreference(userID, now)
			case 2:
				c.setUserChat(userID, nil, now.Add(time.Minute))
			case 3:
				c.getUserChat(userID, now)
			case 4:
				c.evictExpired(now)
			}
		}(i)
	}

	wg.Wait()
}
