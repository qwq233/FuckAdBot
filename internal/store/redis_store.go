package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qwq233/fuckadbot/internal/config"

	"github.com/redis/go-redis/v9"
)

type redisKeyspace struct {
	prefix string
}

func newRedisKeyspace(prefix string) redisKeyspace {
	return redisKeyspace{prefix: prefix}
}

func (k redisKeyspace) preference(userID int64) string {
	return fmt.Sprintf("%spreferences:%d", k.prefix, userID)
}

func (k redisKeyspace) status(chatID, userID int64) string {
	return fmt.Sprintf("%sstatus:%d:%d", k.prefix, chatID, userID)
}

func (k redisKeyspace) warning(chatID, userID int64) string {
	return fmt.Sprintf("%swarnings:%d:%d", k.prefix, chatID, userID)
}

func (k redisKeyspace) pending(chatID, userID int64) string {
	return fmt.Sprintf("%spending:%d:%d", k.prefix, chatID, userID)
}

func (k redisKeyspace) pendingIndex() string {
	return k.prefix + "pending:by-expire"
}

func (k redisKeyspace) pendingMember(chatID, userID int64) string {
	return fmt.Sprintf("%d:%d", chatID, userID)
}

func (k redisKeyspace) userChats(userID int64) string {
	return fmt.Sprintf("%suser:chats:%d", k.prefix, userID)
}

func (k redisKeyspace) blacklist(chatID int64) string {
	return fmt.Sprintf("%sblacklist:%d", k.prefix, chatID)
}

func (k redisKeyspace) blacklistScopes() string {
	return k.prefix + "blacklist:scopes"
}

type RedisStore struct {
	client   *redis.Client
	keyspace redisKeyspace
}

func NewRedisStore(cfg config.StoreConfig) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &RedisStore{
		client:   client,
		keyspace: newRedisKeyspace(cfg.RedisKeyPrefix),
	}, nil
}

func (s *RedisStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *RedisStore) GetUserLanguagePreference(userID int64) (string, error) {
	language, _, err := s.loadPreference(context.Background(), userID)
	return language, err
}

func (s *RedisStore) SetUserLanguagePreference(userID int64, language string) error {
	if strings.TrimSpace(language) == "" {
		return fmt.Errorf("preferred language cannot be empty")
	}
	return s.setPreference(context.Background(), userID, language)
}

func (s *RedisStore) IsVerified(chatID, userID int64) (bool, error) {
	status, _, err := s.loadStatus(context.Background(), chatID, userID)
	if err != nil {
		return false, err
	}
	return status == "verified", nil
}

func (s *RedisStore) SetVerified(chatID, userID int64) error {
	return s.setStatus(context.Background(), chatID, userID, "verified")
}

func (s *RedisStore) RemoveVerified(chatID, userID int64) error {
	status, found, err := s.loadStatus(context.Background(), chatID, userID)
	if err != nil || !found || status != "verified" {
		return err
	}
	return s.deleteStatus(context.Background(), chatID, userID)
}

func (s *RedisStore) IsRejected(chatID, userID int64) (bool, error) {
	status, _, err := s.loadStatus(context.Background(), chatID, userID)
	if err != nil {
		return false, err
	}
	return status == "rejected", nil
}

func (s *RedisStore) SetRejected(chatID, userID int64) error {
	return s.setStatus(context.Background(), chatID, userID, "rejected")
}

func (s *RedisStore) RemoveRejected(chatID, userID int64) error {
	status, found, err := s.loadStatus(context.Background(), chatID, userID)
	if err != nil || !found || status != "rejected" {
		return err
	}
	return s.deleteStatus(context.Background(), chatID, userID)
}

func (s *RedisStore) HasActivePending(chatID, userID int64) (bool, error) {
	pending, err := s.GetPending(chatID, userID)
	if err != nil || pending == nil {
		return false, err
	}
	return pending.ExpireAt.After(time.Now().UTC()), nil
}

func (s *RedisStore) GetPending(chatID, userID int64) (*PendingVerification, error) {
	pending, _, err := s.loadPending(context.Background(), chatID, userID)
	return pending, err
}

