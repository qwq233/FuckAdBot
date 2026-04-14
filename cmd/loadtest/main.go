package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
	"github.com/qwq233/fuckadbot/internal/testingcfg"
)

type profileSpec struct {
	duration time.Duration
	rate     int
}

var profiles = map[string]profileSpec{
	"smoke":     {duration: time.Minute, rate: 5},
	"steady":    {duration: 10 * time.Minute, rate: 15},
	"burst":     {duration: time.Minute, rate: 30},
	"medium-60": {duration: 5 * time.Minute, rate: 60},
	"high-100":  {duration: 5 * time.Minute, rate: 100},
	"soak":      {duration: 24 * time.Hour, rate: 5},
}

func main() {
	profileName := flag.String("profile", "smoke", "load profile: smoke, steady, burst, medium-60, high-100, soak")
	storeType := flag.String("store", "sqlite", "store backend: sqlite, redis, dual-write")
	rateOverride := flag.Int("rate", 0, "override operations/messages per second for the selected profile")
	durationOverride := flag.Duration("duration", 0, "override profile duration")
	cooldown := flag.Duration("cooldown", time.Second, "post-load drain wait before collecting final runtime stats")
	flag.Parse()

	profile, ok := profiles[*profileName]
	if !ok {
		failf("unsupported profile %q", *profileName)
	}
	if *rateOverride > 0 {
		profile.rate = *rateOverride
	}
	if *durationOverride > 0 {
		profile.duration = *durationOverride
	}
	if profile.rate <= 0 {
		failf("rate must be > 0")
	}
	if profile.duration <= 0 {
		failf("duration must be > 0")
	}

	st, cleanup := mustCreateStore(*storeType)
	defer cleanup()

	if err := seedBlacklist(st); err != nil {
		failf("seed blacklist: %v", err)
	}

	results := runSyntheticLoad(context.Background(), st, profile)
	if *cooldown > 0 {
		time.Sleep(*cooldown)
	}
	printResults(*profileName, *storeType, profile, *cooldown, results, st)
}

type loadResult struct {
	totalOps   int64
	errors     int64
	panics     int64
	latencies  []time.Duration
	startedAt  time.Time
	finishedAt time.Time
}

func runSyntheticLoad(ctx context.Context, st store.Store, profile profileSpec) loadResult {
	workers := max(1, profile.rate/2)
	ops := make(chan int64, profile.rate*2)
	latencyCh := make(chan time.Duration, profile.rate*2)
	done := make(chan struct{})

	var result loadResult
	result.startedAt = time.Now()

	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for seq := range ops {
				start := time.Now()
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							atomic.AddInt64(&result.panics, 1)
						}
					}()

					if err := syntheticModerationOperation(st, seq); err != nil {
						atomic.AddInt64(&result.errors, 1)
					}
				}()
				atomic.AddInt64(&result.totalOps, 1)
				latencyCh <- time.Since(start)
			}
		}()
	}

	go func() {
		defer close(done)
		interval := time.Second / time.Duration(profile.rate)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		timeoutCtx, cancel := context.WithTimeout(ctx, profile.duration)
		defer cancel()

		var seq int64
		for {
			select {
			case <-timeoutCtx.Done():
				close(ops)
				waitGroup.Wait()
				close(latencyCh)
				return
			case <-ticker.C:
				seq++
				ops <- seq
			}
		}
	}()

	for latency := range latencyCh {
		result.latencies = append(result.latencies, latency)
	}
	<-done
	result.finishedAt = time.Now()
	return result
}

func syntheticModerationOperation(st store.Store, seq int64) error {
	chatID := -100123 - (seq % 4)
	userID := (seq % 512) + 1
	now := time.Now().UTC()
	pending := store.PendingVerification{
		ChatID:       chatID,
		UserID:       userID,
		UserLanguage: "en",
		Timestamp:    now.UnixNano(),
		RandomToken:  fmt.Sprintf("token-%d", seq),
		ExpireAt:     now.Add(5 * time.Minute),
	}

	created, existing, err := st.CreatePendingIfAbsent(pending)
	if err != nil {
		return err
	}

	if _, err := st.GetBlacklistWords(chatID); err != nil {
		return err
	}

	if created {
		switch seq % 8 {
		case 0:
			_, err = st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, store.PendingActionApprove, 3)
		case 1:
			_, err = st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, store.PendingActionReject, 3)
		case 2:
			_, err = st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, store.PendingActionCancel, 3)
		case 3:
			_, err = st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, store.PendingActionExpire, 3)
		default:
			_, err = st.GetPending(pending.ChatID, pending.UserID)
			if err == nil {
				_, err = st.GetWarningCount(pending.ChatID, pending.UserID)
			}
		}
		return err
	}

	if existing != nil {
		if _, err := st.GetPending(existing.ChatID, existing.UserID); err != nil {
			return err
		}
	}
	_, err = st.GetWarningCount(chatID, userID)
	return err
}

