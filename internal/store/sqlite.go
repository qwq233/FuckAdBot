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

const currentSchemaVersion = 3

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &SQLiteStore{db: db}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
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
			user_language TEXT NOT NULL DEFAULT 'zh-cn',
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at DATETIME NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			private_message_id INTEGER NOT NULL DEFAULT 0,
			original_message_id INTEGER NOT NULL DEFAULT 0,
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
			chat_id  INTEGER NOT NULL DEFAULT 0,
			word     TEXT NOT NULL,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, word)
		)`,
		`CREATE TABLE IF NOT EXISTS user_preferences (
			user_id INTEGER NOT NULL PRIMARY KEY,
			preferred_language TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec %q: %w", q[:40], err)
		}
	}

	if err := s.migrateSchemaVersion(); err != nil {
		return err
	}
	if err := s.ensureLegacyPendingVerificationColumns(); err != nil {
		return err
	}
	if err := s.migrateBlacklistTable(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) migrateSchemaVersion() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for version < currentSchemaVersion {
		switch version {
		case 0:
			if err := s.migrateToVersion1(); err != nil {
				return err
			}
			version = 1
		case 1:
			if err := s.migrateToVersion2(); err != nil {
				return err
			}
			version = 2
		case 2:
			if err := s.migrateToVersion3(); err != nil {
				return err
			}
			version = 3
		default:
			return fmt.Errorf("unsupported database schema version: %d", version)
		}
	}

	return nil
}

func (s *SQLiteStore) migrateToVersion1() error {
	if err := s.addPendingVerificationColumns(map[string]string{
		"original_message_id": "INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return fmt.Errorf("migrate database schema from version 0 to 1: %w", err)
	}

	if err := s.setSchemaVersion(1); err != nil {
		return fmt.Errorf("set user_version to 1: %w", err)
	}

	return nil
}

func (s *SQLiteStore) migrateToVersion2() error {
	if err := s.addPendingVerificationColumns(map[string]string{
		"user_language": "TEXT NOT NULL DEFAULT 'zh-cn'",
	}); err != nil {
		return fmt.Errorf("migrate database schema from version 1 to 2: %w", err)
	}

	if err := s.setSchemaVersion(2); err != nil {
		return fmt.Errorf("set user_version to 2: %w", err)
	}

	return nil
}

func (s *SQLiteStore) migrateToVersion3() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS user_preferences (
		user_id INTEGER NOT NULL PRIMARY KEY,
		preferred_language TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("migrate database schema from version 2 to 3: %w", err)
	}

	if err := s.setSchemaVersion(3); err != nil {
		return fmt.Errorf("set user_version to 3: %w", err)
	}

	return nil
}

func (s *SQLiteStore) ensureLegacyPendingVerificationColumns() error {
	requiredColumns := map[string]string{
		"token_timestamp":     "INTEGER NOT NULL DEFAULT 0",
		"token_rand":          "TEXT NOT NULL DEFAULT ''",
		"reminder_message_id": "INTEGER NOT NULL DEFAULT 0",
		"private_message_id":  "INTEGER NOT NULL DEFAULT 0",
		"message_thread_id":   "INTEGER NOT NULL DEFAULT 0",
		"reply_to_message_id": "INTEGER NOT NULL DEFAULT 0",
	}

	return s.addPendingVerificationColumns(requiredColumns)
}

