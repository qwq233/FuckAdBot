package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"
)

type dualWriteUserStatePayload struct {
	ChatID int64 `json:"chat_id"`
	UserID int64 `json:"user_id"`
}

type dualWriteUserPayload struct {
	UserID int64 `json:"user_id"`
}

type dualWriteBlacklistPayload struct {
	ChatID int64 `json:"chat_id"`
}

const (
	dualWriteFlushInterval  = 25 * time.Millisecond
	dualWriteBatchSize      = 512
	dualWriteWarnQueueDepth = 10000
	dualWriteMaxQueueDepth  = 50000
)

var dualWriteLegacyConfigWarning sync.Once

type DualWriteStore struct {
	cfg     config.StoreConfig
	primary *SQLiteStore
	cache   *RedisStore
	queue   *dualWriteQueue

	wakeCh    chan struct{}
	errCh     chan error
	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once
	statsMu   sync.RWMutex
	stats     RuntimeStats

	queueDepth    atomic.Int64
	bufferSinceNs atomic.Int64

	bufferMu sync.Mutex
	buffer   map[string]dualWriteQueueItem
}

func NewDualWriteStore(cfg config.StoreConfig) (*DualWriteStore, error) {
	primary, err := NewSQLiteStore(cfg.SQLitePath())
	if err != nil {
		return nil, err
	}

	cache, err := NewRedisStore(cfg)
	if err != nil {
		_ = primary.Close()
		return nil, err
	}

	queue, err := newDualWriteQueue(cfg.DualWriteQueuePath())
	if err != nil {
		_ = cache.Close()
		_ = primary.Close()
		return nil, err
	}

	warnIfUsingLegacyDualWriteTuning(cfg)

	store := &DualWriteStore{
		cfg:     cfg,
		primary: primary,
		cache:   cache,
		queue:   queue,
		wakeCh:  make(chan struct{}, 1),
		errCh:   make(chan error, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		buffer:  make(map[string]dualWriteQueueItem),
		stats: RuntimeStats{
			Mode: "dual-write",
		},
	}

	if err := store.recoverCache(); err != nil {
		_ = queue.Close()
		_ = cache.Close()
		_ = primary.Close()
		return nil, err
	}

	go store.runQueueWorker()
	return store, nil
}

func (s *DualWriteStore) Errors() <-chan error {
	return s.errCh
}

func (s *DualWriteStore) RuntimeStats() RuntimeStats {
	s.statsMu.RLock()
	snapshot := s.stats
	s.statsMu.RUnlock()

	depth := int(s.queueDepth.Load())
	snapshot.QueueDepth = depth
	snapshot.QueueDepthError = ""
	if depth > dualWriteMaxQueueDepth {
		snapshot.Degraded = true
		snapshot.QueueDepthError = fmt.Sprintf("queue depth %d exceeded hard limit %d", depth, dualWriteMaxQueueDepth)
		return snapshot
	}
	if depth > dualWriteWarnQueueDepth {
		snapshot.Degraded = true
	}
	if snapshot.LastFlushError != "" {
		snapshot.Degraded = true
	}
	if snapshot.LastReplayError != "" || snapshot.LastRebuildError != "" {
		snapshot.Degraded = true
	}
	return snapshot
}

func (s *DualWriteStore) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh

		flushAt := time.Now().UTC()
		processed, err := s.flushQueue()
		s.recordFlush(flushAt, processed, time.Since(flushAt), err)
		if err != nil && closeErr == nil {
			closeErr = err
		}

		if err := s.queue.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		if err := s.cache.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		if err := s.primary.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (s *DualWriteStore) GetUserLanguagePreference(userID int64) (string, error) {
	language, found, err := s.cache.loadPreference(context.Background(), userID)
	if err == nil && found {
		return language, nil
	}

	language, err = s.primary.GetUserLanguagePreference(userID)
	if err != nil {
		return "", err
	}

	s.enqueuePreferenceSync(userID)
	return language, nil
}

func (s *DualWriteStore) SetUserLanguagePreference(userID int64, language string) error {
	if err := s.primary.SetUserLanguagePreference(userID, language); err != nil {
		return err
	}
	s.enqueuePreferenceSync(userID)
	return nil
}

func (s *DualWriteStore) IsVerified(chatID, userID int64) (bool, error) {
	status, found, err := s.cache.loadStatus(context.Background(), chatID, userID)
	if err == nil && found {
		return status == "verified", nil
	}

	verified, err := s.primary.IsVerified(chatID, userID)
	if err != nil {
		return false, err
	}

	s.enqueueUserStateSync(chatID, userID)
	return verified, nil
}

func (s *DualWriteStore) SetVerified(chatID, userID int64) error {
	if err := s.primary.SetVerified(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) RemoveVerified(chatID, userID int64) error {
	if err := s.primary.RemoveVerified(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) IsRejected(chatID, userID int64) (bool, error) {
	status, found, err := s.cache.loadStatus(context.Background(), chatID, userID)
	if err == nil && found {
		return status == "rejected", nil
	}

	rejected, err := s.primary.IsRejected(chatID, userID)
	if err != nil {
		return false, err
	}

	s.enqueueUserStateSync(chatID, userID)
	return rejected, nil
}

func (s *DualWriteStore) SetRejected(chatID, userID int64) error {
	if err := s.primary.SetRejected(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) RemoveRejected(chatID, userID int64) error {
	if err := s.primary.RemoveRejected(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) HasActivePending(chatID, userID int64) (bool, error) {
	pending, err := s.GetPending(chatID, userID)
	if err != nil || pending == nil {
		return false, err
	}
	return pending.ExpireAt.After(time.Now().UTC()), nil
}

func (s *DualWriteStore) GetPending(chatID, userID int64) (*PendingVerification, error) {
	pending, found, err := s.cache.loadPending(context.Background(), chatID, userID)
	if err == nil && found {
		return pending, nil
	}

	pending, err = s.primary.GetPending(chatID, userID)
	if err != nil {
		return nil, err
	}

	s.enqueueUserStateSync(chatID, userID)
	return pending, nil
}

func (s *DualWriteStore) ReserveVerificationWindow(pending PendingVerification, maxWarnings int) (VerificationReservationResult, error) {
	result, err := s.primary.ReserveVerificationWindow(pending, maxWarnings)
	if err != nil {
		return result, err
	}
	if result.Created {
		s.enqueueUserStateSync(pending.ChatID, pending.UserID)
	}
	return result, nil
}

func (s *DualWriteStore) ListPendingVerifications() ([]PendingVerification, error) {
	return s.primary.ListPendingVerifications()
}

func (s *DualWriteStore) CreatePendingIfAbsent(pending PendingVerification) (bool, *PendingVerification, error) {
	created, existing, err := s.primary.CreatePendingIfAbsent(pending)
	if err != nil {
		return false, nil, err
	}
	s.enqueueUserStateSync(pending.ChatID, pending.UserID)
	return created, existing, nil
}

func (s *DualWriteStore) SetPending(pending PendingVerification) error {
	if err := s.primary.SetPending(pending); err != nil {
		return err
	}
	s.enqueueUserStateSync(pending.ChatID, pending.UserID)
	return nil
}

func (s *DualWriteStore) UpdatePendingMetadataByToken(pending PendingVerification) (bool, error) {
	updated, err := s.primary.UpdatePendingMetadataByToken(pending)
	if err != nil {
		return false, err
	}
	if updated {
		s.enqueueUserStateSync(pending.ChatID, pending.UserID)
	}
	return updated, nil
}

func (s *DualWriteStore) ClearPending(chatID, userID int64) error {
	if err := s.primary.ClearPending(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action PendingAction, maxWarnings int) (PendingResolutionResult, error) {
	result, err := s.primary.ResolvePendingByToken(chatID, userID, timestamp, randomToken, action, maxWarnings)
	if err != nil {
		return result, err
	}
	if result.Matched {
		s.enqueueUserStateSync(chatID, userID)
	}
	return result, nil
}

func (s *DualWriteStore) ClearUserVerificationStateEverywhere(userID int64) error {
	if err := s.primary.ClearUserVerificationStateEverywhere(userID); err != nil {
		return err
	}
	s.enqueueClearUserStateSync(userID)
	return nil
}

func (s *DualWriteStore) GetWarningCount(chatID, userID int64) (int, error) {
	count, found, err := s.cache.loadWarning(context.Background(), chatID, userID)
	if err == nil && found {
		return count, nil
	}

	count, err = s.primary.GetWarningCount(chatID, userID)
	if err != nil {
		return 0, err
	}

	s.enqueueUserStateSync(chatID, userID)
	return count, nil
}

func (s *DualWriteStore) IncrWarningCount(chatID, userID int64) (int, error) {
	count, err := s.primary.IncrWarningCount(chatID, userID)
	if err != nil {
		return 0, err
	}
	s.enqueueUserStateSync(chatID, userID)
	return count, nil
}

func (s *DualWriteStore) ResetWarningCount(chatID, userID int64) error {
	if err := s.primary.ResetWarningCount(chatID, userID); err != nil {
		return err
	}
	s.enqueueUserStateSync(chatID, userID)
	return nil
}

func (s *DualWriteStore) GetBlacklistWords(chatID int64) ([]string, error) {
	words, found, err := s.cache.loadBlacklist(context.Background(), chatID)
	if err == nil && found {
		return words, nil
	}

	words, err = s.primary.GetBlacklistWords(chatID)
	if err != nil {
		return nil, err
	}

	s.enqueueBlacklistScopeSync(chatID)
	return words, nil
}

func (s *DualWriteStore) AddBlacklistWord(chatID int64, word, addedBy string) error {
	if err := s.primary.AddBlacklistWord(chatID, word, addedBy); err != nil {
		return err
	}
	s.enqueueBlacklistScopeSync(chatID)
	return nil
}

func (s *DualWriteStore) RemoveBlacklistWord(chatID int64, word string) error {
	if err := s.primary.RemoveBlacklistWord(chatID, word); err != nil {
		return err
	}
	s.enqueueBlacklistScopeSync(chatID)
	return nil
}

func (s *DualWriteStore) GetAllBlacklistWords() (map[int64][]string, error) {
	return s.primary.GetAllBlacklistWords()
}

func (s *DualWriteStore) recoverCache() error {
	s.queueDepth.Store(0)

	if _, err := s.flushQueue(); err != nil {
		s.recordReplay(time.Now().UTC(), err)
		return fmt.Errorf("replay dual-write queue: %w", err)
	}
	s.recordReplay(time.Now().UTC(), nil)

	rebuildAt := time.Now().UTC()
	if err := s.cache.rebuildFromSnapshot(s.primary); err != nil {
		s.recordRebuild(rebuildAt, err)
		return fmt.Errorf("rebuild redis cache from sqlite: %w", err)
	}
	s.recordRebuild(rebuildAt, nil)
	if err := s.queue.Clear(); err != nil {
		return fmt.Errorf("clear dual-write queue after rebuild: %w", err)
	}
	s.queueDepth.Store(0)
	return nil
}

func (s *DualWriteStore) runQueueWorker() {
	ticker := time.NewTicker(dualWriteFlushInterval)
	defer func() {
		ticker.Stop()
		close(s.errCh)
		close(s.doneCh)
	}()

	for {
		select {
		case <-ticker.C:
			s.runFlushCycle()
		case <-s.wakeCh:
			if s.shouldFlushOnWake() {
				s.runFlushCycle()
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *DualWriteStore) flushQueue() (int, error) {
	if _, err := s.flushBufferedEvents(); err != nil {
		return 0, err
	}

	return s.drainQueue(true)
}

func (s *DualWriteStore) applyEvent(event dualWriteEvent) error {
	switch event.Kind {
	case dualWriteEventSyncUserState:
		payload, err := dualWriteDecodeUserStatePayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode user-state event: %w", err)
		}
		return s.syncUserState(payload.ChatID, payload.UserID)
	case dualWriteEventSyncPreference:
		payload, err := dualWriteDecodeUserPayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode preference event: %w", err)
		}
		return s.syncPreference(payload.UserID)
	case dualWriteEventSyncBlacklistScope:
		payload, err := dualWriteDecodeBlacklistPayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode blacklist event: %w", err)
		}
		return s.syncBlacklistScope(payload.ChatID)
	case dualWriteEventClearUserState:
		payload, err := dualWriteDecodeUserPayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode clear-user event: %w", err)
		}
		return s.cache.clearUserEverywhereCache(context.Background(), payload.UserID)
	default:
		return fmt.Errorf("unsupported dual-write event kind: %s", event.Kind)
	}
}

func (s *DualWriteStore) syncUserState(chatID, userID int64) error {
	ctx := context.Background()

	pending, err := s.primary.GetPending(chatID, userID)
	if err != nil {
		return err
	}
	status, err := s.primary.currentStatus(chatID, userID)
	if err != nil {
		return err
	}
	warnings, err := s.primary.GetWarningCount(chatID, userID)
	if err != nil {
		return err
	}

	if pending != nil {
		if err := s.cache.setPendingRaw(ctx, *pending); err != nil {
			return err
		}
	} else {
		if err := s.cache.deletePendingRaw(ctx, chatID, userID); err != nil {
			return err
		}
	}

	switch {
	case status == "verified":
		if err := s.cache.setStatus(ctx, chatID, userID, "verified"); err != nil {
			return err
		}
	case status == "rejected":
		if err := s.cache.setStatus(ctx, chatID, userID, "rejected"); err != nil {
			return err
		}
	default:
		if err := s.cache.deleteStatus(ctx, chatID, userID); err != nil {
			return err
		}
	}

	if warnings > 0 {
		if err := s.cache.setWarning(ctx, chatID, userID, warnings); err != nil {
			return err
		}
	} else {
		if err := s.cache.deleteWarning(ctx, chatID, userID); err != nil {
			return err
		}
	}

	return nil
}

func (s *DualWriteStore) syncPreference(userID int64) error {
	ctx := context.Background()
	language, err := s.primary.GetUserLanguagePreference(userID)
	if err != nil {
		return err
	}
	if language == "" {
		return s.cache.client.Del(ctx, s.cache.keyspace.preference(userID)).Err()
	}
	return s.cache.setPreference(ctx, userID, language)
}

func (s *DualWriteStore) syncBlacklistScope(chatID int64) error {
	words, err := s.primary.GetBlacklistWords(chatID)
	if err != nil {
		return err
	}
	return s.cache.replaceBlacklistScope(context.Background(), chatID, words)
}

func (s *DualWriteStore) applyUserStateBatch(events []dualWriteEvent) error {
	keys := make([]userStateKey, 0, len(events))
	for _, event := range events {
		payload, err := dualWriteDecodeUserStatePayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode user-state event: %w", err)
		}
		keys = append(keys, userStateKey{ChatID: payload.ChatID, UserID: payload.UserID})
	}

	snapshots, err := s.primary.loadUserStateSnapshots(keys)
	if err != nil {
		return err
	}
	return s.cache.applyUserStateSnapshotsBatch(context.Background(), snapshots)
}

func (s *DualWriteStore) applyPreferenceBatch(events []dualWriteEvent) error {
	userIDs := make([]int64, 0, len(events))
	for _, event := range events {
		payload, err := dualWriteDecodeUserPayload(event.Payload)
		if err != nil {
			return fmt.Errorf("decode preference event: %w", err)
		}
		userIDs = append(userIDs, payload.UserID)
	}

	preferences, err := s.primary.loadUserLanguagePreferencesBatch(userIDs)
	if err != nil {
		return err
	}
	return s.cache.applyUserLanguagePreferencesBatch(context.Background(), userIDs, preferences)
}

func (s *DualWriteStore) enqueueUserStateSync(chatID, userID int64) {
	s.enqueueBuffered(dualWriteQueueItem{
		Kind:      dualWriteEventSyncUserState,
		DedupeKey: dualWriteUserStateDedupeKey(chatID, userID),
	}, dualWriteUserStatePayload{ChatID: chatID, UserID: userID}, userID, false)
}

func (s *DualWriteStore) enqueuePreferenceSync(userID int64) {
	s.enqueueBuffered(dualWriteQueueItem{
		Kind:      dualWriteEventSyncPreference,
		DedupeKey: dualWriteUserDedupeKey(userID),
	}, dualWriteUserPayload{UserID: userID}, userID, false)
}

func (s *DualWriteStore) enqueueBlacklistScopeSync(chatID int64) {
	s.enqueueBuffered(dualWriteQueueItem{
		Kind:      dualWriteEventSyncBlacklistScope,
		DedupeKey: dualWriteBlacklistScopeDedupeKey(chatID),
	}, dualWriteBlacklistPayload{ChatID: chatID}, 0, false)
}

func (s *DualWriteStore) enqueueClearUserStateSync(userID int64) {
	s.enqueueBuffered(dualWriteQueueItem{
		Kind:      dualWriteEventClearUserState,
		DedupeKey: dualWriteUserDedupeKey(userID),
	}, dualWriteUserPayload{UserID: userID}, userID, true)
}

func (s *DualWriteStore) recordFlush(at time.Time, batchSize int, duration time.Duration, err error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.LastFlushAt = at
	s.stats.LastFlushBatchSize = batchSize
	s.stats.LastFlushDuration = duration
	if err != nil {
		s.stats.Degraded = true
		s.stats.LastFlushError = err.Error()
		return
	}
	s.stats.Degraded = false
	s.stats.LastFlushError = ""
}

func (s *DualWriteStore) recordReplay(at time.Time, err error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.LastReplayAt = at
	if err != nil {
		s.stats.LastReplayError = err.Error()
		return
	}
	s.stats.LastReplayError = ""
}

func (s *DualWriteStore) recordRebuild(at time.Time, err error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.LastRebuildAt = at
	if err != nil {
		s.stats.LastRebuildError = err.Error()
		return
	}
	s.stats.LastRebuildError = ""
}

func warnIfUsingLegacyDualWriteTuning(cfg config.StoreConfig) {
	if !cfg.HasLegacyDualWriteTuning() {
		return
	}

	dualWriteLegacyConfigWarning.Do(func() {
		log.Printf("[store] Warning: dual_write_* tuning keys are deprecated and ignored; built-in constants are used instead")
	})
}

func (s *DualWriteStore) runFlushCycle() {
	flushAt := time.Now().UTC()
	processed, err := s.flushCycle()
	s.recordFlush(flushAt, processed, time.Since(flushAt), err)
}

func (s *DualWriteStore) flushCycle() (int, error) {
	if _, err := s.flushBufferedEvents(); err != nil {
		return 0, err
	}

	return s.drainQueue(false)
}

func (s *DualWriteStore) drainQueue(full bool) (int, error) {
	processed := 0
	forceBatch := true
	for {
		depth := s.queueDepth.Load()
		if depth == 0 {
			return processed, nil
		}
		if !full && !forceBatch && depth <= dualWriteBatchSize {
			return processed, nil
		}

		batchProcessed, err := s.flushQueueBatch()
		processed += batchProcessed
		if err != nil {
			return processed, err
		}
		if batchProcessed == 0 {
			return processed, nil
		}
		forceBatch = false
	}
}

func (s *DualWriteStore) flushQueueBatch() (int, error) {
	events, err := s.queue.PeekBatch(dualWriteBatchSize)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}

	processedIDs := make([]int64, 0, len(events))
	for i := 0; i < len(events); {
		next := i + 1
		for next < len(events) && events[next].Kind == events[i].Kind {
			next++
		}

		var applyErr error
		switch events[i].Kind {
		case dualWriteEventSyncUserState:
			applyErr = s.applyUserStateBatch(events[i:next])
		case dualWriteEventSyncPreference:
			applyErr = s.applyPreferenceBatch(events[i:next])
		default:
			applyErr = s.applyEvent(events[i])
			next = i + 1
		}
		if applyErr != nil {
			return s.failFlushBatch(processedIDs, events[i].ID, applyErr)
		}

		for _, event := range events[i:next] {
			processedIDs = append(processedIDs, event.ID)
		}
		i = next
	}

	deleted, err := s.queue.DeleteBatch(processedIDs)
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		s.queueDepth.Add(-int64(deleted))
	}
	return deleted, nil
}

func (s *DualWriteStore) failFlushBatch(processedIDs []int64, failedID int64, err error) (int, error) {
	deleted, deleteErr := s.queue.DeleteBatch(processedIDs)
	if deleteErr != nil {
		return deleted, deleteErr
	}
	if deleted > 0 {
		s.queueDepth.Add(-int64(deleted))
	}

	markErr := s.queue.MarkFailed(failedID, err.Error())
	if markErr != nil {
		return deleted, errors.Join(err, markErr)
	}
	return deleted, err
}

func (s *DualWriteStore) flushBufferedEvents() (int, error) {
	events := s.takeBufferedEvents()
	if len(events) == 0 {
		return 0, nil
	}

	inserted, err := s.queue.EnqueueBatch(events)
	if err != nil {
		s.restoreBufferedEvents(events)
		return 0, err
	}
	if inserted > 0 {
		s.queueDepth.Add(int64(inserted))
	}

	return inserted, nil
}

func (s *DualWriteStore) takeBufferedEvents() []dualWriteQueueItem {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()

	if len(s.buffer) == 0 {
		s.bufferSinceNs.Store(0)
		return nil
	}

	events := make([]dualWriteQueueItem, 0, len(s.buffer))
	for key, event := range s.buffer {
		events = append(events, event)
		delete(s.buffer, key)
	}
	s.bufferSinceNs.Store(0)

	sort.Slice(events, func(i, j int) bool {
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].DedupeKey < events[j].DedupeKey
	})
	return events
}

func (s *DualWriteStore) restoreBufferedEvents(events []dualWriteQueueItem) {
	nowUnix := time.Now().UTC().UnixNano()

	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()

	for _, event := range events {
		if s.shouldSkipRestoredEventLocked(event) {
			continue
		}

		key := dualWriteBufferedMapKey(event.Kind, event.DedupeKey)
		if _, exists := s.buffer[key]; exists {
			continue
		}
		s.buffer[key] = event
		if s.bufferSinceNs.Load() == 0 {
			s.bufferSinceNs.Store(nowUnix)
		}
	}
}

func (s *DualWriteStore) shouldSkipRestoredEventLocked(event dualWriteQueueItem) bool {
	switch event.Kind {
	case dualWriteEventSyncUserState:
		userID, ok := dualWriteUserIDFromStateDedupeKey(event.DedupeKey)
		if !ok {
			return false
		}
		_, clearExists := s.buffer[dualWriteBufferedMapKey(dualWriteEventClearUserState, dualWriteUserDedupeKey(userID))]
		return clearExists
	case dualWriteEventClearUserState:
		userID, err := strconv.ParseInt(event.DedupeKey, 10, 64)
		if err != nil {
			return false
		}
		for _, buffered := range s.buffer {
			if buffered.Kind != dualWriteEventSyncUserState {
				continue
			}
			bufferedUserID, ok := dualWriteUserIDFromStateDedupeKey(buffered.DedupeKey)
			if ok && bufferedUserID == userID {
				return true
			}
		}
	}
	return false
}

func (s *DualWriteStore) enqueueBuffered(item dualWriteQueueItem, payload any, userID int64, clearUserState bool) {
	encoded, err := dualWriteEncodePayload(item.Kind, payload)
	if err != nil {
		s.recordFlush(time.Now().UTC(), 0, 0, fmt.Errorf("encode %s event: %w", item.Kind, err))
		return
	}
	item.Payload = encoded

	nowUnix := time.Now().UTC().UnixNano()

	s.bufferMu.Lock()
	if clearUserState {
		s.removeBufferedUserStateLocked(userID)
	}
	if item.Kind == dualWriteEventSyncUserState && s.hasBufferedClearUserStateLocked(userID) {
		s.bufferMu.Unlock()
		return
	}

	if len(s.buffer) == 0 {
		s.bufferSinceNs.Store(nowUnix)
	}
	s.buffer[dualWriteBufferedMapKey(item.Kind, item.DedupeKey)] = item
	s.bufferMu.Unlock()

	s.signalFlush()
}

func (s *DualWriteStore) removeBufferedUserStateLocked(userID int64) {
	for key, event := range s.buffer {
		if event.Kind != dualWriteEventSyncUserState {
			continue
		}
		bufferedUserID, ok := dualWriteUserIDFromStateDedupeKey(event.DedupeKey)
		if ok && bufferedUserID == userID {
			delete(s.buffer, key)
		}
	}
}

func (s *DualWriteStore) hasBufferedClearUserStateLocked(userID int64) bool {
	_, exists := s.buffer[dualWriteBufferedMapKey(dualWriteEventClearUserState, dualWriteUserDedupeKey(userID))]
	return exists
}

func (s *DualWriteStore) signalFlush() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *DualWriteStore) shouldFlushOnWake() bool {
	if s.queueDepth.Load() > dualWriteBatchSize {
		return true
	}

	sinceUnix := s.bufferSinceNs.Load()
	if sinceUnix == 0 {
		return false
	}
	return time.Since(time.Unix(0, sinceUnix)) >= dualWriteFlushInterval
}