func (s *RedisStore) ListPendingVerifications() ([]PendingVerification, error) {
	ctx := context.Background()
	members, err := s.client.ZRange(ctx, s.keyspace.pendingIndex(), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	pendingVerifications := make([]PendingVerification, 0, len(members))
	for _, member := range members {
		chatID, userID, err := parsePendingMember(member)
		if err != nil {
			return nil, err
		}
		pending, found, err := s.loadPending(ctx, chatID, userID)
		if err != nil {
			return nil, err
		}
		if !found || pending == nil {
			continue
		}
		pendingVerifications = append(pendingVerifications, *pending)
	}

	sort.Slice(pendingVerifications, func(i, j int) bool {
		left := pendingVerifications[i]
		right := pendingVerifications[j]
		if !left.ExpireAt.Equal(right.ExpireAt) {
			return left.ExpireAt.Before(right.ExpireAt)
		}
		if left.ChatID != right.ChatID {
			return left.ChatID < right.ChatID
		}
		return left.UserID < right.UserID
	})

	return pendingVerifications, nil
}

func (s *RedisStore) ReserveVerificationWindow(pending PendingVerification, maxWarnings int) (VerificationReservationResult, error) {
	result := VerificationReservationResult{}
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	ctx := context.Background()
	raw, err := reserveVerificationWindowScript.Run(ctx, s.client, []string{
		s.keyspace.pending(pending.ChatID, pending.UserID),
		s.keyspace.status(pending.ChatID, pending.UserID),
		s.keyspace.warning(pending.ChatID, pending.UserID),
		s.keyspace.pendingIndex(),
		s.keyspace.userChats(pending.UserID),
	}, []any{
		s.keyspace.pendingMember(pending.ChatID, pending.UserID),
		pending.ChatID,
		pending.UserID,
		pending.UserLanguage,
		pending.Timestamp,
		pending.RandomToken,
		pending.ExpireAt.UTC().Unix(),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
		maxWarnings,
	}).Result()
	if err != nil {
		return result, err
	}

	values, err := resultToSlice(raw)
	if err != nil {
		return result, err
	}
	code, err := resultInt(values, 0)
	if err != nil {
		return result, err
	}
	result.WarningCount, err = resultInt(values, 1)
	if err != nil {
		return result, err
	}

	switch code {
	case 0:
		return result, nil
	case 1:
		result.Created = true
		return result, nil
	case 2:
		existing, err := pendingFromResultAt(pending.ChatID, pending.UserID, values, 2)
		if err != nil {
			return result, err
		}
		result.Existing = existing
		return result, nil
	case 3:
		result.LimitExceeded = true
		return result, nil
	default:
		return result, fmt.Errorf("unexpected reserve verification result code %d", code)
	}
}

func (s *RedisStore) CreatePendingIfAbsent(pending PendingVerification) (bool, *PendingVerification, error) {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	ctx := context.Background()
	result, err := createPendingIfAbsentScript.Run(ctx, s.client, []string{
		s.keyspace.pending(pending.ChatID, pending.UserID),
		s.keyspace.status(pending.ChatID, pending.UserID),
		s.keyspace.pendingIndex(),
		s.keyspace.userChats(pending.UserID),
	}, []any{
		s.keyspace.pendingMember(pending.ChatID, pending.UserID),
		pending.ChatID,
		pending.UserID,
		pending.UserLanguage,
		pending.Timestamp,
		pending.RandomToken,
		pending.ExpireAt.UTC().Unix(),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
	}).Result()
	if err != nil {
		return false, nil, err
	}

	values, err := resultToSlice(result)
	if err != nil {
		return false, nil, err
	}
	code, err := resultInt(values, 0)
	if err != nil {
		return false, nil, err
	}

	switch code {
	case 1:
		return true, nil, nil
	case 2:
		existing, err := s.GetPending(pending.ChatID, pending.UserID)
		return false, existing, err
	default:
		return false, nil, nil
	}
}

func (s *RedisStore) SetPending(pending PendingVerification) error {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}
	return s.setPendingRaw(context.Background(), pending)
}

