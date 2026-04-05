package blacklist

import (
	"strings"
	"sync"
)

type Blacklist struct {
	mu     sync.RWMutex
	global map[string]struct{}
	groups map[int64]map[string]struct{}
}

func New() *Blacklist {
	return &Blacklist{
		global: make(map[string]struct{}),
		groups: make(map[int64]map[string]struct{}),
	}
}

func (b *Blacklist) Load(words []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range words {
		b.global[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
}

func (b *Blacklist) Add(word string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.global[strings.ToLower(strings.TrimSpace(word))] = struct{}{}
}

func (b *Blacklist) Remove(word string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(word))
	if _, ok := b.global[key]; !ok {
		return false
	}
	delete(b.global, key)
	return true
}

func (b *Blacklist) List() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]string, 0, len(b.global))
	for w := range b.global {
		result = append(result, w)
	}
	return result
}

// Match checks if the text contains any globally blacklisted word.
func (b *Blacklist) Match(text string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	lower := strings.ToLower(text)
	for w := range b.global {
		if w != "" && strings.Contains(lower, w) {
			return w
		}
	}
	return ""
}

// --- Group-scoped methods ---

func (b *Blacklist) LoadGroup(chatID int64, words []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	for _, w := range words {
		b.groups[chatID][strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
}

func (b *Blacklist) AddGroup(chatID int64, word string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	b.groups[chatID][strings.ToLower(strings.TrimSpace(word))] = struct{}{}
}

func (b *Blacklist) RemoveGroup(chatID int64, word string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	group, ok := b.groups[chatID]
	if !ok {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(word))
	if _, exists := group[key]; !exists {
		return false
	}
	delete(group, key)
	if len(group) == 0 {
		delete(b.groups, chatID)
	}
	return true
}

func (b *Blacklist) ListGroup(chatID int64) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	group, ok := b.groups[chatID]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(group))
	for w := range group {
		result = append(result, w)
	}
	return result
}

// MatchWithGroup checks text against both global and group-specific blacklists.
func (b *Blacklist) MatchWithGroup(chatID int64, text string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	lower := strings.ToLower(text)
	for w := range b.global {
		if w != "" && strings.Contains(lower, w) {
			return w
		}
	}
	if group, ok := b.groups[chatID]; ok {
		for w := range group {
			if w != "" && strings.Contains(lower, w) {
				return w
			}
		}
	}
	return ""
}
