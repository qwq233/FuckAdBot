package store

import (
	"fmt"
	"strings"
)

const (
	sqliteUserStateBatchChunkSize      = 400
	sqliteUserPreferenceBatchChunkSize = 900
)

func (s *SQLiteStore) ListUserLanguagePreferences() ([]LanguagePreferenceRecord, error) {
	rows, err := s.db.Query(
		`SELECT user_id, preferred_language
		 FROM user_preferences
		 ORDER BY user_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list user language preferences: %w", err)
	}
	defer rows.Close()

	records := make([]LanguagePreferenceRecord, 0, 32)
	for rows.Next() {
		var record LanguagePreferenceRecord
		if err := rows.Scan(&record.UserID, &record.Language); err != nil {
			return nil, fmt.Errorf("scan user language preference: %w", err)
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *SQLiteStore) ListUserStatuses() ([]UserStatusRecord, error) {
	rows, err := s.db.Query(
		`SELECT chat_id, user_id, status
		 FROM user_status
		 ORDER BY chat_id ASC, user_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list user statuses: %w", err)
	}
	defer rows.Close()

	records := make([]UserStatusRecord, 0, 32)
	for rows.Next() {
		var record UserStatusRecord
		if err := rows.Scan(&record.ChatID, &record.UserID, &record.Status); err != nil {
			return nil, fmt.Errorf("scan user status: %w", err)
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *SQLiteStore) ListWarnings() ([]WarningRecord, error) {
	rows, err := s.db.Query(
		`SELECT chat_id, user_id, count
		 FROM warnings
		 ORDER BY chat_id ASC, user_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list warnings: %w", err)
	}
	defer rows.Close()

	records := make([]WarningRecord, 0, 32)
	for rows.Next() {
		var record WarningRecord
		if err := rows.Scan(&record.ChatID, &record.UserID, &record.Count); err != nil {
			return nil, fmt.Errorf("scan warning: %w", err)
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

func (s *SQLiteStore) loadUserStateSnapshots(keys []userStateKey) (map[userStateKey]UserStateSnapshot, error) {
	result := make(map[userStateKey]UserStateSnapshot, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	unique := uniqueUserStateKeys(keys)
	for _, key := range unique {
		result[key] = UserStateSnapshot{}
	}

	for start := 0; start < len(unique); start += sqliteUserStateBatchChunkSize {
		end := start + sqliteUserStateBatchChunkSize
		if end > len(unique) {
			end = len(unique)
		}
		if err := s.loadUserStateSnapshotChunk(unique[start:end], result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (s *SQLiteStore) loadUserStateSnapshotChunk(keys []userStateKey, result map[userStateKey]UserStateSnapshot) error {
	clause, args := buildUserStateKeyWhere(keys)

	pendingRows, err := s.db.Query(
		`SELECT `+pendingVerificationColumns+`
		 FROM pending_verifications
		 WHERE `+clause,
		args...,
	)
	if err != nil {
		return fmt.Errorf("load pending snapshots: %w", err)
	}
	defer pendingRows.Close()

	for pendingRows.Next() {
		pending, found, err := scanPendingValue(pendingRows)
		if err != nil {
			return fmt.Errorf("scan pending snapshot: %w", err)
		}
		if !found {
			continue
		}
		key := userStateKey{ChatID: pending.ChatID, UserID: pending.UserID}
		snapshot := result[key]
		pendingCopy := pending
		snapshot.Pending = &pendingCopy
		result[key] = snapshot
	}
	if err := pendingRows.Err(); err != nil {
		return fmt.Errorf("iterate pending snapshots: %w", err)
	}

	statusRows, err := s.db.Query(
		`SELECT chat_id, user_id, status
		 FROM user_status
		 WHERE `+clause,
		args...,
	)
	if err != nil {
		return fmt.Errorf("load status snapshots: %w", err)
	}
	defer statusRows.Close()

	for statusRows.Next() {
		var (
			chatID int64
			userID int64
			status string
		)
		if err := statusRows.Scan(&chatID, &userID, &status); err != nil {
			return fmt.Errorf("scan status snapshot: %w", err)
		}
		key := userStateKey{ChatID: chatID, UserID: userID}
		snapshot := result[key]
		snapshot.Status = status
		result[key] = snapshot
	}
	if err := statusRows.Err(); err != nil {
		return fmt.Errorf("iterate status snapshots: %w", err)
	}

	warningRows, err := s.db.Query(
		`SELECT chat_id, user_id, count
		 FROM warnings
		 WHERE `+clause,
		args...,
	)
	if err != nil {
		return fmt.Errorf("load warning snapshots: %w", err)
	}
	defer warningRows.Close()

	for warningRows.Next() {
		var (
			chatID int64
			userID int64
			count  int
		)
		if err := warningRows.Scan(&chatID, &userID, &count); err != nil {
			return fmt.Errorf("scan warning snapshot: %w", err)
		}
		key := userStateKey{ChatID: chatID, UserID: userID}
		snapshot := result[key]
		snapshot.WarningCount = count
		result[key] = snapshot
	}
	if err := warningRows.Err(); err != nil {
		return fmt.Errorf("iterate warning snapshots: %w", err)
	}

	return nil
}

func (s *SQLiteStore) loadUserLanguagePreferencesBatch(userIDs []int64) (map[int64]string, error) {
	result := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}

	unique := uniqueUserIDs(userIDs)
	for start := 0; start < len(unique); start += sqliteUserPreferenceBatchChunkSize {
		end := start + sqliteUserPreferenceBatchChunkSize
		if end > len(unique) {
			end = len(unique)
		}

		clause, args := buildInt64InClause(unique[start:end])
		rows, err := s.db.Query(
			`SELECT user_id, preferred_language
			 FROM user_preferences
			 WHERE user_id IN (`+clause+`)`,
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("load language preference batch: %w", err)
		}

		for rows.Next() {
			var (
				userID   int64
				language string
			)
			if err := rows.Scan(&userID, &language); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan language preference batch: %w", err)
			}
			result[userID] = language
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate language preference batch: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close language preference batch rows: %w", err)
		}
	}

	return result, nil
}

func buildUserStateKeyWhere(keys []userStateKey) (string, []any) {
	var builder strings.Builder
	args := make([]any, 0, len(keys)*2)
	for i, key := range keys {
		if i > 0 {
			builder.WriteString(" OR ")
		}
		builder.WriteString("(chat_id = ? AND user_id = ?)")
		args = append(args, key.ChatID, key.UserID)
	}
	return builder.String(), args
}

func buildInt64InClause(values []int64) (string, []any) {
	var builder strings.Builder
	args := make([]any, 0, len(values))
	for i, value := range values {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteByte('?')
		args = append(args, value)
	}
	return builder.String(), args
}

func uniqueUserStateKeys(keys []userStateKey) []userStateKey {
	seen := make(map[userStateKey]struct{}, len(keys))
	unique := make([]userStateKey, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	return unique
}

func uniqueUserIDs(userIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(userIDs))
	unique := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	return unique
}