func (s *SQLiteStore) setSchemaVersion(version int) error {
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPending(scanner rowScanner) (*PendingVerification, error) {
	var pending PendingVerification
	var expireAt string
	err := scanner.Scan(
		&pending.ChatID,
		&pending.UserID,
		&pending.UserLanguage,
		&pending.Timestamp,
		&pending.RandomToken,
		&expireAt,
		&pending.ReminderMessageID,
		&pending.PrivateMessageID,
		&pending.OriginalMessageID,
		&pending.MessageThreadID,
		&pending.ReplyToMessageID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	pending.ExpireAt, err = parseSQLiteTime(expireAt)
	if err != nil {
		return nil, fmt.Errorf("parse pending expire_at: %w", err)
	}

	return &pending, nil
}

func (s *SQLiteStore) addPendingVerificationColumns(requiredColumns map[string]string) error {
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

// --- User preferences ---

func (s *SQLiteStore) GetUserLanguagePreference(userID int64) (string, error) {
	var language string
	err := s.db.QueryRow(
		`SELECT preferred_language FROM user_preferences WHERE user_id = ?`,
		userID,
	).Scan(&language)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	return language, nil
}

func (s *SQLiteStore) SetUserLanguagePreference(userID int64, language string) error {
	if strings.TrimSpace(language) == "" {
		return fmt.Errorf("preferred language cannot be empty")
	}

	_, err := s.db.Exec(
		`INSERT INTO user_preferences (user_id, preferred_language, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET
		 	preferred_language = excluded.preferred_language,
		 	updated_at = excluded.updated_at`,
		userID,
		language,
	)
	return err
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

func (s *SQLiteStore) hasStatusTx(tx *sql.Tx, chatID, userID int64, status string) (bool, error) {
	var count int
	err := tx.QueryRow(
		`SELECT COUNT(*) FROM user_status WHERE chat_id = ? AND user_id = ? AND status = ?`,
		chatID, userID, status,
	).Scan(&count)
	return count > 0, err
}

func (s *SQLiteStore) setStatusTx(tx *sql.Tx, chatID, userID int64, status string) error {
	_, err := tx.Exec(
		`INSERT INTO user_status (chat_id, user_id, status, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		chatID, userID, status,
	)
	return err
}

func (s *SQLiteStore) removeStatusTx(tx *sql.Tx, chatID, userID int64, status string) error {
	_, err := tx.Exec(
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
	pending, err := scanPending(s.db.QueryRow(
		`SELECT chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		 FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	))
	if err != nil {
		return nil, err
	}
	return pending, nil
}

func (s *SQLiteStore) ListPendingVerifications() ([]PendingVerification, error) {
	rows, err := s.db.Query(
		`SELECT chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		 FROM pending_verifications
		 ORDER BY expire_at ASC, chat_id ASC, user_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pendingVerifications []PendingVerification
	for rows.Next() {
		pending, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		if pending == nil {
			continue
		}
		pendingVerifications = append(pendingVerifications, *pending)
	}

	return pendingVerifications, rows.Err()
}

func (s *SQLiteStore) CreatePendingIfAbsent(pending PendingVerification) (bool, *PendingVerification, error) {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()

	existing, err := scanPending(tx.QueryRow(
		`SELECT chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		 FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		pending.ChatID, pending.UserID,
	))
	if err != nil {
		return false, nil, err
	}
	if existing != nil {
		if existing.ExpireAt.After(time.Now().UTC()) {
			if err := tx.Commit(); err != nil {
				return false, nil, err
			}
			return false, existing, nil
		}

		if err := tx.Commit(); err != nil {
			return false, nil, err
		}
		return false, existing, nil
	}

	verified, err := s.hasStatusTx(tx, pending.ChatID, pending.UserID, "verified")
	if err != nil {
		return false, nil, err
	}
	if verified {
		if err := tx.Commit(); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	}

	rejected, err := s.hasStatusTx(tx, pending.ChatID, pending.UserID, "rejected")
	if err != nil {
		return false, nil, err
	}
	if rejected {
		if err := tx.Commit(); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	}

	if _, err := tx.Exec(
		`INSERT INTO pending_verifications (
			chat_id,
			user_id,
			user_language,
			token_timestamp,
			token_rand,
			expire_at,
			reminder_message_id,
			private_message_id,
			original_message_id,
			message_thread_id,
			reply_to_message_id
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pending.ChatID,
		pending.UserID,
		pending.UserLanguage,
		pending.Timestamp,
		pending.RandomToken,
		pending.ExpireAt.UTC().Format(time.DateTime),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
	); err != nil {
		return false, nil, err
	}

	if err := tx.Commit(); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func (s *SQLiteStore) SetPending(pending PendingVerification) error {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	_, err := s.db.Exec(
		`INSERT INTO pending_verifications (
			chat_id,
			user_id,
			user_language,
			token_timestamp,
			token_rand,
			expire_at,
			reminder_message_id,
			private_message_id,
			original_message_id,
			message_thread_id,
			reply_to_message_id
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET
		 	user_language = excluded.user_language,
		 	token_timestamp = excluded.token_timestamp,
		 	token_rand = excluded.token_rand,
		 	expire_at = excluded.expire_at,
		 	reminder_message_id = excluded.reminder_message_id,
		 	private_message_id = excluded.private_message_id,
		 	original_message_id = excluded.original_message_id,
		 	message_thread_id = excluded.message_thread_id,
		 	reply_to_message_id = excluded.reply_to_message_id`,
		pending.ChatID,
		pending.UserID,
		pending.UserLanguage,
		pending.Timestamp,
		pending.RandomToken,
		pending.ExpireAt.UTC().Format(time.DateTime),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
	)
	return err
}

func (s *SQLiteStore) UpdatePendingMetadataByToken(pending PendingVerification) (bool, error) {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	result, err := s.db.Exec(
		`UPDATE pending_verifications
		 SET user_language = ?,
		     expire_at = ?,
		     reminder_message_id = ?,
		     private_message_id = ?,
		     original_message_id = ?,
		     message_thread_id = ?,
		     reply_to_message_id = ?
		 WHERE chat_id = ?
		   AND user_id = ?
		   AND token_timestamp = ?
		   AND token_rand = ?`,
		pending.UserLanguage,
		pending.ExpireAt.UTC().Format(time.DateTime),
		pending.ReminderMessageID,
		pending.PrivateMessageID,
		pending.OriginalMessageID,
		pending.MessageThreadID,
		pending.ReplyToMessageID,
		pending.ChatID,
		pending.UserID,
		pending.Timestamp,
		pending.RandomToken,
	)
	if err != nil {
		return false, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rowsAffected > 0, nil
}

func (s *SQLiteStore) ClearPending(chatID, userID int64) error {
	_, err := s.db.Exec(
		`DELETE FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	return err
}

func (s *SQLiteStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action PendingAction, maxWarnings int) (PendingResolutionResult, error) {
	result := PendingResolutionResult{Action: action}

	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	pending, err := scanPending(tx.QueryRow(
		`SELECT chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id
		 FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	))
	if err != nil {
		return result, err
	}
	if pending == nil {
		if err := tx.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}
	if pending.Timestamp != timestamp || pending.RandomToken != randomToken {
		if err := tx.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}

	result.Matched = true
	result.Pending = pending

	if _, err := tx.Exec(
		`DELETE FROM pending_verifications
		 WHERE chat_id = ? AND user_id = ? AND token_timestamp = ? AND token_rand = ?`,
		chatID, userID, timestamp, randomToken,
	); err != nil {
		return result, err
	}

	switch action {
	case PendingActionApprove:
		if err := s.setStatusTx(tx, chatID, userID, "verified"); err != nil {
			return result, err
		}
		if err := s.removeStatusTx(tx, chatID, userID, "rejected"); err != nil {
			return result, err
		}
		if err := s.resetWarningCountTx(tx, chatID, userID); err != nil {
			return result, err
		}
		result.Verified = true
	case PendingActionReject:
		if err := s.setStatusTx(tx, chatID, userID, "rejected"); err != nil {
			return result, err
		}
		if err := s.removeStatusTx(tx, chatID, userID, "verified"); err != nil {
			return result, err
		}
		if err := s.resetWarningCountTx(tx, chatID, userID); err != nil {
			return result, err
		}
		result.Rejected = true
	case PendingActionExpire:
		verified, err := s.hasStatusTx(tx, chatID, userID, "verified")
		if err != nil {
			return result, err
		}
		if verified {
			result.Verified = true
			break
		}

		rejected, err := s.hasStatusTx(tx, chatID, userID, "rejected")
		if err != nil {
			return result, err
		}
		if rejected {
			result.Rejected = true
			break
		}

		newCount, err := s.incrWarningCountTx(tx, chatID, userID)
		if err != nil {
			return result, err
		}
		result.WarningCount = newCount
		result.ShouldBan = maxWarnings > 0 && newCount >= maxWarnings
	case PendingActionCancel:
	default:
		return result, fmt.Errorf("unsupported pending action: %s", action)
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *SQLiteStore) ClearUserVerificationStateEverywhere(userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	queries := []string{
		`DELETE FROM pending_verifications WHERE user_id = ?`,
		`DELETE FROM warnings WHERE user_id = ?`,
		`DELETE FROM user_status WHERE user_id = ?`,
	}

	for _, query := range queries {
		if _, err := tx.Exec(query, userID); err != nil {
			return err
		}
	}

	return tx.Commit()
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
	var count int
	err := s.db.QueryRow(
		`INSERT INTO warnings (chat_id, user_id, count, updated_at)
		 VALUES (?, ?, 1, datetime('now'))
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET count = count + 1, updated_at = datetime('now')
		 RETURNING count`,
		chatID, userID,
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) incrWarningCountTx(tx *sql.Tx, chatID, userID int64) (int, error) {
	var count int
	err := tx.QueryRow(
		`INSERT INTO warnings (chat_id, user_id, count, updated_at)
		 VALUES (?, ?, 1, datetime('now'))
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET count = count + 1, updated_at = datetime('now')
		 RETURNING count`,
		chatID, userID,
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) ResetWarningCount(chatID, userID int64) error {
	_, err := s.db.Exec(
		`DELETE FROM warnings WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	return err
}

func (s *SQLiteStore) resetWarningCountTx(tx *sql.Tx, chatID, userID int64) error {
	_, err := tx.Exec(
		`DELETE FROM warnings WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	return err
}

// --- Blacklist ---

func (s *SQLiteStore) migrateBlacklistTable() error {
	rows, err := s.db.Query(`PRAGMA table_info(blacklist_words)`)
	if err != nil {
		return fmt.Errorf("pragma blacklist_words: %w", err)
	}
	defer rows.Close()

	hasChatID := false
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
			return fmt.Errorf("scan blacklist_words columns: %w", err)
		}
		if name == "chat_id" {
			hasChatID = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate blacklist_words columns: %w", err)
	}

	if hasChatID {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin blacklist migration tx: %w", err)
	}
	defer tx.Rollback()

	migrationQueries := []string{
		`CREATE TABLE blacklist_words_new (
			chat_id  INTEGER NOT NULL DEFAULT 0,
			word     TEXT NOT NULL,
			added_by TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (chat_id, word)
		)`,
		`INSERT INTO blacklist_words_new (chat_id, word, added_by, added_at)
		 SELECT 0, word, added_by, added_at FROM blacklist_words`,
		`DROP TABLE blacklist_words`,
		`ALTER TABLE blacklist_words_new RENAME TO blacklist_words`,
	}

	for _, q := range migrationQueries {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("migrate blacklist_words: %w", err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetBlacklistWords(chatID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT word FROM blacklist_words WHERE chat_id = ?`, chatID)
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

func (s *SQLiteStore) AddBlacklistWord(chatID int64, word, addedBy string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	_, err := s.db.Exec(
		`INSERT INTO blacklist_words (chat_id, word, added_by, added_at) VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(chat_id, word) DO NOTHING`,
		chatID, normalized, addedBy,
	)
	return err
}

func (s *SQLiteStore) RemoveBlacklistWord(chatID int64, word string) error {
	normalized := normalizeBlacklistWord(word)
	if normalized == "" {
		return fmt.Errorf("blacklist word cannot be empty")
	}

	_, err := s.db.Exec(`DELETE FROM blacklist_words WHERE chat_id = ? AND lower(trim(word)) = ?`, chatID, normalized)
	return err
}

func (s *SQLiteStore) GetAllBlacklistWords() (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT chat_id, word FROM blacklist_words`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]string)
	for rows.Next() {
		var chatID int64
		var w string
		if err := rows.Scan(&chatID, &w); err != nil {
			return nil, err
		}
		result[chatID] = append(result[chatID], w)
	}
	return result, rows.Err()
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
