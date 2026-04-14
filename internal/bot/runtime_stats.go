package bot

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const runtimeRecentErrorLimit = 20

type botRuntimeSnapshot struct {
	StartedAt           time.Time
	LastSweeperRunAt    time.Time
	LastSweeperDuration time.Duration
	LastSweeperScanned  int
	LastSweeperExpired  int
	AdminCacheHits      uint64
	AdminCacheMisses    uint64
	AdminCacheErrors    uint64
	RecentErrors        []string
}

type botRuntimeStats struct {
	startedAtUnix       atomic.Int64
	mu                  sync.RWMutex
	lastSweeperRunAt    time.Time
	lastSweeperDuration time.Duration
	lastSweeperScanned  int
	lastSweeperExpired  int
	adminCacheHits      atomic.Uint64
	adminCacheMisses    atomic.Uint64
	adminCacheErrors    atomic.Uint64
	recentErrors        []string
}

func (s *botRuntimeStats) ensureInitialized() {
	if s.startedAtUnix.Load() != 0 {
		return
	}
	s.startedAtUnix.CompareAndSwap(0, time.Now().UTC().UnixNano())
}

func (s *botRuntimeStats) snapshot() botRuntimeSnapshot {
	s.ensureInitialized()

	s.mu.RLock()
	defer s.mu.RUnlock()

	recentErrors := append([]string(nil), s.recentErrors...)
	startedAt := time.Unix(0, s.startedAtUnix.Load()).UTC()
	return botRuntimeSnapshot{
		StartedAt:           startedAt,
		LastSweeperRunAt:    s.lastSweeperRunAt,
		LastSweeperDuration: s.lastSweeperDuration,
		LastSweeperScanned:  s.lastSweeperScanned,
		LastSweeperExpired:  s.lastSweeperExpired,
		AdminCacheHits:      s.adminCacheHits.Load(),
		AdminCacheMisses:    s.adminCacheMisses.Load(),
		AdminCacheErrors:    s.adminCacheErrors.Load(),
		RecentErrors:        recentErrors,
	}
}

func (s *botRuntimeStats) recordAdminCacheHit() {
	s.ensureInitialized()
	s.adminCacheHits.Add(1)
}

func (s *botRuntimeStats) recordAdminCacheMiss() {
	s.ensureInitialized()
	s.adminCacheMisses.Add(1)
}

func (s *botRuntimeStats) recordAdminCacheError() {
	s.ensureInitialized()
	s.adminCacheErrors.Add(1)
}

func (s *botRuntimeStats) recordSweeperRun(startedAt time.Time, duration time.Duration, scanned, expired int) {
	s.ensureInitialized()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSweeperRunAt = startedAt
	s.lastSweeperDuration = duration
	s.lastSweeperScanned = scanned
	s.lastSweeperExpired = expired
}

func (s *botRuntimeStats) recordErrorf(format string, args ...any) {
	s.recordError(fmt.Sprintf(format, args...))
}

func (s *botRuntimeStats) recordError(message string) {
	s.ensureInitialized()
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339), message)
	s.recentErrors = append([]string{entry}, s.recentErrors...)
	if len(s.recentErrors) > runtimeRecentErrorLimit {
		s.recentErrors = s.recentErrors[:runtimeRecentErrorLimit]
	}
}
