package store

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/testingcfg"
)

var benchmarkRedisRunID uint64

func benchmarkStoreBackends(b *testing.B, fn func(b *testing.B, name string, st Store)) {
	b.Helper()

	b.Run("sqlite", func(b *testing.B) {
		cfg := config.StoreConfig{Type: "sqlite", DataPath: b.TempDir()}
		st, err := NewFromConfig(cfg)
		if err != nil {
			b.Fatalf("NewFromConfig(sqlite) error = %v", err)
		}
		b.Cleanup(func() { _ = st.Close() })
		fn(b, "sqlite", st)
	})

	b.Run("redis", func(b *testing.B) {
		cfg := benchmarkRedisStoreConfig(b, "store-redis")
		cfg.Type = "redis"
		st, err := NewFromConfig(cfg)
		if err != nil {
			b.Fatalf("NewFromConfig(redis) error = %v", err)
		}
		b.Cleanup(func() { _ = st.Close() })
		fn(b, "redis", st)
	})

	b.Run("dual_write", func(b *testing.B) {
		cfg := benchmarkRedisStoreConfig(b, "store-dual-write")
		cfg.Type = "sqlite"
		cfg.DataPath = b.TempDir()
		cfg.DualWriteEnabled = true
		cfg.Normalize()
		st, err := NewDualWriteStore(cfg)
		if err != nil {
			b.Fatalf("NewDualWriteStore() error = %v", err)
		}
		stopDualWriteWorkerForBenchmark(b, st)
		b.Cleanup(func() { _ = st.Close() })
		fn(b, "dual_write", st)
	})
}

func benchmarkRedisStoreConfig(tb testing.TB, scope string) config.StoreConfig {
	tb.Helper()

	cfg, path, err := testingcfg.Load()
	if err != nil {
		tb.Fatalf("testingcfg.Load() error = %v", err)
	}

	if cfg.Redis.UseRealRedis() {
		prefix := cfg.Redis.ScopedKeyPrefix(
			"bench",
			scope,
			tb.Name(),
			strconv.FormatUint(atomic.AddUint64(&benchmarkRedisRunID, 1), 36),
		)
		scheduleRealRedisCleanup(tb, cfg.Redis, path, prefix)
		return config.StoreConfig{
			RedisAddr:      cfg.Redis.Addr,
			RedisPassword:  cfg.Redis.Password,
			RedisDB:        cfg.Redis.DB,
			RedisKeyPrefix: prefix,
		}
	}

	redisSrv := miniredis.RunT(tb)
	tb.Cleanup(redisSrv.Close)
	return config.StoreConfig{
		RedisAddr:      redisSrv.Addr(),
		RedisKeyPrefix: "bench:",
	}
}

func scheduleRealRedisCleanup(tb testing.TB, cfg testingcfg.RedisConfig, path, prefix string) {
	tb.Helper()

	tb.Cleanup(func() {
		ctx, cancel := testingcfg.RealRedisCleanupContext()
		defer cancel()
		if err := testingcfg.CleanupRedisPrefix(ctx, cfg, prefix); err != nil {
			tb.Logf("CleanupRedisPrefix(after, %s from %s) error = %v", prefix, path, err)
		}
	})
}

func stopDualWriteWorkerForBenchmark(tb testing.TB, st *DualWriteStore) {
	tb.Helper()

	close(st.stopCh)
	<-st.doneCh

	st.stopCh = make(chan struct{})
	st.doneCh = make(chan struct{})
	close(st.doneCh)
}

func enqueueDualWriteBatch(tb testing.TB, st *DualWriteStore, events []dualWriteQueueItem) {
	tb.Helper()

	inserted, err := st.queue.EnqueueBatch(events)
	if err != nil {
		tb.Fatalf("queue.EnqueueBatch() error = %v", err)
	}
	if inserted != len(events) {
		tb.Fatalf("queue.EnqueueBatch() inserted %d events, want %d", inserted, len(events))
	}
	if inserted > 0 {
		st.queueDepth.Add(int64(inserted))
	}
}