func (s *RedisStore) UpdatePendingMetadataByToken(pending PendingVerification) (bool, error) {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	ctx := context.Background()
	result, err := updatePendingMetadataByTokenScript.Run(ctx, s.client, []string{
		s.keyspace.pending(pending.ChatID, pending.UserID),
		s.keyspace.pendingIndex(),
	}, []any{
		s.keyspace.pendingMember(pending.ChatID, pending.UserID),
		pending.Timestamp,
		pending.RandomToken,
		pending.UserLanguage,
		pending.ExpireAt.UTC().Unix(),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
	}).Result()
	if err != nil {
		return false, err
	}

	updated, err := asInt64(result)
	if err != nil {
		return false, err
	}
	return updated == 1, nil
}

func (s *RedisStore) ClearPending(chatID, userID int64) error {
	return s.deletePendingRaw(context.Background(), chatID, userID)
}

func (s *RedisStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action PendingAction, maxWarnings int) (PendingResolutionResult, error) {
	ctx := context.Background()
	result, err := resolvePendingByTokenScript.Run(ctx, s.client, []string{
		s.keyspace.pending(chatID, userID),
		s.keyspace.status(chatID, userID),
		s.keyspace.warning(chatID, userID),
		s.keyspace.pendingIndex(),
		s.keyspace.userChats(userID),
	}, []any{
		s.keyspace.pendingMember(chatID, userID),
		chatID,
		timestamp,
		randomToken,
		string(action),
		maxWarnings,
	}).Result()
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}

	values, err := resultToSlice(result)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}
	if len(values) == 0 {
		return PendingResolutionResult{Action: action}, nil
	}

	code, err := resultInt(values, 0)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}
	if code == -1 {
		return PendingResolutionResult{Action: action}, fmt.Errorf("unsupported pending action: %s", action)
	}
	if code == 0 {
		return PendingResolutionResult{Action: action}, nil
	}

	pending, err := pendingFromResult(chatID, userID, values)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}

	verified, err := resultBool(values, 10)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}
	rejected, err := resultBool(values, 11)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}
	warningCount, err := resultInt(values, 12)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}
	shouldBan, err := resultBool(values, 13)
	if err != nil {
		return PendingResolutionResult{Action: action}, err
	}

	return PendingResolutionResult{
		Matched:      true,
		Action:       action,
		Pending:      pending,
		Verified:     verified,
		Rejected:     rejected,
		WarningCount: warningCount,
		ShouldBan:    shouldBan,
	}, nil
}

func (s *RedisStore) ClearUserVerificationStateEverywhere(userID int64) error {
	return s.clearUserEverywhereCache(context.Background(), userID)
}

func (s *RedisStore) GetWarningCount(chatID, userID int64) (int, error) {
	count, _, err := s.loadWarning(context.Background(), chatID, userID)
	return count, err
}

func (s *RedisStore) IncrWarningCount(chatID, userID int64) (int, error) {
	ctx := context.Background()
	result, err := incrWarningCountScript.Run(ctx, s.client, []string{
		s.keyspace.warning(chatID, userID),
		s.keyspace.userChats(userID),
	}, []any{
		chatID,
	}).Result()
	if err != nil {
		return 0, err
	}
	count, err := asInt64(result)
	return int(count), err
}

func (s *RedisStore) ResetWarningCount(chatID, userID int64) error {
	return s.deleteWarning(context.Background(), chatID, userID)
}

func (s *RedisStore) GetBlacklistWords(chatID int64) ([]string, error) {
	words, _, err := s.loadBlacklist(context.Background(), chatID)
	return words, err
}

func (s *RedisStore) AddBlacklistWord(chatID int64, word, addedBy string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	ctx := context.Background()
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.SAdd(ctx, s.keyspace.blacklist(chatID), normalized)
		pipe.SAdd(ctx, s.keyspace.blacklistScopes(), strconv.FormatInt(chatID, 10))
		return nil
	})
	return err
}

