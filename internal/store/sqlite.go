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
	db    *sql.DB
	stmts sqliteStatements
}

const (
	currentSchemaVersion = 4
	sqliteNowUnixExpr    = "CAST(strftime('%s','now') AS INTEGER)"
	// Legacy databases may have stored expire_at as DATETIME text, integer unix
	// seconds, or integer-like text. We preserve numeric values directly and only
	// fall back to SQLite's datetime parser for actual timestamp strings.
	sqliteLegacyPendingExpireAtUnixExpr = `CASE
		WHEN typeof(expire_at) IN ('integer', 'real') THEN CAST(expire_at AS INTEGER)
		WHEN typeof(expire_at) = 'text'
			AND NULLIF(TRIM(expire_at), '') IS NOT NULL
			AND TRIM(expire_at) NOT GLOB '*[^0-9]*'
		THEN CAST(TRIM(expire_at) AS INTEGER)
		ELSE COALESCE(
			CAST(strftime('%s', expire_at) AS INTEGER),
			CAST(strftime('%s', expire_at || ' UTC') AS INTEGER)
		)
	END`
)

const pendingVerificationColumns = `chat_id, user_id, user_language, token_timestamp, token_rand, expire_at, reminder_message_id, private_message_id, original_message_id, message_thread_id, reply_to_message_id`

const (
	sqliteSetStatusQuery = `INSERT INTO user_status (chat_id, user_id, status, updated_at)
	 VALUES (?, ?, ?, datetime('now'))
	 ON CONFLICT(chat_id, user_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`
	sqliteRemoveStatusQuery          = `DELETE FROM user_status WHERE chat_id = ? AND user_id = ? AND status = ?`
	sqliteCreatePendingIfAbsentQuery = `INSERT INTO pending_verifications (
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
	 )
	 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
	 WHERE NOT EXISTS (
	 	SELECT 1
	 	FROM user_status
	 	WHERE chat_id = ?
	 		AND user_id = ?
	 		AND status IN ('verified', 'rejected')
	 )
	 ON CONFLICT(chat_id, user_id) DO NOTHING`
	sqliteSetPendingQuery = `INSERT INTO pending_verifications (
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
	 	reply_to_message_id = excluded.reply_to_message_id`
	sqliteUpdatePendingMetadataByTokenQuery = `UPDATE pending_verifications
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
	   AND token_rand = ?`
	sqliteClearPendingQuery          = `DELETE FROM pending_verifications WHERE chat_id = ? AND user_id = ?`
	sqliteResolvePendingByTokenQuery = `DELETE FROM pending_verifications
	 WHERE chat_id = ?
	   AND user_id = ?
	   AND token_timestamp = ?
	   AND token_rand = ?
	 RETURNING ` + pendingVerificationColumns
	sqliteIncrWarningCountQuery = `INSERT INTO warnings (chat_id, user_id, count, updated_at)
	 VALUES (?, ?, 1, datetime('now'))
	 ON CONFLICT(chat_id, user_id) DO UPDATE SET count = count + 1, updated_at = datetime('now')
	 RETURNING count`
	sqliteResetWarningCountQuery        = `DELETE FROM warnings WHERE chat_id = ? AND user_id = ?`
	sqliteBlacklistWordsDefaultCapacity = 16
)

type sqliteStatements struct {
	getUserLanguagePreference *sql.Stmt
	setUserLanguagePreference *sql.Stmt
	getStatus                 *sql.Stmt
	setStatus                 *sql.Stmt
	removeStatus              *sql.Stmt
	hasActivePending          *sql.Stmt
	getPending                *sql.Stmt
	listPendingVerifications  *sql.Stmt
	createPendingIfAbsent     *sql.Stmt
	setPending                *sql.Stmt
	updatePendingMetadata     *sql.Stmt
	clearPending              *sql.Stmt
	resolvePendingByToken     *sql.Stmt
	getWarningCount           *sql.Stmt
	incrWarningCount          *sql.Stmt
	resetWarningCount         *sql.Stmt
	getBlacklistWords         *sql.Stmt
	getAllBlacklistWords      *sql.Stmt
}