func seedDualWriteReplayBatch(b *testing.B, st *DualWriteStore, startUserID int64, size int) {
	b.Helper()

	events := make([]dualWriteQueueItem, 0, size)
	for i := 0; i < size; i++ {
		userID := startUserID + int64(i)
		payload, err := dualWriteEncodePayload(dualWriteEventSyncPreference, dualWriteUserPayload{UserID: userID})
		if err != nil {
			b.Fatalf("dualWriteEncodePayload() error = %v", err)
		}
		events = append(events, dualWriteQueueItem{
			Kind:      dualWriteEventSyncPreference,
			DedupeKey: dualWriteUserDedupeKey(userID),
			Payload:   payload,
		})
		if err := st.primary.SetUserLanguagePreference(userID, "en"); err != nil {
			b.Fatalf("SetUserLanguagePreference() error = %v", err)
		}
	}

	enqueueDualWriteBatch(b, st, events)
}

func seedDualWriteUserStateBatch(b *testing.B, st *DualWriteStore, chatID, startUserID int64, size int) {
	b.Helper()

	events := make([]dualWriteQueueItem, 0, size)
	for i := 0; i < size; i++ {
		userID := startUserID + int64(i)
		payload, err := dualWriteEncodePayload(dualWriteEventSyncUserState, dualWriteUserStatePayload{ChatID: chatID, UserID: userID})
		if err != nil {
			b.Fatalf("dualWriteEncodePayload() error = %v", err)
		}
		events = append(events, dualWriteQueueItem{
			Kind:      dualWriteEventSyncUserState,
			DedupeKey: dualWriteUserStateDedupeKey(chatID, userID),
			Payload:   payload,
		})
		if err := st.primary.SetVerified(chatID, userID); err != nil {
			b.Fatalf("SetVerified() error = %v", err)
		}
	}

	enqueueDualWriteBatch(b, st, events)
}

func BenchmarkCreatePendingIfAbsent(b *testing.B) {
	benchmarkStoreBackends(b, func(b *testing.B, _ string, st Store) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pending := PendingVerification{
				ChatID:       -100123,
				UserID:       int64(i + 1),
				UserLanguage: "en",
				Timestamp:    int64(i + 1),
				RandomToken:  fmt.Sprintf("token-%d", i),
				ExpireAt:     time.Now().UTC().Add(5 * time.Minute),
			}
			if _, _, err := st.CreatePendingIfAbsent(pending); err != nil {
				b.Fatalf("CreatePendingIfAbsent() error = %v", err)
			}
		}
	})
}

func BenchmarkResolvePendingByToken(b *testing.B) {
	actions := []PendingAction{
		PendingActionApprove,
		PendingActionReject,
		PendingActionExpire,
		PendingActionCancel,
	}

	for _, action := range actions {
		action := action
		b.Run(string(action), func(b *testing.B) {
			benchmarkStoreBackends(b, func(b *testing.B, _ string, st Store) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					pending := PendingVerification{
						ChatID:       -100123,
						UserID:       int64(i + 1),
						UserLanguage: "en",
						Timestamp:    int64(i + 1),
						RandomToken:  fmt.Sprintf("token-%d", i),
						ExpireAt:     time.Now().UTC().Add(5 * time.Minute),
					}
					if err := st.SetPending(pending); err != nil {
						b.Fatalf("SetPending() error = %v", err)
					}
					if _, err := st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, action, 3); err != nil {
						b.Fatalf("ResolvePendingByToken() error = %v", err)
					}
				}
			})
		})
	}
}

func BenchmarkGetPendingAndWarningCount(b *testing.B) {
	benchmarkStoreBackends(b, func(b *testing.B, _ string, st Store) {
		for i := 0; i < 256; i++ {
			pending := PendingVerification{
				ChatID:       -100123,
				UserID:       int64(i + 1),
				UserLanguage: "en",
				Timestamp:    int64(i + 1),
				RandomToken:  fmt.Sprintf("token-%d", i),
				ExpireAt:     time.Now().UTC().Add(5 * time.Minute),
			}
			if err := st.SetPending(pending); err != nil {
				b.Fatalf("SetPending() error = %v", err)
			}
			if _, err := st.IncrWarningCount(pending.ChatID, pending.UserID); err != nil {
				b.Fatalf("IncrWarningCount() error = %v", err)
			}
		}

		b.Run("GetPending", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := st.GetPending(-100123, int64(i%256+1)); err != nil {
					b.Fatalf("GetPending() error = %v", err)
				}
			}
		})

		b.Run("GetWarningCount", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := st.GetWarningCount(-100123, int64(i%256+1)); err != nil {
					b.Fatalf("GetWarningCount() error = %v", err)
				}
			}
		})
	})
}