func (s *RedisStore) RemoveBlacklistWord(chatID int64, word string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	ctx := context.Background()
	if err := s.client.SRem(ctx, s.keyspace.blacklist(chatID), normalized).Err(); err != nil {
		return err
	}
	count, err := s.client.SCard(ctx, s.keyspace.blacklist(chatID)).Result()
	if err != nil {
		return err
	}
	if count == 0 {
		_, err = s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, s.keyspace.blacklist(chatID))
			pipe.SRem(ctx, s.keyspace.blacklistScopes(), strconv.FormatInt(chatID, 10))
			return nil
		})
	}
	return err
}

func (s *RedisStore) GetAllBlacklistWords() (map[int64][]string, error) {
	ctx := context.Background()
	scopeIDs, err := s.client.SMembers(ctx, s.keyspace.blacklistScopes()).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[int64][]string, len(scopeIDs))
	for _, scopeID := range scopeIDs {
		chatID, err := strconv.ParseInt(scopeID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse blacklist scope id %q: %w", scopeID, err)
		}
		words, _, err := s.loadBlacklist(ctx, chatID)
		if err != nil {
			return nil, err
		}
		if len(words) == 0 {
			continue
		}
		result[chatID] = words
	}

	return result, nil
}

func (s *RedisStore) loadPreference(ctx context.Context, userID int64) (string, bool, error) {
	value, err := s.client.Get(ctx, s.keyspace.preference(userID)).Result()
	switch {
	case err == nil:
		return value, true, nil
	case err == redis.Nil:
		return "", false, nil
	default:
		return "", false, err
	}
}

func (s *RedisStore) setPreference(ctx context.Context, userID int64, language string) error {
	return s.client.Set(ctx, s.keyspace.preference(userID), language, 0).Err()
}

func (s *RedisStore) loadStatus(ctx context.Context, chatID, userID int64) (string, bool, error) {
	value, err := s.client.Get(ctx, s.keyspace.status(chatID, userID)).Result()
	switch {
	case err == nil:
		return value, true, nil
	case err == redis.Nil:
		return "", false, nil
	default:
		return "", false, err
	}
}

func (s *RedisStore) setStatus(ctx context.Context, chatID, userID int64, status string) error {
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, s.keyspace.status(chatID, userID), status, 0)
		pipe.SAdd(ctx, s.keyspace.userChats(userID), chatID)
		return nil
	})
	return err
}

func (s *RedisStore) deleteStatus(ctx context.Context, chatID, userID int64) error {
	return s.client.Del(ctx, s.keyspace.status(chatID, userID)).Err()
}

func (s *RedisStore) loadWarning(ctx context.Context, chatID, userID int64) (int, bool, error) {
	value, err := s.client.Get(ctx, s.keyspace.warning(chatID, userID)).Result()
	switch {
	case err == nil:
		count, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return 0, false, fmt.Errorf("parse warning count %q: %w", value, parseErr)
		}
		return count, true, nil
	case err == redis.Nil:
		return 0, false, nil
	default:
		return 0, false, err
	}
}

func (s *RedisStore) setWarning(ctx context.Context, chatID, userID int64, count int) error {
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, s.keyspace.warning(chatID, userID), strconv.Itoa(count), 0)
		pipe.SAdd(ctx, s.keyspace.userChats(userID), chatID)
		return nil
	})
	return err
}

func (s *RedisStore) deleteWarning(ctx context.Context, chatID, userID int64) error {
	return s.client.Del(ctx, s.keyspace.warning(chatID, userID)).Err()
}

func (s *RedisStore) loadPending(ctx context.Context, chatID, userID int64) (*PendingVerification, bool, error) {
	values, err := s.client.HGetAll(ctx, s.keyspace.pending(chatID, userID)).Result()
	if err != nil {
		return nil, false, err
	}
	if len(values) == 0 {
		return nil, false, nil
	}

	pending, err := pendingFromHash(values)
	if err != nil {
		return nil, false, err
	}
	return pending, true, nil
}

