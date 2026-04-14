package blacklist

import (
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type automatonNode struct {
	ascii  [utf8.RuneSelf]int
	next   map[rune]int
	fail   int
	output string
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
	globalVersion uint64           // incremented on every global write, under mu
	groupVersions map[int64]uint64 // incremented on every per-group write, under mu
}

func New() *Blacklist {
	return &Blacklist{
		global:        make(map[string]struct{}),
		groups:        make(map[int64]map[string]struct{}),
		groupMatchers: make(map[int64]*matcher),
		groupVersions: make(map[int64]uint64),
	}
}

func (b *Blacklist) Load(words []string) {
	b.mu.Lock()
	for _, w := range words {
		if normalized := normalizeWord(w); normalized != "" {
			b.global[normalized] = struct{}{}
		}
	}
	snapshot := cloneWordSet(b.global)
	b.globalVersion++
	ver := b.globalVersion
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.globalVersion == ver {
		b.globalMatcher = m
	}
	b.mu.Unlock()
}

func (b *Blacklist) Add(word string) {
	normalized := normalizeWord(word)
	if normalized == "" {
		return
	}

	b.mu.Lock()
	b.global[normalized] = struct{}{}
	snapshot := cloneWordSet(b.global)
	b.globalVersion++
	ver := b.globalVersion
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.globalVersion == ver {
		b.globalMatcher = m
	}
	b.mu.Unlock()
}

func (b *Blacklist) Remove(word string) bool {
	key := normalizeWord(word)

	b.mu.Lock()
	if _, ok := b.global[key]; !ok {
		b.mu.Unlock()
		return false
	}
	delete(b.global, key)
	snapshot := cloneWordSet(b.global)
	b.globalVersion++
	ver := b.globalVersion
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.globalVersion == ver {
		b.globalMatcher = m
	}
	b.mu.Unlock()
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

func (b *Blacklist) MatchFields(fields ...string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.globalMatcher == nil {
		return ""
	}
	return b.globalMatcher.MatchFields(fields...)
}

// --- Group-scoped methods ---

func (b *Blacklist) LoadGroup(chatID int64, words []string) {
	b.mu.Lock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	for _, w := range words {
		if normalized := normalizeWord(w); normalized != "" {
			b.groups[chatID][normalized] = struct{}{}
		}
	}
	snapshot := cloneWordSet(b.groups[chatID])
	b.groupVersions[chatID]++
	ver := b.groupVersions[chatID]
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.groupVersions[chatID] == ver {
		b.groupMatchers[chatID] = m
	}
	b.mu.Unlock()
}

func (b *Blacklist) AddGroup(chatID int64, word string) {
	normalized := normalizeWord(word)
	if normalized == "" {
		return
	}

	b.mu.Lock()
	if _, ok := b.groups[chatID]; !ok {
		b.groups[chatID] = make(map[string]struct{})
	}
	b.groups[chatID][normalized] = struct{}{}
	snapshot := cloneWordSet(b.groups[chatID])
	b.groupVersions[chatID]++
	ver := b.groupVersions[chatID]
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.groupVersions[chatID] == ver {
		b.groupMatchers[chatID] = m
	}
	b.mu.Unlock()
}

func (b *Blacklist) RemoveGroup(chatID int64, word string) bool {
	key := normalizeWord(word)

	b.mu.Lock()
	group, ok := b.groups[chatID]
	if !ok {
		b.mu.Unlock()
		return false
	}
	if _, exists := group[key]; !exists {
		b.mu.Unlock()
		return false
	}
	delete(group, key)
	if len(group) == 0 {
		delete(b.groups, chatID)
		delete(b.groupMatchers, chatID)
		delete(b.groupVersions, chatID)
		b.mu.Unlock()
		return true
	}
	snapshot := cloneWordSet(group)
	b.groupVersions[chatID]++
	ver := b.groupVersions[chatID]
	b.mu.Unlock()

	m := newMatcher(snapshot)

	b.mu.Lock()
	if b.groupVersions[chatID] == ver {
		b.groupMatchers[chatID] = m
	}
	b.mu.Unlock()
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

func (b *Blacklist) MatchFieldsWithGroup(chatID int64, fields ...string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.globalMatcher != nil {
		if matched := b.globalMatcher.MatchFields(fields...); matched != "" {
			return matched
		}
	}
	if groupMatcher, ok := b.groupMatchers[chatID]; ok && groupMatcher != nil {
		return groupMatcher.MatchFields(fields...)
	}
	return ""
}

func normalizeWord(word string) string {
	return strings.ToLower(strings.TrimSpace(word))
}

func cloneWordSet(m map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(m))
	for k := range m {
		clone[k] = struct{}{}
	}
	return clone
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
		nodes: []automatonNode{{}},
	}

	for _, word := range ordered {
		state := 0
		for _, r := range word {
			nextState := m.transition(state, r)
			if nextState == 0 {
				nextState = len(m.nodes)
				m.nodes = append(m.nodes, automatonNode{})
				m.setTransition(state, r, nextState)
			}
			state = nextState
		}
		if m.nodes[state].output == "" {
			m.nodes[state].output = word
		}
	}

	queue := make([]int, 0, len(m.nodes))
	m.forEachTransition(0, func(_ rune, nextState int) {
		queue = append(queue, nextState)
	})

	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]

		m.forEachTransition(state, func(r rune, nextState int) {
			queue = append(queue, nextState)

			fail := m.nodes[state].fail
			for fail != 0 {
				if fallback := m.transition(fail, r); fallback != 0 {
					m.nodes[nextState].fail = fallback
					break
				}
				fail = m.nodes[fail].fail
			}
			if fallback := m.transition(fail, r); fallback != 0 && state != 0 {
				m.nodes[nextState].fail = fallback
			}

			if m.nodes[nextState].output == "" {
				m.nodes[nextState].output = m.nodes[m.nodes[nextState].fail].output
			}
		})
	}

	return m
}

