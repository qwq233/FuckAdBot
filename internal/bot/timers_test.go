package bot

import (
	"testing"
	"time"
)

func TestScheduleUserTimerRemovesTrackedTimerAfterFire(t *testing.T) {
	b := &Bot{
		timers: make(map[timerKey][]*time.Timer),
	}

	done := make(chan struct{})
	b.scheduleUserTimer(-100123, 42, 10*time.Millisecond, func() {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduleUserTimer() callback did not fire in time")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.timersMu.Lock()
		remaining := len(b.timers)
		b.timersMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("tracked timers were not cleaned up after firing")
}