func (s *RedisStore) setPendingRaw(ctx context.Context, pending PendingVerification) error {
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, s.keyspace.pending(pending.ChatID, pending.UserID), pendingToHash(pending))
		pipe.ZAdd(ctx, s.keyspace.pendingIndex(), redis.Z{
			Score:  float64(pending.ExpireAt.UTC().Unix()),
			Member: s.keyspace.pendingMember(pending.ChatID, pending.UserID),
		})
		pipe.SAdd(ctx, s.keyspace.userChats(pending.UserID), pending.ChatID)
		return nil
	})
	return err
}

func (s *RedisStore) deletePendingRaw(ctx context.Context, chatID, userID int64) error {
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, s.keyspace.pending(chatID, userID))
		pipe.ZRem(ctx, s.keyspace.pendingIndex(), s.keyspace.pendingMember(chatID, userID))
		return nil
	})
	return err
}

func (s *RedisStore) loadBlacklist(ctx context.Context, chatID int64) ([]string, bool, error) {
	words, err := s.client.SMembers(ctx, s.keyspace.blacklist(chatID)).Result()
	if err != nil {
		return nil, false, err
	}
	if len(words) == 0 {
		return nil, false, nil
	}
	sort.Strings(words)
	return words, true, nil
}

func (s *RedisStore) replaceBlacklistScope(ctx context.Context, chatID int64, words []string) error {
	scopeKey := s.keyspace.blacklist(chatID)
	scopeID := strconv.FormatInt(chatID, 10)

	normalized := make([]any, 0, len(words))
	for _, word := range words {
		trimmed := normalizeBlacklistWord(word)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}

	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, scopeKey)
		if len(normalized) == 0 {
			pipe.SRem(ctx, s.keyspace.blacklistScopes(), scopeID)
			return nil
		}
		pipe.SAdd(ctx, scopeKey, normalized...)
		pipe.SAdd(ctx, s.keyspace.blacklistScopes(), scopeID)
		return nil
	})
	return err
}

func (s *RedisStore) clearUserEverywhereCache(ctx context.Context, userID int64) error {
	chatIDs, err := s.client.SMembers(ctx, s.keyspace.userChats(userID)).Result()
	if err != nil && err != redis.Nil {
		return err
	}

	keys := make([]string, 0, len(chatIDs)*3+1)
	members := make([]any, 0, len(chatIDs))
	for _, chatIDValue := range chatIDs {
		chatID, parseErr := strconv.ParseInt(chatIDValue, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("parse user chat scope %q: %w", chatIDValue, parseErr)
		}
		keys = append(keys,
			s.keyspace.status(chatID, userID),
			s.keyspace.warning(chatID, userID),
			s.keyspace.pending(chatID, userID),
		)
		members = append(members, s.keyspace.pendingMember(chatID, userID))
	}
	keys = append(keys, s.keyspace.userChats(userID))

	_, err = s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		if len(keys) > 0 {
			pipe.Del(ctx, keys...)
		}
		if len(members) > 0 {
			pipe.ZRem(ctx, s.keyspace.pendingIndex(), members...)
		}
		return nil
	})
	return err
}

func (s *RedisStore) clearPrefixedKeys(ctx context.Context) error {
	var cursor uint64
	pattern := s.keyspace.prefix + "*"
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := s.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func (s *RedisStore) applyUserStateSnapshotsBatch(ctx context.Context, snapshots map[userStateKey]UserStateSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}

	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for key, snapshot := range snapshots {
			pendingKey := s.keyspace.pending(key.ChatID, key.UserID)
			pendingMember := s.keyspace.pendingMember(key.ChatID, key.UserID)
			statusKey := s.keyspace.status(key.ChatID, key.UserID)
			warningKey := s.keyspace.warning(key.ChatID, key.UserID)
			userChatsKey := s.keyspace.userChats(key.UserID)

			if snapshot.Pending != nil {
				pipe.HSet(ctx, pendingKey, pendingToHash(*snapshot.Pending))
				pipe.ZAdd(ctx, s.keyspace.pendingIndex(), redis.Z{
					Score:  float64(snapshot.Pending.ExpireAt.UTC().Unix()),
					Member: pendingMember,
				})
				pipe.SAdd(ctx, userChatsKey, key.ChatID)
			} else {
				pipe.Del(ctx, pendingKey)
				pipe.ZRem(ctx, s.keyspace.pendingIndex(), pendingMember)
			}

			switch snapshot.Status {
			case "verified", "rejected":
				pipe.Set(ctx, statusKey, snapshot.Status, 0)
				pipe.SAdd(ctx, userChatsKey, key.ChatID)
			default:
				pipe.Del(ctx, statusKey)
			}

			if snapshot.WarningCount > 0 {
				pipe.Set(ctx, warningKey, strconv.Itoa(snapshot.WarningCount), 0)
				pipe.SAdd(ctx, userChatsKey, key.ChatID)
			} else {
				pipe.Del(ctx, warningKey)
			}
		}
		return nil
	})
	return err
}

