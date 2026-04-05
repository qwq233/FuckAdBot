package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS user_status (
			chat_id  INTEGER NOT NULL,
			user_id  INTEGER NOT NULL,
			status   TEXT NOT NULL CHECK(status IN ('verified','rejected')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS pending_verifications (
			chat_id   INTEGER NOT NULL,
			user_id   INTEGER NOT NULL,
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at DATETIME NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			message_thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS warnings (
			chat_id    INTEGER NOT NULL,
			user_id    INTEGER NOT NULL,
			count      INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS blacklist_words (
			word     TEXT PRIMARY KEY,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec %q: %w", q[:40], err)
		}
	}

	if err := s.ensurePendingVerificationColumns(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) ensurePendingVerificationColumns() error {
	requiredColumns := map[string]string{
		"token_timestamp":     "INTEGER NOT NULL DEFAULT 0",
		"token_rand":          "TEXT NOT NULL DEFAULT ''",
		"reminder_message_id": "INTEGER NOT NULL DEFAULT 0",
		"message_thread_id":   "INTEGER NOT NULL DEFAULT 0",
		"reply_to_message_id": "INTEGER NOT NULL DEFAULT 0",
	}

	rows, err := s.db.Query(`PRAGMA table_info(pending_verifications)`)
	if err != nil {
		return fmt.Errorf("pragma pending_verifications: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]struct{}, len(requiredColumns))
	for rows.Next() {
		var (
			cid       int
			name      string
			dataType  string
			notNull   int
			defaultV  sql.NullString
			primaryPK int
		)
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultV, &primaryPK); err != nil {
			return fmt.Errorf("scan pending_verifications columns: %w", err)
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pending_verifications columns: %w", err)
	}

	for columnName, definition := range requiredColumns {
		if _, ok := existing[columnName]; ok {
			continue
		}

		if _, err := s.db.Exec(fmt.Sprintf(
			`ALTER TABLE pending_verifications ADD COLUMN %s %s`,
			columnName,
			definition,
		)); err != nil {
			return fmt.Errorf("add %s to pending_verifications: %w", columnName, err)
		}
	}

	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- User status ---

func (s *SQLiteStore) IsVerified(chatID, userID int64) (bool, error) {
	return s.hasStatus(chatID, userID, "verified")
}

func (s *SQLiteStore) SetVerified(chatID, userID int64) error {
	return s.setStatus(chatID, userID, "verified")
}

func (s *SQLiteStore) RemoveVerified(chatID, userID int64) error {
	return s.removeStatus(chatID, userID, "verified")
}

func (s *SQLiteStore) IsRejected(chatID, userID int64) (bool, error) {
	return s.hasStatus(chatID, userID, "rejected")
}

func (s *SQLiteStore) SetRejected(chatID, userID int64) error {
	return s.setStatus(chatID, userID, "rejected")
}

func (s *SQLiteStore) RemoveRejected(chatID, userID int64) error {
	return s.removeStatus(chatID, userID, "rejected")
}

func (s *SQLiteStore) hasStatus(chatID, userID int64, status string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM user_status WHERE chat_id = ? AND user_id = ? AND status = ?`,
		chatID, userID, status,
	).Scan(&count)
	return count > 0, err
}

func (s *SQLiteStore) setStatus(chatID, userID int64, status string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_status (chat_id, user_id, status, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		chatID, userID, status,
	)
	return err
}

func (s *SQLiteStore) removeStatus(chatID, userID int64, status string) error {
	_, err := s.db.Exec(
		`DELETE FROM user_status WHERE chat_id = ? AND user_id = ? AND status = ?`,
		chatID, userID, status,
	)
	return err
}

// --- Pending verification ---

func (s *SQLiteStore) HasActivePending(chatID, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pending_verifications WHERE chat_id = ? AND user_id = ? AND expire_at > datetime('now')`,
		chatID, userID,
	).Scan(&count)
	return count > 0, err
}

func (s *SQLiteStore) GetPending(chatID, userID int64) (*PendingVerification, error) {
	var pending PendingVerification
	var expireAt string
	err := s.db.QueryRow(
		`SELECT chat_id, user_id, token_timestamp, token_rand, expire_at, reminder_message_id, message_thread_id, reply_to_message_id
		 FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	).Scan(
		&pending.ChatID,
		&pending.UserID,
		&pending.Timestamp,
		&pending.RandomToken,
		&expireAt,
		&pending.ReminderMessageID,
		&pending.MessageThreadID,
		&pending.ReplyToMessageID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	pending.ExpireAt, err = parseSQLiteTime(expireAt)
	if err != nil {
		return nil, fmt.Errorf("parse pending expire_at: %w", err)
	}

	return &pending, nil
}

func (s *SQLiteStore) SetPending(pending PendingVerification) error {
	_, err := s.db.Exec(
		`INSERT INTO pending_verifications (
			chat_id,
			user_id,
			token_timestamp,
			token_rand,
			expire_at,
			reminder_message_id,
			message_thread_id,
			reply_to_message_id
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET
		 	token_timestamp = excluded.token_timestamp,
		 	token_rand = excluded.token_rand,
		 	expire_at = excluded.expire_at,
		 	reminder_message_id = excluded.reminder_message_id,
		 	message_thread_id = excluded.message_thread_id,
		 	reply_to_message_id = excluded.reply_to_message_id`,
		pending.ChatID,
		pending.UserID,
		pending.Timestamp,
		pending.RandomToken,
		pending.ExpireAt.UTC().Format(time.DateTime),
		pending.ReminderMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
	)
	return err
}

func (s *SQLiteStore) ClearPending(chatID, userID int64) error {
	_, err := s.db.Exec(
		`DELETE FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	return err
}

// --- Warnings ---

func (s *SQLiteStore) GetWarningCount(chatID, userID int64) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COALESCE((SELECT count FROM warnings WHERE chat_id = ? AND user_id = ?), 0)`,
		chatID, userID,
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) IncrWarningCount(chatID, userID int64) (int, error) {
	_, err := s.db.Exec(
		`INSERT INTO warnings (chat_id, user_id, count, updated_at)
		 VALUES (?, ?, 1, datetime('now'))
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET count = count + 1, updated_at = datetime('now')`,
		chatID, userID,
	)
	if err != nil {
		return 0, err
	}
	return s.GetWarningCount(chatID, userID)
}

func (s *SQLiteStore) ResetWarningCount(chatID, userID int64) error {
	_, err := s.db.Exec(
		`DELETE FROM warnings WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	return err
}

// --- Blacklist ---

func (s *SQLiteStore) GetBlacklistWords() ([]string, error) {
	rows, err := s.db.Query(`SELECT word FROM blacklist_words`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

func (s *SQLiteStore) AddBlacklistWord(word, addedBy string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	_, err := s.db.Exec(
		`INSERT INTO blacklist_words (word, added_by, added_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(word) DO NOTHING`,
		normalized, addedBy,
	)
	return err
}

func (s *SQLiteStore) RemoveBlacklistWord(word string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	_, err := s.db.Exec(`DELETE FROM blacklist_words WHERE lower(trim(word)) = ?`, normalized)
	return err
}

func normalizeBlacklistWord(word string) string {
	return strings.ToLower(strings.TrimSpace(word))
}

func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.DateTime, time.RFC3339, time.RFC3339Nano} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported time format %q", value)
}
