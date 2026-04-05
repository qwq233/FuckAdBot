package bot

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	pollingGetUpdatesTimeoutSeconds = 10
	pollingRequestTimeout           = 15 * time.Second
	updaterErrorLogInterval         = time.Hour
)

type updaterErrorThrottler struct {
	mu                    sync.Mutex
	lastDeadlineErrorTime time.Time
}

func newUpdaterErrorThrottler() *updaterErrorThrottler {
	return &updaterErrorThrottler{}
}

func (t *updaterErrorThrottler) Handle(err error) {
	if !isGetUpdatesDeadlineExceeded(err) {
		log.Printf("[bot] updater error: %v", err)
		return
	}

	if !t.shouldLogDeadlineError(time.Now()) {
		return
	}

	log.Printf("[bot] failed to get updates; sleeping 1s: %v (repeated context deadline exceeded logs suppressed for 1h)", err)
}

func (t *updaterErrorThrottler) shouldLogDeadlineError(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.lastDeadlineErrorTime.IsZero() && now.Sub(t.lastDeadlineErrorTime) < updaterErrorLogInterval {
		return false
	}

	t.lastDeadlineErrorTime = now
	return true
}

func isGetUpdatesDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}

	if !strings.Contains(err.Error(), "getUpdates") {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	return strings.Contains(err.Error(), "context deadline exceeded")
}