func (s *RedisStore) applyUserLanguagePreferencesBatch(ctx context.Context, userIDs []int64, preferences map[int64]string) error {
	if len(userIDs) == 0 {
		return nil
	}

	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, userID := range userIDs {
			language := strings.TrimSpace(preferences[userID])
			if language == "" {
				pipe.Del(ctx, s.keyspace.preference(userID))
				continue
			}
			pipe.Set(ctx, s.keyspace.preference(userID), language, 0)
		}
		return nil
	})
	return err
}

func (s *RedisStore) rebuildFromSnapshot(source SnapshotStore) error {
	ctx := context.Background()
	if err := s.clearPrefixedKeys(ctx); err != nil {
		return err
	}

	preferences, err := source.ListUserLanguagePreferences()
	if err != nil {
		return err
	}
	for _, record := range preferences {
		if err := s.setPreference(ctx, record.UserID, record.Language); err != nil {
			return err
		}
	}

	statuses, err := source.ListUserStatuses()
	if err != nil {
		return err
	}
	for _, record := range statuses {
		if err := s.setStatus(ctx, record.ChatID, record.UserID, record.Status); err != nil {
			return err
		}
	}

	warnings, err := source.ListWarnings()
	if err != nil {
		return err
	}
	for _, record := range warnings {
		if err := s.setWarning(ctx, record.ChatID, record.UserID, record.Count); err != nil {
			return err
		}
	}

	pending, err := source.ListPendingVerifications()
	if err != nil {
		return err
	}
	for _, record := range pending {
		if err := s.setPendingRaw(ctx, record); err != nil {
			return err
		}
	}

	blacklists, err := source.GetAllBlacklistWords()
	if err != nil {
		return err
	}
	for chatID, words := range blacklists {
		if err := s.replaceBlacklistScope(ctx, chatID, words); err != nil {
			return err
		}
	}

	return nil
}

func pendingToHash(pending PendingVerification) map[string]any {
	return map[string]any{
		"chat_id":             pending.ChatID,
		"user_id":             pending.UserID,
		"user_language":       pending.UserLanguage,
		"timestamp":           pending.Timestamp,
		"random_token":        pending.RandomToken,
		"expire_at_unix":      pending.ExpireAt.UTC().Unix(),
		"reminder_message_id": pending.ReminderMessageID,
		"private_message_id":  pending.PrivateMessageID,
		"original_message_id": pending.OriginalMessageID,
		"message_thread_id":   pending.MessageThreadID,
		"reply_to_message_id": pending.ReplyToMessageID,
	}
}