func BenchmarkGetBlacklistWords(b *testing.B) {
	benchmarkStoreBackends(b, func(b *testing.B, _ string, st Store) {
		for i := 0; i < 128; i++ {
			if err := st.AddBlacklistWord(-100123, fmt.Sprintf("word-%03d", i), "bench"); err != nil {
				b.Fatalf("AddBlacklistWord() error = %v", err)
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := st.GetBlacklistWords(-100123); err != nil {
				b.Fatalf("GetBlacklistWords() error = %v", err)
			}
		}
	})
}

func BenchmarkDualWriteFlushQueue(b *testing.B) {
	cfg := benchmarkRedisStoreConfig(b, "dual-write-flush")
	cfg.Type = "sqlite"
	cfg.DataPath = b.TempDir()
	cfg.DualWriteEnabled = true
	cfg.Normalize()
	st, err := NewDualWriteStore(cfg)
	if err != nil {
		b.Fatalf("NewDualWriteStore() error = %v", err)
	}
	defer st.Close()
	stopDualWriteWorkerForBenchmark(b, st)

	const batchSize = 64
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		seedDualWriteUserStateBatch(b, st, -100123, int64(i*batchSize+1), batchSize)
		b.StartTimer()

		processed, err := st.flushQueue()
		if err != nil {
			b.Fatalf("flushQueue() error = %v", err)
		}
		if processed != batchSize {
			b.Fatalf("flushQueue() processed %d events, want %d", processed, batchSize)
		}
	}
}

func BenchmarkDualWriteReplay(b *testing.B) {
	cfg := benchmarkRedisStoreConfig(b, "dual-write-replay")
	cfg.Type = "sqlite"
	cfg.DataPath = b.TempDir()
	cfg.DualWriteEnabled = true
	cfg.Normalize()
	st, err := NewDualWriteStore(cfg)
	if err != nil {
		b.Fatalf("NewDualWriteStore() error = %v", err)
	}
	defer st.Close()
	stopDualWriteWorkerForBenchmark(b, st)

	const batchSize = 64
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		seedDualWriteReplayBatch(b, st, int64(i*batchSize+1), batchSize)
		b.StartTimer()

		processed, err := st.flushQueue()
		if err != nil {
			b.Fatalf("flushQueue() error = %v", err)
		}
		if processed != batchSize {
			b.Fatalf("flushQueue() processed %d events, want %d", processed, batchSize)
		}
	}
}

func BenchmarkDualWriteRuntimeStats(b *testing.B) {
	cfg := benchmarkRedisStoreConfig(b, "dual-write-runtime-stats")
	cfg.Type = "sqlite"
	cfg.DataPath = b.TempDir()
	cfg.DualWriteEnabled = true
	cfg.Normalize()
	st, err := NewDualWriteStore(cfg)
	if err != nil {
		b.Fatalf("NewDualWriteStore() error = %v", err)
	}
	defer st.Close()
	stopDualWriteWorkerForBenchmark(b, st)

	events := make([]dualWriteQueueItem, 0, 128)
	for i := 0; i < 128; i++ {
		payload, err := dualWriteEncodePayload(dualWriteEventClearUserState, dualWriteUserPayload{UserID: int64(i + 1)})
		if err != nil {
			b.Fatalf("dualWriteEncodePayload() error = %v", err)
		}
		events = append(events, dualWriteQueueItem{
			Kind:      dualWriteEventClearUserState,
			DedupeKey: dualWriteUserDedupeKey(int64(i + 1)),
			Payload:   payload,
		})
	}
	enqueueDualWriteBatch(b, st, events)
	if got := st.RuntimeStats().QueueDepth; got != len(events) {
		b.Fatalf("RuntimeStats().QueueDepth = %d, want %d before benchmark loop", got, len(events))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.RuntimeStats()
	}
}

func BenchmarkDualWriteClearUserCache(b *testing.B) {
	cfg := benchmarkRedisStoreConfig(b, "dual-write-clear-user-cache")
	cfg.Type = "sqlite"
	cfg.DataPath = b.TempDir()
	cfg.DualWriteEnabled = true
	cfg.DualWriteFlushInterval = "1h"
	cfg.DualWriteBatchSize = 100
	cfg.DualWriteMaxConsecutiveFailures = 5
	cfg.DualWriteMaxQueueDepth = 10000
	cfg.Normalize()
	st, err := NewDualWriteStore(cfg)
	if err != nil {
		b.Fatalf("NewDualWriteStore() error = %v", err)
	}
	defer st.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.cache.clearUserEverywhereCache(context.Background(), int64(i+1)); err != nil {
			b.Fatalf("clearUserEverywhereCache() error = %v", err)
		}
	}
}
