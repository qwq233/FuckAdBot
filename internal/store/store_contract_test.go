package store_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	configpkg "github.com/qwq233/fuckadbot/internal/config"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type storeFactory struct {
	name string
	new  func(t *testing.T) storepkg.Store
}

func contractStoreFactories() []storeFactory {
	return []storeFactory{
		{
			name: "sqlite",
			new: func(t *testing.T) storepkg.Store {
				t.Helper()

				st, err := storepkg.NewSQLiteStore(filepath.Join(t.TempDir(), "contract.db"))
				if err != nil {
					t.Fatalf("NewSQLiteStore() error = %v", err)
				}
				t.Cleanup(func() {
					_ = st.Close()
				})
				return st
			},
		},
		{
			name: "redis",
			new: func(t *testing.T) storepkg.Store {
				t.Helper()

				redisSrv := miniredis.RunT(t)
				cfg := configpkg.StoreConfig{
					Type:           "redis",
					DataPath:       t.TempDir(),
					RedisAddr:      redisSrv.Addr(),
					RedisKeyPrefix: fmt.Sprintf("contract:%s:", t.Name()),
				}
				cfg.Normalize()

				st, err := storepkg.NewRedisStore(cfg)
				if err != nil {
					t.Fatalf("NewRedisStore() error = %v", err)
				}
				t.Cleanup(func() {
					_ = st.Close()
				})
				return st
			},
		},
	}
}

func TestStoreContractPreferencesAndStatuses(t *testing.T) {
	t.Parallel()

	for _, factory := range contractStoreFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			st := factory.new(t)
			chatID := int64(-100123)
			userID := int64(42)

			if err := st.SetUserLanguagePreference(userID, "en"); err != nil {
				t.Fatalf("SetUserLanguagePreference() error = %v", err)
			}
			language, err := st.GetUserLanguagePreference(userID)
			if err != nil {
				t.Fatalf("GetUserLanguagePreference() error = %v", err)
			}
			if language != "en" {
				t.Fatalf("GetUserLanguagePreference() = %q, want %q", language, "en")
			}

			if err := st.SetVerified(chatID, userID); err != nil {
				t.Fatalf("SetVerified() error = %v", err)
			}
			verified, err := st.IsVerified(chatID, userID)
			if err != nil {
				t.Fatalf("IsVerified() error = %v", err)
			}
			if !verified {
				t.Fatal("IsVerified() = false, want true")
			}

			if err := st.SetRejected(chatID, userID); err != nil {
				t.Fatalf("SetRejected() error = %v", err)
			}
			rejected, err := st.IsRejected(chatID, userID)
			if err != nil {
				t.Fatalf("IsRejected() error = %v", err)
			}
			if !rejected {
				t.Fatal("IsRejected() = false, want true")
			}

			if err := st.RemoveRejected(chatID, userID); err != nil {
				t.Fatalf("RemoveRejected() error = %v", err)
			}
			rejected, err = st.IsRejected(chatID, userID)
			if err != nil {
				t.Fatalf("IsRejected() after remove error = %v", err)
			}
			if rejected {
				t.Fatal("IsRejected() = true, want false after remove")
			}
		})
	}
}