func pendingFromHash(values map[string]string) (*PendingVerification, error) {
	chatID, err := parseRedisInt64(values["chat_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending chat_id: %w", err)
	}
	userID, err := parseRedisInt64(values["user_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending user_id: %w", err)
	}
	timestamp, err := parseRedisInt64(values["timestamp"])
	if err != nil {
		return nil, fmt.Errorf("parse pending timestamp: %w", err)
	}
	expireAtUnix, err := parseRedisInt64(values["expire_at_unix"])
	if err != nil {
		return nil, fmt.Errorf("parse pending expire_at_unix: %w", err)
	}
	reminderMessageID, err := parseRedisInt64(values["reminder_message_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending reminder_message_id: %w", err)
	}
	privateMessageID, err := parseRedisInt64(values["private_message_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending private_message_id: %w", err)
	}
	originalMessageID, err := parseRedisInt64(values["original_message_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending original_message_id: %w", err)
	}
	messageThreadID, err := parseRedisInt64(values["message_thread_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending message_thread_id: %w", err)
	}
	replyToMessageID, err := parseRedisInt64(values["reply_to_message_id"])
	if err != nil {
		return nil, fmt.Errorf("parse pending reply_to_message_id: %w", err)
	}

	userLanguage := strings.TrimSpace(values["user_language"])
	if userLanguage == "" {
		userLanguage = "zh-cn"
	}

	return &PendingVerification{
		ChatID:            chatID,
		UserID:            userID,
		UserLanguage:      userLanguage,
		Timestamp:         timestamp,
		RandomToken:       values["random_token"],
		ExpireAt:          time.Unix(expireAtUnix, 0).UTC(),
		ReminderMessageID: reminderMessageID,
		PrivateMessageID:  privateMessageID,
		OriginalMessageID: originalMessageID,
		MessageThreadID:   messageThreadID,
		ReplyToMessageID:  replyToMessageID,
	}, nil
}

func pendingFromResult(chatID, userID int64, values []any) (*PendingVerification, error) {
	return pendingFromResultAt(chatID, userID, values, 1)
}

func pendingFromResultAt(chatID, userID int64, values []any, offset int) (*PendingVerification, error) {
	timestamp, err := resultInt(values, offset+1)
	if err != nil {
		return nil, err
	}
	expireAtUnix, err := resultInt(values, offset+3)
	if err != nil {
		return nil, err
	}
	reminderMessageID, err := resultInt(values, offset+4)
	if err != nil {
		return nil, err
	}
	privateMessageID, err := resultInt(values, offset+5)
	if err != nil {
		return nil, err
	}
	originalMessageID, err := resultInt(values, offset+6)
	if err != nil {
		return nil, err
	}
	messageThreadID, err := resultInt(values, offset+7)
	if err != nil {
		return nil, err
	}
	replyToMessageID, err := resultInt(values, offset+8)
	if err != nil {
		return nil, err
	}

	userLanguage, err := resultString(values, offset)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userLanguage) == "" {
		userLanguage = "zh-cn"
	}
	randomToken, err := resultString(values, offset+2)
	if err != nil {
		return nil, err
	}

	return &PendingVerification{
		ChatID:            chatID,
		UserID:            userID,
		UserLanguage:      userLanguage,
		Timestamp:         int64(timestamp),
		RandomToken:       randomToken,
		ExpireAt:          time.Unix(int64(expireAtUnix), 0).UTC(),
		ReminderMessageID: int64(reminderMessageID),
		PrivateMessageID:  int64(privateMessageID),
		OriginalMessageID: int64(originalMessageID),
		MessageThreadID:   int64(messageThreadID),
		ReplyToMessageID:  int64(replyToMessageID),
	}, nil
}

func resultToSlice(result any) ([]any, error) {
	values, ok := result.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected redis result type %T", result)
	}
	return values, nil
}

func resultInt(values []any, index int) (int, error) {
	if index >= len(values) {
		return 0, fmt.Errorf("missing redis result index %d", index)
	}
	value, err := asInt64(values[index])
	return int(value), err
}

func resultBool(values []any, index int) (bool, error) {
	value, err := resultInt(values, index)
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

func resultString(values []any, index int) (string, error) {
	if index >= len(values) {
		return "", fmt.Errorf("missing redis result index %d", index)
	}
	switch typed := values[index].(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("unexpected redis result string type %T", values[index])
	}
}

func asInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected integer type %T", value)
	}
}

func parseRedisInt64(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func parsePendingMember(value string) (int64, int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid pending member %q", value)
	}
	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse pending chat id from %q: %w", value, err)
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse pending user id from %q: %w", value, err)
	}
	return chatID, userID, nil
}