func mustCreateStore(kind string) (store.Store, func()) {
	dataPath, err := os.MkdirTemp("", "fuckad-loadtest-*")
	if err != nil {
		failf("create temp dir: %v", err)
	}

	cleanup := func() {
		_ = os.RemoveAll(dataPath)
	}

	cfg := config.StoreConfig{
		Type:           "sqlite",
		DataPath:       dataPath,
		RedisKeyPrefix: "loadtest:",
	}

	redisCleanup := func() {}
	switch kind {
	case "sqlite":
	case "redis":
		redisCfg, redisResourceCleanup, redisErr := newLoadtestRedisConfig("redis")
		if redisErr != nil {
			cleanup()
			failf("create redis config: %v", redisErr)
		}
		cfg.Type = "redis"
		cfg.RedisAddr = redisCfg.RedisAddr
		cfg.RedisPassword = redisCfg.RedisPassword
		cfg.RedisDB = redisCfg.RedisDB
		cfg.RedisKeyPrefix = redisCfg.RedisKeyPrefix
		redisCleanup = redisResourceCleanup
	case "dual-write":
		redisCfg, redisResourceCleanup, redisErr := newLoadtestRedisConfig("dual-write")
		if redisErr != nil {
			cleanup()
			failf("create dual-write redis config: %v", redisErr)
		}
		cfg.Type = "sqlite"
		cfg.DualWriteEnabled = true
		cfg.RedisAddr = redisCfg.RedisAddr
		cfg.RedisPassword = redisCfg.RedisPassword
		cfg.RedisDB = redisCfg.RedisDB
		cfg.RedisKeyPrefix = redisCfg.RedisKeyPrefix
		redisCleanup = redisResourceCleanup
	default:
		cleanup()
		failf("unsupported store %q", kind)
	}
	cfg.Normalize()

	st, err := store.NewFromConfig(cfg)
	if err != nil {
		cleanup()
		failf("create store: %v", err)
	}

	return st, func() {
		_ = st.Close()
		redisCleanup()
		cleanup()
	}
}

func newLoadtestRedisConfig(scope string) (config.StoreConfig, func(), error) {
	cfg, path, err := testingcfg.Load()
	if err != nil {
		return config.StoreConfig{}, nil, err
	}

	if cfg.Redis.UseRealRedis() {
		prefix := cfg.Redis.ScopedKeyPrefix(
			"loadtest",
			scope,
			strconv.FormatInt(time.Now().UnixNano(), 36),
		)

		return config.StoreConfig{
				RedisAddr:      cfg.Redis.Addr,
				RedisPassword:  cfg.Redis.Password,
				RedisDB:        cfg.Redis.DB,
				RedisKeyPrefix: prefix,
			}, func() {
				ctx, cancel := testingcfg.RealRedisCleanupContext()
				defer cancel()
				if err := testingcfg.CleanupRedisPrefix(ctx, cfg.Redis, prefix); err != nil {
					fmt.Fprintf(os.Stderr, "cleanup redis prefix %q from %s after loadtest: %v\n", prefix, path, err)
				}
			}, nil
	}

	redisSrv, err := miniredis.Run()
	if err != nil {
		return config.StoreConfig{}, nil, fmt.Errorf("start miniredis: %w", err)
	}

	return config.StoreConfig{
		RedisAddr:      redisSrv.Addr(),
		RedisKeyPrefix: "loadtest:",
	}, redisSrv.Close, nil
}

func seedBlacklist(st store.Store) error {
	for i := 0; i < 32; i++ {
		if err := st.AddBlacklistWord(0, fmt.Sprintf("global-%02d", i), "loadtest"); err != nil {
			return err
		}
		if err := st.AddBlacklistWord(-100123, fmt.Sprintf("group-%02d", i), "loadtest"); err != nil {
			return err
		}
	}
	return nil
}

func printResults(profileName, storeType string, profile profileSpec, cooldown time.Duration, result loadResult, st store.Store) {
	duration := result.finishedAt.Sub(result.startedAt)
	sort.Slice(result.latencies, func(i, j int) bool { return result.latencies[i] < result.latencies[j] })

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	queueDepth := -1
	if reporter, ok := st.(store.RuntimeStatsReporter); ok {
		queueDepth = reporter.RuntimeStats().QueueDepth
	}

	fmt.Printf("profile=%s\n", profileName)
	fmt.Printf("store=%s\n", storeType)
	fmt.Printf("target_rate=%d\n", profile.rate)
	fmt.Printf("target_duration=%s\n", profile.duration)
	fmt.Printf("cooldown=%s\n", cooldown)
	fmt.Printf("duration=%s\n", duration.Round(time.Millisecond))
	fmt.Printf("ops=%d\n", result.totalOps)
	fmt.Printf("throughput=%.2f ops/s\n", float64(result.totalOps)/duration.Seconds())
	fmt.Printf("p50=%s\n", percentile(result.latencies, 0.50))
	fmt.Printf("p95=%s\n", percentile(result.latencies, 0.95))
	fmt.Printf("p99=%s\n", percentile(result.latencies, 0.99))
	fmt.Printf("goroutines=%d\n", runtime.NumGoroutine())
	fmt.Printf("rss_bytes=%d\n", mem.Sys)
	fmt.Printf("heap_bytes=%d\n", mem.HeapAlloc)
	fmt.Printf("queue_depth=%d\n", queueDepth)
	fmt.Printf("errors=%d\n", result.errors)
	fmt.Printf("panics=%d\n", result.panics)
}

func percentile(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(values))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