func (m *matcher) Match(text string) string {
	return m.MatchFields(text)
}

func (m *matcher) MatchFields(fields ...string) string {
	if m == nil {
		return ""
	}

	state := 0
	needsSeparator := false
	for _, text := range fields {
		if text == "" {
			continue
		}
		if needsSeparator {
			if matched := m.advanceASCII(' ', &state); matched != "" {
				return matched
			}
		}
		needsSeparator = true

		for index := 0; index < len(text); {
			if text[index] < utf8.RuneSelf {
				if matched := m.advanceASCII(foldMatcherByte(text[index]), &state); matched != "" {
					return matched
				}
				index++
				continue
			}

			r, size := utf8.DecodeRuneInString(text[index:])
			if matched := m.advanceRune(foldMatcherRune(r), &state); matched != "" {
				return matched
			}
			index += size
		}
	}

	return ""
}

func (m *matcher) advanceASCII(b byte, state *int) string {
	current := *state
	for current != 0 && m.nodes[current].ascii[b] == 0 {
		current = m.nodes[current].fail
	}

	current = m.nodes[current].ascii[b]
	*state = current
	return m.nodes[current].output
}

func (m *matcher) advanceRune(r rune, state *int) string {
	current := *state
	for current != 0 {
		if m.transition(current, r) != 0 {
			break
		}
		current = m.nodes[current].fail
	}

	current = m.transition(current, r)
	*state = current
	return m.nodes[current].output
}

func (m *matcher) transition(state int, r rune) int {
	if r >= 0 && r < utf8.RuneSelf {
		return m.nodes[state].ascii[byte(r)]
	}
	if m.nodes[state].next == nil {
		return 0
	}
	return m.nodes[state].next[r]
}

func (m *matcher) setTransition(state int, r rune, nextState int) {
	if r >= 0 && r < utf8.RuneSelf {
		m.nodes[state].ascii[byte(r)] = nextState
		return
	}
	if m.nodes[state].next == nil {
		m.nodes[state].next = make(map[rune]int)
	}
	m.nodes[state].next[r] = nextState
}

func (m *matcher) forEachTransition(state int, yield func(rune, int)) {
	for ascii, nextState := range m.nodes[state].ascii {
		if nextState != 0 {
			yield(rune(ascii), nextState)
		}
	}
	for r, nextState := range m.nodes[state].next {
		yield(r, nextState)
	}
}

func foldMatcherByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func foldMatcherRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return unicode.ToLower(r)
}
