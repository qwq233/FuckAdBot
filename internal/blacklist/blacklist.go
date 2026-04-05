package blacklist

import (
	"strings"
	"sync"
)

type Blacklist struct {
	mu    sync.RWMutex
	words map[string]struct{}
}

func New() *Blacklist {
	return &Blacklist{
		words: make(map[string]struct{}),
	}
}

func (b *Blacklist) Load(words []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range words {
		b.words[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
}

func (b *Blacklist) Add(word string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.words[strings.ToLower(strings.TrimSpace(word))] = struct{}{}
}

func (b *Blacklist) Remove(word string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(word))
	if _, ok := b.words[key]; !ok {
		return false
	}
	delete(b.words, key)
	return true
}

func (b *Blacklist) List() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]string, 0, len(b.words))
	for w := range b.words {
		result = append(result, w)
	}
	return result
}

// Match checks if the text contains any blacklisted word (case-insensitive substring match).
// Returns the first matched word, or empty string if no match.
func (b *Blacklist) Match(text string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	lower := strings.ToLower(text)
	for w := range b.words {
		if w != "" && strings.Contains(lower, w) {
			return w
		}
	}
	return ""
}