func TestStoreContractPendingLifecycle(t *testing.T) {
	t.Parallel()

	for _, factory := range contractStoreFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			st := factory.new(t)
			base := time.Now().UTC().Truncate(time.Second)
			pending := storepkg.PendingVerification{
				ChatID:            -100321,
				UserID:            77,
				UserLanguage:      "en",
				Timestamp:         1712300000,
				RandomToken:       "token-a",
				ExpireAt:          base.Add(5 * time.Minute),
				ReminderMessageID: 1,
				PrivateMessageID:  2,
				OriginalMessageID: 3,
				MessageThreadID:   4,
				ReplyToMessageID:  5,
			}

			created, existing, err := st.CreatePendingIfAbsent(pending)
			if err != nil {
				t.Fatalf("CreatePendingIfAbsent() error = %v", err)
			}
			if !created || existing != nil {
				t.Fatalf("CreatePendingIfAbsent() = (%v, %+v), want (true, nil)", created, existing)
			}

			created, existing, err = st.CreatePendingIfAbsent(pending)
			if err != nil {
				t.Fatalf("CreatePendingIfAbsent(existing) error = %v", err)
			}
			if created || existing == nil {
				t.Fatalf("CreatePendingIfAbsent(existing) = (%v, %+v), want (false, existing)", created, existing)
			}

			pending.UserLanguage = "zh-cn"
			pending.ExpireAt = pending.ExpireAt.Add(2 * time.Minute)
			pending.ReminderMessageID = 10
			pending.PrivateMessageID = 20
			pending.OriginalMessageID = 30
			pending.MessageThreadID = 40
			pending.ReplyToMessageID = 50

			updated, err := st.UpdatePendingMetadataByToken(pending)
			if err != nil {
				t.Fatalf("UpdatePendingMetadataByToken() error = %v", err)
			}
			if !updated {
				t.Fatal("UpdatePendingMetadataByToken() = false, want true")
			}

			gotPending, err := st.GetPending(pending.ChatID, pending.UserID)
			if err != nil {
				t.Fatalf("GetPending() error = %v", err)
			}
			if gotPending == nil {
				t.Fatal("GetPending() = nil, want value")
			}
			if gotPending.UserLanguage != "zh-cn" || gotPending.ReminderMessageID != 10 || gotPending.PrivateMessageID != 20 || gotPending.OriginalMessageID != 30 || gotPending.MessageThreadID != 40 || gotPending.ReplyToMessageID != 50 {
				t.Fatalf("GetPending() = %+v, want updated metadata", *gotPending)
			}

			pendingList, err := st.ListPendingVerifications()
			if err != nil {
				t.Fatalf("ListPendingVerifications() error = %v", err)
			}
			if len(pendingList) != 1 {
				t.Fatalf("len(ListPendingVerifications()) = %d, want 1", len(pendingList))
			}

			approved, err := st.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, storepkg.PendingActionApprove, 3)
			if err != nil {
				t.Fatalf("ResolvePendingByToken(approve) error = %v", err)
			}
			if !approved.Matched || !approved.Verified {
				t.Fatalf("ResolvePendingByToken(approve) = %+v, want matched verified result", approved)
			}

			verified, err := st.IsVerified(pending.ChatID, pending.UserID)
			if err != nil {
				t.Fatalf("IsVerified() after approve error = %v", err)
			}
			if !verified {
				t.Fatal("IsVerified() = false, want true after approve")
			}

			rejectedPending := pending
			rejectedPending.UserID = 78
			rejectedPending.RandomToken = "token-b"
			if err := st.SetPending(rejectedPending); err != nil {
				t.Fatalf("SetPending(reject) error = %v", err)
			}

			rejectedResult, err := st.ResolvePendingByToken(rejectedPending.ChatID, rejectedPending.UserID, rejectedPending.Timestamp, rejectedPending.RandomToken, storepkg.PendingActionReject, 3)
			if err != nil {
				t.Fatalf("ResolvePendingByToken(reject) error = %v", err)
			}
			if !rejectedResult.Matched || !rejectedResult.Rejected {
				t.Fatalf("ResolvePendingByToken(reject) = %+v, want matched rejected result", rejectedResult)
			}

			expiredPending := pending
			expiredPending.UserID = 79
			expiredPending.RandomToken = "token-c"
			if err := st.SetPending(expiredPending); err != nil {
				t.Fatalf("SetPending(expire) error = %v", err)
			}

			expiredResult, err := st.ResolvePendingByToken(expiredPending.ChatID, expiredPending.UserID, expiredPending.Timestamp, expiredPending.RandomToken, storepkg.PendingActionExpire, 2)
			if err != nil {
				t.Fatalf("ResolvePendingByToken(expire) error = %v", err)
			}
			if !expiredResult.Matched || expiredResult.WarningCount != 1 || expiredResult.ShouldBan {
				t.Fatalf("ResolvePendingByToken(expire) = %+v, want warning_count=1 and should_ban=false", expiredResult)
			}

			canceledPending := pending
			canceledPending.UserID = 80
			canceledPending.RandomToken = "token-d"
			if err := st.SetPending(canceledPending); err != nil {
				t.Fatalf("SetPending(cancel) error = %v", err)
			}

			canceledResult, err := st.ResolvePendingByToken(canceledPending.ChatID, canceledPending.UserID, canceledPending.Timestamp, canceledPending.RandomToken, storepkg.PendingActionCancel, 3)
			if err != nil {
				t.Fatalf("ResolvePendingByToken(cancel) error = %v", err)
			}
			if !canceledResult.Matched {
				t.Fatalf("ResolvePendingByToken(cancel) = %+v, want matched result", canceledResult)
			}

			gotPending, err = st.GetPending(canceledPending.ChatID, canceledPending.UserID)
			if err != nil {
				t.Fatalf("GetPending(cancel) error = %v", err)
			}
			if gotPending != nil {
				t.Fatalf("GetPending(cancel) = %+v, want nil", *gotPending)
			}
		})
	}
}