func sqliteDataSourceName(dbPath string) string {
	return dbPath + "?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)"
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteDataSourceName(dbPath))
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
	if err := s.prepareStatements(); err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare sqlite statements: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) prepareStatements() error {
	var prepared []*sql.Stmt
	prepare := func(dst **sql.Stmt, query string) error {
		stmt, err := s.db.Prepare(query)
		if err != nil {
			return err
		}
		*dst = stmt
		prepared = append(prepared, stmt)
		return nil
	}
	closePrepared := func() {
		for _, stmt := range prepared {
			if stmt != nil {
				_ = stmt.Close()
			}
		}
	}

	for _, item := range []struct {
		dst   **sql.Stmt
		query string
	}{
		{&s.stmts.getUserLanguagePreference, `SELECT preferred_language FROM user_preferences WHERE user_id = ?`},
		{&s.stmts.setUserLanguagePreference, `INSERT INTO user_preferences (user_id, preferred_language, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET
		 	preferred_language = excluded.preferred_language,
		 	updated_at = excluded.updated_at`},
		{&s.stmts.getStatus, `SELECT status FROM user_status WHERE chat_id = ? AND user_id = ?`},
		{&s.stmts.setStatus, sqliteSetStatusQuery},
		{&s.stmts.removeStatus, sqliteRemoveStatusQuery},
		{&s.stmts.hasActivePending, `SELECT COUNT(*) FROM pending_verifications WHERE chat_id = ? AND user_id = ? AND expire_at > ` + sqliteNowUnixExpr},
		{&s.stmts.getPending, `SELECT ` + pendingVerificationColumns + `
		 FROM pending_verifications WHERE chat_id = ? AND user_id = ?`},
		{&s.stmts.listPendingVerifications, `SELECT ` + pendingVerificationColumns + `
		 FROM pending_verifications
		 ORDER BY expire_at ASC, chat_id ASC, user_id ASC`},
		{&s.stmts.createPendingIfAbsent, sqliteCreatePendingIfAbsentQuery},
		{&s.stmts.setPending, sqliteSetPendingQuery},
		{&s.stmts.updatePendingMetadata, sqliteUpdatePendingMetadataByTokenQuery},
		{&s.stmts.clearPending, sqliteClearPendingQuery},
		{&s.stmts.resolvePendingByToken, sqliteResolvePendingByTokenQuery},
		{&s.stmts.getWarningCount, `SELECT COALESCE((SELECT count FROM warnings WHERE chat_id = ? AND user_id = ?), 0)`},
		{&s.stmts.incrWarningCount, sqliteIncrWarningCountQuery},
		{&s.stmts.resetWarningCount, sqliteResetWarningCountQuery},
		{&s.stmts.getBlacklistWords, `SELECT word FROM blacklist_words WHERE chat_id = ?`},
		{&s.stmts.getAllBlacklistWords, `SELECT chat_id, word FROM blacklist_words`},
	} {
		if err := prepare(item.dst, item.query); err != nil {
			closePrepared()
			return err
		}
	}

	return nil
}

