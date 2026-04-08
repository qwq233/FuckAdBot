package blacklist

import (
	"sort"
	"strings"
	"sync"
)

type automatonNode struct {
	next    map[rune]int
	fail    int
	outputs []string
}

type matcher struct {
	nodes []automatonNode
}

type Blacklist struct {
	mu            sync.RWMutex
	global        map[string]struct{}
	groups        map[int64]map[string]struct{}
	globalMatcher *matcher
	groupMatchers map[int64]*matcher
}

func New() *Blacklist {
	return &Blacklist{
		global:        make(map[string]struct{}),
		groups:        make(map[int64]map[string]struct{}),
		groupMatchers: make(map[int64]*matcher),
	}
}

func (b *Blacklist) Load(words []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range words {
		if normalized := normalizeWord(w); normalized != "" {
			b.global[normalized] = struct{}{}
		}
	}
	b.globalMatcher = newMatcher(b.global)
}

func (b *Blacklist) Add(word string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if normalized := normalizeWord(word); normalized != "" {
		b.global[normalized] = struct{}{}
		b.globalMatcher = newMatcher(b.global)
	}
}

func (b *Blacklist) Remove(word string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := normalizeWord(word)
	if _, ok := b.global[key]; !ok {
		return false
	}
	delete(b.global, key)
	b.globalMatcher = newMatcher(b.global)
	return true
}

func (b *Blacklist) List() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return sortedWords(b.global)
}

// Match checks if the text contains any globally blacklisted word.
func (b *Blacklist) Match(text string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.globalMatcher == nil {
		return ""
	}
	return b.globalMatcher.Match(text)
}

// --- Group-scoped methods ---

func (b *Blacklist) LoadGroup(chatID int64, words []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	for _, w := range words {
		if normalized := normalizeWord(w); normalized != "" {
			b.groups[chatID][normalized] = struct{}{}
		}
	}
	b.groupMatchers[chatID] = newMatcher(b.groups[chatID])
}

func (b *Blacklist) AddGroup(chatID int64, word string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	if normalized := normalizeWord(word); normalized != "" {
		b.groups[chatID][normalized] = struct{}{}
		b.groupMatchers[chatID] = newMatcher(b.groups[chatID])
	}
}

func (b *Blacklist) RemoveGroup(chatID int64, word string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	group, ok := b.groups[chatID]
	if !ok {
		return false
	}
	key := normalizeWord(word)
	if _, exists := group[key]; !exists {
		return false
	}
	delete(group, key)
	if len(group) == 0 {
		delete(b.groups, chatID)
		delete(b.groupMatchers, chatID)
		return true
	}
	b.groupMatchers[chatID] = newMatcher(group)
	return true
}

func (b *Blacklist) ListGroup(chatID int64) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	group, ok := b.groups[chatID]
	if !ok {
		return nil
	}
	return sortedWords(group)
}

// MatchWithGroup checks text against both global and group-specific blacklists.
func (b *Blacklist) MatchWithGroup(chatID int64, text string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.globalMatcher != nil {
		if matched := b.globalMatcher.Match(text); matched != "" {
			return matched
		}
	}
	if groupMatcher, ok := b.groupMatchers[chatID]; ok && groupMatcher != nil {
		return groupMatcher.Match(text)
	}
	return ""
}

func normalizeWord(word string) string {
	return strings.ToLower(strings.TrimSpace(word))
}

func sortedWords(words map[string]struct{}) []string {
	result := make([]string, 0, len(words))
	for w := range words {
		result = append(result, w)
	}
	sort.Strings(result)
	return result
}

func newMatcher(words map[string]struct{}) *matcher {
	if len(words) == 0 {
		return nil
	}

	ordered := sortedWords(words)
	m := &matcher{
		nodes: []automatonNode{{next: make(map[rune]int)}},
	}

	for _, word := range ordered {
		state := 0
		for _, r := range word {
			nextState, ok := m.nodes[state].next[r]
			if !ok {
				nextState = len(m.nodes)
				m.nodes = append(m.nodes, automatonNode{next: make(map[rune]int)})
				m.nodes[state].next[r] = nextState
			}
			state = nextState
		}
		m.nodes[state].outputs = append(m.nodes[state].outputs, word)
	}

	queue := make([]int, 0)
	for _, nextState := range m.nodes[0].next {
		queue = append(queue, nextState)
	}

	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]

		for r, nextState := range m.nodes[state].next {
			queue = append(queue, nextState)

			fail := m.nodes[state].fail
			for fail != 0 {
				if fallback, ok := m.nodes[fail].next[r]; ok {
					m.nodes[nextState].fail = fallback
					break
				}
				fail = m.nodes[fail].fail
			}
			if fallback, ok := m.nodes[fail].next[r]; ok && state != 0 {
				m.nodes[nextState].fail = fallback
			}

			if outputs := m.nodes[m.nodes[nextState].fail].outputs; len(outputs) > 0 {
				m.nodes[nextState].outputs = append(m.nodes[nextState].outputs, outputs...)
			}
		}
	}

	return m
}

func (m *matcher) Match(text string) string {
	if m == nil {
		return ""
	}

	state := 0
	for _, r := range strings.ToLower(text) {
		for state != 0 {
			if _, ok := m.nodes[state].next[r]; ok {
				break
			}
			state = m.nodes[state].fail
		}

		if nextState, ok := m.nodes[state].next[r]; ok {
			state = nextState
		} else {
			state = 0
		}

		if len(m.nodes[state].outputs) > 0 {
			return m.nodes[state].outputs[0]
		}
	}

	return ""
}