func TestStoreContractReserveVerificationWindow(t *testing.T) {
	t.Parallel()

	for _, factory := range contractStoreFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			st := factory.new(t)
			base := time.Now().UTC().Truncate(time.Second)
			pending := storepkg.PendingVerification{
				ChatID:       -100321,
				UserID:       77,
				UserLanguage: "en",
				Timestamp:    1712300000,
				RandomToken:  "reserve-a",
				ExpireAt:     base.Add(5 * time.Minute),
			}

			reservation, err := st.ReserveVerificationWindow(pending, 3)
			if err != nil {
				t.Fatalf("ReserveVerificationWindow(create) error = %v", err)
			}
			if !reservation.Created || reservation.Existing != nil || reservation.LimitExceeded || reservation.WarningCount != 0 {
				t.Fatalf("ReserveVerificationWindow(create) = %+v, want created result", reservation)
			}

			reservation, err = st.ReserveVerificationWindow(pending, 3)
			if err != nil {
				t.Fatalf("ReserveVerificationWindow(existing) error = %v", err)
			}
			if reservation.Created || reservation.Existing == nil || reservation.LimitExceeded {
				t.Fatalf("ReserveVerificationWindow(existing) = %+v, want existing snapshot", reservation)
			}
			if reservation.Existing.RandomToken != pending.RandomToken {
				t.Fatalf("ReserveVerificationWindow(existing).Existing.RandomToken = %q, want %q", reservation.Existing.RandomToken, pending.RandomToken)
			}

			limitedPending := pending
			limitedPending.UserID = 78
			limitedPending.RandomToken = "reserve-b"
			if _, err := st.IncrWarningCount(limitedPending.ChatID, limitedPending.UserID); err != nil {
				t.Fatalf("IncrWarningCount(limit) error = %v", err)
			}
			if _, err := st.IncrWarningCount(limitedPending.ChatID, limitedPending.UserID); err != nil {
				t.Fatalf("IncrWarningCount(limit second) error = %v", err)
			}
			if _, err := st.IncrWarningCount(limitedPending.ChatID, limitedPending.UserID); err != nil {
				t.Fatalf("IncrWarningCount(limit third) error = %v", err)
			}

			reservation, err = st.ReserveVerificationWindow(limitedPending, 3)
			if err != nil {
				t.Fatalf("ReserveVerificationWindow(limit) error = %v", err)
			}
			if !reservation.LimitExceeded || reservation.Created || reservation.Existing != nil || reservation.WarningCount != 3 {
				t.Fatalf("ReserveVerificationWindow(limit) = %+v, want limit-exceeded warning_count=3", reservation)
			}

			gotPending, err := st.GetPending(limitedPending.ChatID, limitedPending.UserID)
			if err != nil {
				t.Fatalf("GetPending(limit) error = %v", err)
			}
			if gotPending != nil {
				t.Fatalf("GetPending(limit) = %+v, want nil", gotPending)
			}
		})
	}
}

