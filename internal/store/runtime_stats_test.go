package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDualWriteStoreRuntimeStatsIncludesRecoveryAndQueueDepth(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	stats := st.RuntimeStats()
	if stats.Mode != "dual-write" {
		t.Fatalf("RuntimeStats().Mode = %q, want %q", stats.Mode, "dual-write")
	}
	if stats.LastReplayAt.IsZero() || stats.LastRebuildAt.IsZero() {
		t.Fatalf("RuntimeStats() = %+v, want startup replay and rebuild timestamps", stats)
	}
	if stats.QueueDepth != 0 {
		t.Fatalf("RuntimeStats().QueueDepth = %d, want 0", stats.QueueDepth)
	}

	if err := st.cache.client.Close(); err != nil {
		t.Fatalf("cache client close error = %v", err)
	}
	if err := st.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	flushAt := time.Now().UTC()
	processed, flushErr := st.flushQueue()
	st.recordFlush(flushAt, processed, time.Since(flushAt), flushErr)

	stats = st.RuntimeStats()
	if stats.QueueDepth != 1 {
		t.Fatalf("RuntimeStats().QueueDepth = %d, want 1 after enqueue", stats.QueueDepth)
	}
	if !stats.Degraded {
		t.Fatalf("RuntimeStats().Degraded = false, want true after failed flush: %+v", stats)
	}
}

func TestDualWriteStoreApplyEventRejectsInvalidPayloadAndKind(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)

	if err := st.applyEvent(dualWriteEvent{
		Kind:    dualWriteEventSyncUserState,
		Payload: []byte("not-json"),
	}); err == nil {
		t.Fatal("applyEvent() error = nil, want decode error")
	}

	if err := st.applyEvent(dualWriteEvent{
		Kind:    dualWriteEventKind("unknown"),
		Payload: []byte("{}"),
	}); err == nil {
		t.Fatal("applyEvent() error = nil, want unsupported kind error")
	}
}

func TestDualWriteStoreApplyEventSupportsLegacyJSONPayload(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	if err := st.primary.SetUserLanguagePreference(42, "en"); err != nil {
		t.Fatalf("SetUserLanguagePreference() error = %v", err)
	}

	payload, err := json.Marshal(dualWriteUserPayload{UserID: 42})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := st.applyEvent(dualWriteEvent{
		Kind:    dualWriteEventSyncPreference,
		Payload: payload,
	}); err != nil {
		t.Fatalf("applyEvent() error = %v", err)
	}

	language, found, err := st.cache.loadPreference(context.Background(), 42)
	if err != nil {
		t.Fatalf("cache.loadPreference() error = %v", err)
	}
	if !found || language != "en" {
		t.Fatalf("cache.loadPreference() = (%q, %v), want (en, true)", language, found)
	}
}

func TestDualWriteStoreRuntimeStatsDoesNotQueryQueueCount(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)

	if err := st.cache.client.Close(); err != nil {
		t.Fatalf("cache client close error = %v", err)
	}
	if err := st.SetVerified(-100123, 42); err != nil {
		t.Fatalf("SetVerified() error = %v", err)
	}

	flushAt := time.Now().UTC()
	processed, flushErr := st.flushQueue()
	st.recordFlush(flushAt, processed, time.Since(flushAt), flushErr)
	if flushErr == nil {
		t.Fatal("flushQueue() error = nil, want redis sync failure")
	}

	if err := st.queue.db.Close(); err != nil {
		t.Fatalf("queue.db.Close() error = %v", err)
	}

	stats := st.RuntimeStats()
	if stats.QueueDepth != 1 {
		t.Fatalf("RuntimeStats().QueueDepth = %d, want 1 from atomic depth tracking", stats.QueueDepth)
	}
	if stats.QueueDepthError != "" {
		t.Fatalf("RuntimeStats().QueueDepthError = %q, want empty because count query should not run", stats.QueueDepthError)
	}
}

func TestDualWriteStoreManualQueueSeedTracksDepthAndFlushes(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)
	stopDualWriteWorkerForBenchmark(t, st)

	events := make([]dualWriteQueueItem, 0, 4)
	for i := 0; i < 4; i++ {
		userID := int64(i + 1)
		if err := st.primary.SetUserLanguagePreference(userID, "en"); err != nil {
			t.Fatalf("SetUserLanguagePreference() error = %v", err)
		}

		payload, err := json.Marshal(dualWriteUserPayload{UserID: userID})
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		events = append(events, dualWriteQueueItem{
			Kind:      dualWriteEventSyncPreference,
			DedupeKey: dualWriteUserDedupeKey(userID),
			Payload:   payload,
		})
	}

	enqueueDualWriteBatch(t, st, events)

	stats := st.RuntimeStats()
	if stats.QueueDepth != len(events) {
		t.Fatalf("RuntimeStats().QueueDepth = %d, want %d after manual queue seed", stats.QueueDepth, len(events))
	}

	processed, err := st.flushQueue()
	if err != nil {
		t.Fatalf("flushQueue() error = %v", err)
	}
	if processed != len(events) {
		t.Fatalf("flushQueue() processed %d events, want %d", processed, len(events))
	}

	stats = st.RuntimeStats()
	if stats.QueueDepth != 0 {
		t.Fatalf("RuntimeStats().QueueDepth = %d, want 0 after flush", stats.QueueDepth)
	}

	depth, err := st.queue.Count()
	if err != nil {
		t.Fatalf("queue.Count() error = %v", err)
	}
	if depth != 0 {
		t.Fatalf("queue.Count() = %d, want 0 after flush", depth)
	}
}

func TestDualWriteStoreCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	st, _, _ := newDualWriteStoreForTest(t)

	if err := st.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := st.Close(); err != nil {
			t.Errorf("second Close() error = %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Close() did not return in time")
	}
}