func (s *SQLiteStore) closeStatements() {
	for _, stmt := range []*sql.Stmt{
		s.stmts.getUserLanguagePreference,
		s.stmts.setUserLanguagePreference,
		s.stmts.getStatus,
		s.stmts.setStatus,
		s.stmts.removeStatus,
		s.stmts.hasActivePending,
		s.stmts.getPending,
		s.stmts.listPendingVerifications,
		s.stmts.createPendingIfAbsent,
		s.stmts.setPending,
		s.stmts.updatePendingMetadata,
		s.stmts.clearPending,
		s.stmts.resolvePendingByToken,
		s.stmts.getWarningCount,
		s.stmts.incrWarningCount,
		s.stmts.resetWarningCount,
		s.stmts.getBlacklistWords,
		s.stmts.getAllBlacklistWords,
	} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
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
			expire_at INTEGER NOT NULL,
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
		case 3:
			if err := s.migrateToVersion4(); err != nil {
				return err
			}
			version = 4
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

func (s *SQLiteStore) migrateToVersion4() error {
	if err := s.migratePendingExpireAtToUnix(); err != nil {
		return fmt.Errorf("migrate database schema from version 3 to 4: %w", err)
	}

	if err := s.setSchemaVersion(4); err != nil {
		return fmt.Errorf("set user_version to 4: %w", err)
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

func (s *SQLiteStore) migratePendingExpireAtToUnix() error {
	rows, err := s.db.Query(`PRAGMA table_info(pending_verifications)`)
	if err != nil {
		return fmt.Errorf("pragma pending_verifications: %w", err)
	}

	expireAtType := ""
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
		if name == "expire_at" {
			expireAtType = strings.ToUpper(strings.TrimSpace(dataType))
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate pending_verifications columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close pending_verifications schema rows: %w", err)
	}

	if expireAtType == "INTEGER" {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin pending expire_at migration tx: %w", err)
	}
	defer tx.Rollback()

	queries := []string{
		`CREATE TABLE pending_verifications_new (
			chat_id   INTEGER NOT NULL,
			user_id   INTEGER NOT NULL,
			user_language TEXT NOT NULL DEFAULT 'zh-cn',
			token_timestamp INTEGER NOT NULL DEFAULT 0,
			token_rand TEXT NOT NULL DEFAULT '',
			expire_at INTEGER NOT NULL,
			reminder_message_id INTEGER NOT NULL DEFAULT 0,
			private_message_id INTEGER NOT NULL DEFAULT 0,
			original_message_id INTEGER NOT NULL DEFAULT 0,
			message_thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id)
		)`,
		`INSERT INTO pending_verifications_new (` + pendingVerificationColumns + `)
		 SELECT
		 	chat_id,
		 	user_id,
		 	COALESCE(NULLIF(TRIM(user_language), ''), 'zh-cn'),
		 	token_timestamp,
		 	token_rand,
		 	` + sqliteLegacyPendingExpireAtUnixExpr + `,
		 	reminder_message_id,
		 	private_message_id,
		 	original_message_id,
		 	message_thread_id,
		 	reply_to_message_id
		 FROM pending_verifications`,
		`DROP TABLE pending_verifications`,
		`ALTER TABLE pending_verifications_new RENAME TO pending_verifications`,
	}

	for _, query := range queries {
		if _, err := tx.Exec(query); err != nil {
			return fmt.Errorf("migrate pending expire_at to unix: %w", err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) setSchemaVersion(version int) error {
	_, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPending(scanner rowScanner) (*PendingVerification, error) {
	pending, found, err := scanPendingValue(scanner)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &pending, nil
}

func scanPendingValue(scanner rowScanner) (PendingVerification, bool, error) {
	var pending PendingVerification
	var expireAtUnix int64
	err := scanner.Scan(
		&pending.ChatID,
		&pending.UserID,
		&pending.UserLanguage,
		&pending.Timestamp,
		&pending.RandomToken,
		&expireAtUnix,
		&pending.ReminderMessageID,
		&pending.PrivateMessageID,
		&pending.OriginalMessageID,
		&pending.MessageThreadID,
		&pending.ReplyToMessageID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return PendingVerification{}, false, nil
		}
		return PendingVerification{}, false, err
	}

	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	pending.ExpireAt = time.Unix(expireAtUnix, 0).UTC()
	return pending, true, nil
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
	s.closeStatements()
	return s.db.Close()
}

// --- User preferences ---

func (s *SQLiteStore) GetUserLanguagePreference(userID int64) (string, error) {
	var language string
	err := s.stmts.getUserLanguagePreference.QueryRow(userID).Scan(&language)
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

	_, err := s.stmts.setUserLanguagePreference.Exec(userID, language)
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
	currentStatus, err := s.currentStatus(chatID, userID)
	return currentStatus == status, err
}

func (s *SQLiteStore) setStatus(chatID, userID int64, status string) error {
	_, err := s.stmts.setStatus.Exec(chatID, userID, status)
	return err
}

func (s *SQLiteStore) removeStatus(chatID, userID int64, status string) error {
	_, err := s.stmts.removeStatus.Exec(chatID, userID, status)
	return err
}

func (s *SQLiteStore) hasStatusTx(tx *sql.Tx, chatID, userID int64, status string) (bool, error) {
	currentStatus, err := s.currentStatusTx(tx, chatID, userID)
	return currentStatus == status, err
}

func (s *SQLiteStore) setStatusTx(tx *sql.Tx, chatID, userID int64, status string) error {
	_, err := tx.Stmt(s.stmts.setStatus).Exec(chatID, userID, status)
	return err
}

func (s *SQLiteStore) removeStatusTx(tx *sql.Tx, chatID, userID int64, status string) error {
	_, err := tx.Stmt(s.stmts.removeStatus).Exec(chatID, userID, status)
	return err
}

// --- Pending verification ---

func (s *SQLiteStore) HasActivePending(chatID, userID int64) (bool, error) {
	var count int
	err := s.stmts.hasActivePending.QueryRow(chatID, userID).Scan(&count)
	return count > 0, err
}

func (s *SQLiteStore) GetPending(chatID, userID int64) (*PendingVerification, error) {
	pending, err := scanPending(s.stmts.getPending.QueryRow(chatID, userID))
	if err != nil {
		return nil, err
	}
	return pending, nil
}

func (s *SQLiteStore) ListPendingVerifications() ([]PendingVerification, error) {
	rows, err := s.stmts.listPendingVerifications.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pendingVerifications := make([]PendingVerification, 0, 16)
	for rows.Next() {
		pending, found, err := scanPendingValue(rows)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		pendingVerifications = append(pendingVerifications, pending)
	}

	return pendingVerifications, rows.Err()
}

func (s *SQLiteStore) ReserveVerificationWindow(pending PendingVerification, maxWarnings int) (VerificationReservationResult, error) {
	result := VerificationReservationResult{}
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	warningCount, err := s.GetWarningCount(pending.ChatID, pending.UserID)
	if err != nil {
		return result, err
	}
	result.WarningCount = warningCount
	if warningCount >= maxWarnings {
		result.LimitExceeded = true
		return result, nil
	}

	created, existing, err := s.CreatePendingIfAbsent(pending)
	if err != nil {
		return result, err
	}
	result.Created = created
	result.Existing = existing
	return result, nil
}

func (s *SQLiteStore) CreatePendingIfAbsent(pending PendingVerification) (bool, *PendingVerification, error) {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	for attempt := 0; attempt < 2; attempt++ {
		result, err := s.stmts.createPendingIfAbsent.Exec(
			pending.ChatID,
			pending.UserID,
			pending.UserLanguage,
			pending.Timestamp,
			pending.RandomToken,
			pendingExpireAtUnix(pending),
			pending.ReminderMessageID,
			pending.PrivateMessageID,
			pending.OriginalMessageID,
			pending.MessageThreadID,
			pending.ReplyToMessageID,
			pending.ChatID,
			pending.UserID,
		)
		if err != nil {
			return false, nil, err
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return false, nil, err
		}
		if rowsAffected > 0 {
			return true, nil, nil
		}

		existing, err := s.GetPending(pending.ChatID, pending.UserID)
		if err != nil {
			return false, nil, err
		}
		if existing != nil {
			return false, existing, nil
		}

		status, err := s.currentStatus(pending.ChatID, pending.UserID)
		if err != nil {
			return false, nil, err
		}
		if status == "verified" || status == "rejected" {
			return false, nil, nil
		}
	}

	return false, nil, nil
}

func (s *SQLiteStore) SetPending(pending PendingVerification) error {
	if strings.TrimSpace(pending.UserLanguage) == "" {
		pending.UserLanguage = "zh-cn"
	}

	_, err := s.stmts.setPending.Exec(
		pending.ChatID,
		pending.UserID,
		pending.UserLanguage,
		pending.Timestamp,
		pending.RandomToken,
		pendingExpireAtUnix(pending),
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

	result, err := s.stmts.updatePendingMetadata.Exec(
		pending.UserLanguage,
		pendingExpireAtUnix(pending),
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
	_, err := s.stmts.clearPending.Exec(chatID, userID)
	return err
}

func (s *SQLiteStore) ResolvePendingByToken(chatID, userID int64, timestamp int64, randomToken string, action PendingAction, maxWarnings int) (PendingResolutionResult, error) {
	result := PendingResolutionResult{Action: action}

	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	pending, err := scanPending(tx.Stmt(s.stmts.resolvePendingByToken).QueryRow(
		chatID, userID, timestamp, randomToken,
	))
	if err != nil {
		return result, err
	}
	if pending == nil {
		return result, nil
	}

	result.Matched = true
	result.Pending = pending

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
	err := s.stmts.getWarningCount.QueryRow(chatID, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) getWarningCountTx(tx *sql.Tx, chatID, userID int64) (int, error) {
	var count int
	err := tx.Stmt(s.stmts.getWarningCount).QueryRow(chatID, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) IncrWarningCount(chatID, userID int64) (int, error) {
	var count int
	err := s.stmts.incrWarningCount.QueryRow(chatID, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) incrWarningCountTx(tx *sql.Tx, chatID, userID int64) (int, error) {
	var count int
	err := tx.Stmt(s.stmts.incrWarningCount).QueryRow(chatID, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) ResetWarningCount(chatID, userID int64) error {
	_, err := s.stmts.resetWarningCount.Exec(chatID, userID)
	return err
}

func (s *SQLiteStore) resetWarningCountTx(tx *sql.Tx, chatID, userID int64) error {
	_, err := tx.Stmt(s.stmts.resetWarningCount).Exec(chatID, userID)
	return err
}

// --- Blacklist ---

func (s *SQLiteStore) migrateBlacklistTable() error {
	rows, err := s.db.Query(`PRAGMA table_info(blacklist_words)`)
	if err != nil {
		return fmt.Errorf("pragma blacklist_words: %w", err)
	}

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
		_ = rows.Close()
		return fmt.Errorf("iterate blacklist_words columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close blacklist_words schema rows: %w", err)
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
	rows, err := s.stmts.getBlacklistWords.Query(chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	words := make([]string, 0, sqliteBlacklistWordsDefaultCapacity)
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
	rows, err := s.stmts.getAllBlacklistWords.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]string, 8)
	for rows.Next() {
		var chatID int64
		var w string
		if err := rows.Scan(&chatID, &w); err != nil {
			return nil, err
		}
		if result[chatID] == nil {
			result[chatID] = make([]string, 0, 4)
		}
		result[chatID] = append(result[chatID], w)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) currentStatus(chatID, userID int64) (string, error) {
	var status string
	err := s.stmts.getStatus.QueryRow(chatID, userID).Scan(&status)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return status, err
}

func (s *SQLiteStore) currentStatusTx(tx *sql.Tx, chatID, userID int64) (string, error) {
	var status string
	err := tx.Stmt(s.stmts.getStatus).QueryRow(chatID, userID).Scan(&status)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return status, err
}

func normalizeBlacklistWord(word string) string {
	return strings.ToLower(strings.TrimSpace(word))
}

func pendingExpireAtUnix(pending PendingVerification) int64 {
	return pending.ExpireAt.UTC().Unix()
}