func TestStoreContractWarningsBlacklistAndClearEverywhere(t *testing.T) {
	t.Parallel()

	for _, factory := range contractStoreFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()

			st := factory.new(t)
			userID := int64(42)
			chatA := int64(-100111)
			chatB := int64(-100222)

			count, err := st.IncrWarningCount(chatA, userID)
			if err != nil {
				t.Fatalf("IncrWarningCount() error = %v", err)
			}
			if count != 1 {
				t.Fatalf("IncrWarningCount() = %d, want 1", count)
			}
			count, err = st.IncrWarningCount(chatA, userID)
			if err != nil {
				t.Fatalf("IncrWarningCount(second) error = %v", err)
			}
			if count != 2 {
				t.Fatalf("IncrWarningCount(second) = %d, want 2", count)
			}

			if err := st.AddBlacklistWord(0, "global", "admin"); err != nil {
				t.Fatalf("AddBlacklistWord(global) error = %v", err)
			}
			if err := st.AddBlacklistWord(chatA, "group-a", "admin"); err != nil {
				t.Fatalf("AddBlacklistWord(group-a) error = %v", err)
			}
			if err := st.AddBlacklistWord(chatA, "group-b", "admin"); err != nil {
				t.Fatalf("AddBlacklistWord(group-b) error = %v", err)
			}

			allWords, err := st.GetAllBlacklistWords()
			if err != nil {
				t.Fatalf("GetAllBlacklistWords() error = %v", err)
			}

			sort.Strings(allWords[chatA])
			if !reflect.DeepEqual(allWords[0], []string{"global"}) {
				t.Fatalf("GetAllBlacklistWords()[0] = %v, want [global]", allWords[0])
			}
			if !reflect.DeepEqual(allWords[chatA], []string{"group-a", "group-b"}) {
				t.Fatalf("GetAllBlacklistWords()[chatA] = %v, want both group words", allWords[chatA])
			}

			if err := st.SetVerified(chatA, userID); err != nil {
				t.Fatalf("SetVerified(chatA) error = %v", err)
			}
			if err := st.SetRejected(chatB, userID); err != nil {
				t.Fatalf("SetRejected(chatB) error = %v", err)
			}
			if _, err := st.IncrWarningCount(chatB, userID); err != nil {
				t.Fatalf("IncrWarningCount(chatB) error = %v", err)
			}
			if err := st.SetPending(storepkg.PendingVerification{
				ChatID:       chatA,
				UserID:       userID,
				UserLanguage: "en",
				Timestamp:    1,
				RandomToken:  "clear-a",
				ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
			}); err != nil {
				t.Fatalf("SetPending(chatA) error = %v", err)
			}
			if err := st.SetPending(storepkg.PendingVerification{
				ChatID:       chatB,
				UserID:       userID,
				UserLanguage: "en",
				Timestamp:    2,
				RandomToken:  "clear-b",
				ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
			}); err != nil {
				t.Fatalf("SetPending(chatB) error = %v", err)
			}

			if err := st.ClearUserVerificationStateEverywhere(userID); err != nil {
				t.Fatalf("ClearUserVerificationStateEverywhere() error = %v", err)
			}

			verified, err := st.IsVerified(chatA, userID)
			if err != nil {
				t.Fatalf("IsVerified() error = %v", err)
			}
			if verified {
				t.Fatal("IsVerified() = true, want false after clear")
			}

			rejected, err := st.IsRejected(chatB, userID)
			if err != nil {
				t.Fatalf("IsRejected() error = %v", err)
			}
			if rejected {
				t.Fatal("IsRejected() = true, want false after clear")
			}

			if count, err := st.GetWarningCount(chatA, userID); err != nil || count != 0 {
				t.Fatalf("GetWarningCount(chatA) = (%d, %v), want (0, nil)", count, err)
			}
			if count, err := st.GetWarningCount(chatB, userID); err != nil || count != 0 {
				t.Fatalf("GetWarningCount(chatB) = (%d, %v), want (0, nil)", count, err)
			}
		})
	}
}

func TestRedisStoreCreatePendingIfAbsentConcurrentOnlyOneWins(t *testing.T) {
	t.Parallel()

	redisSrv := miniredis.RunT(t)
	cfg := configpkg.StoreConfig{
		Type:           "redis",
		DataPath:       t.TempDir(),
		RedisAddr:      redisSrv.Addr(),
		RedisKeyPrefix: "redis-concurrent:",
	}
	cfg.Normalize()

	st, err := storepkg.NewRedisStore(cfg)
	if err != nil {
		t.Fatalf("NewRedisStore() error = %v", err)
	}
	defer st.Close()

	base := storepkg.PendingVerification{
		ChatID:       -100987,
		UserID:       123,
		UserLanguage: "en",
		ExpireAt:     time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}

	type createResult struct {
		created bool
		token   string
		err     error
	}

	results := make(chan createResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			pending := base
			pending.Timestamp = int64(i + 1)
			pending.RandomToken = fmt.Sprintf("token-%d", i)
			created, _, err := st.CreatePendingIfAbsent(pending)
			results <- createResult{created: created, token: pending.RandomToken, err: err}
		}()
	}

	wg.Wait()
	close(results)

	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("CreatePendingIfAbsent() error = %v", result.err)
		}
		if result.created {
			createdCount++
		}
	}

	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
}
