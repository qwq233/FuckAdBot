package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rowScannerFunc func(dest ...any) error

func (fn rowScannerFunc) Scan(dest ...any) error {
	return fn(dest...)
}

func TestPendingExpireAtUnixUsesUTCSeconds(t *testing.T) {
	t.Parallel()

	expireAt := time.Date(2026, time.April, 13, 2, 30, 45, 987654321, time.FixedZone("UTC+8", 8*60*60))
	pending := PendingVerification{
		ExpireAt: expireAt,
	}
	if got, want := pendingExpireAtUnix(pending), expireAt.UTC().Unix(); got != want {
		t.Fatalf("pendingExpireAtUnix() = %d, want %d", got, want)
	}
}

func TestSQLiteDataSourceNameEnablesWalAndNormalSynchronous(t *testing.T) {
	t.Parallel()

	got := sqliteDataSourceName(filepath.Join("data", "bench.db"))
	for _, want := range []string{
		"_pragma=journal_mode(wal)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(normal)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sqliteDataSourceName() = %q, want substring %q", got, want)
		}
	}
}

func TestScanPendingReturnsNilOnNoRows(t *testing.T) {
	t.Parallel()

	pending, err := scanPending(rowScannerFunc(func(dest ...any) error {
		return sql.ErrNoRows
	}))
	if err != nil {
		t.Fatalf("scanPending() error = %v, want nil", err)
	}
	if pending != nil {
		t.Fatalf("scanPending() = %+v, want nil", pending)
	}
}

func TestScanPendingDefaultsBlankLanguage(t *testing.T) {
	t.Parallel()

	expireAt := time.Now().UTC().Truncate(time.Second)
	pending, err := scanPending(rowScannerFunc(func(dest ...any) error {
		*(dest[0].(*int64)) = -100123
		*(dest[1].(*int64)) = 42
		*(dest[2].(*string)) = ""
		*(dest[3].(*int64)) = 1712300000
		*(dest[4].(*string)) = "token-a"
		*(dest[5].(*int64)) = expireAt.Unix()
		*(dest[6].(*int64)) = 1
		*(dest[7].(*int64)) = 2
		*(dest[8].(*int64)) = 3
		*(dest[9].(*int64)) = 4
		*(dest[10].(*int64)) = 5
		return nil
	}))
	if err != nil {
		t.Fatalf("scanPending() error = %v", err)
	}
	if pending == nil {
		t.Fatal("scanPending() = nil, want populated pending")
	}
	if pending.UserLanguage != "zh-cn" {
		t.Fatalf("scanPending().UserLanguage = %q, want %q", pending.UserLanguage, "zh-cn")
	}
	if !pending.ExpireAt.Equal(expireAt) {
		t.Fatalf("scanPending().ExpireAt = %v, want %v", pending.ExpireAt, expireAt)
	}
}

func TestScanPendingPropagatesScanError(t *testing.T) {
	t.Parallel()

	wantErr := sql.ErrConnDone
	pending, err := scanPending(rowScannerFunc(func(dest ...any) error {
		return wantErr
	}))
	if err != wantErr {
		t.Fatalf("scanPending() error = %v, want %v", err, wantErr)
	}
	if pending != nil {
		t.Fatalf("scanPending() = %+v, want nil on scan error", pending)
	}
}

func TestMigratePendingExpireAtToUnixConvertsLegacyTextColumn(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	expireAt := time.Now().UTC().Truncate(time.Second)
	if _, err := rawDB.Exec(`CREATE TABLE pending_verifications (
		chat_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
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
	)`); err != nil {
		t.Fatalf("create legacy pending_verifications error = %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO pending_verifications (`+pendingVerificationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		-100123, 42, "en", int64(1712300000), "token-a", expireAt.Format(time.DateTime), int64(1), int64(2), int64(3), int64(4), int64(5),
	); err != nil {
		t.Fatalf("seed legacy pending_verifications row error = %v", err)
	}

	s := &SQLiteStore{db: rawDB}
	if err := s.migratePendingExpireAtToUnix(); err != nil {
		t.Fatalf("migratePendingExpireAtToUnix() error = %v", err)
	}

	var gotExpireAt int64
	if err := rawDB.QueryRow(
		`SELECT expire_at FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		-100123, 42,
	).Scan(&gotExpireAt); err != nil {
		t.Fatalf("read migrated expire_at error = %v", err)
	}
	if gotExpireAt != expireAt.Unix() {
		t.Fatalf("migrated expire_at = %d, want %d", gotExpireAt, expireAt.Unix())
	}
}

func TestMigratePendingExpireAtToUnixPreservesLegacyIntegerStorage(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	expireAtUnix := time.Now().UTC().Truncate(time.Second).Unix()
	if _, err := rawDB.Exec(`CREATE TABLE pending_verifications (
		chat_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
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
	)`); err != nil {
		t.Fatalf("create legacy pending_verifications error = %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO pending_verifications (`+pendingVerificationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		-100123, 42, "en", int64(1712300000), "token-a", expireAtUnix, int64(1), int64(2), int64(3), int64(4), int64(5),
	); err != nil {
		t.Fatalf("seed legacy integer pending_verifications row error = %v", err)
	}

	s := &SQLiteStore{db: rawDB}
	if err := s.migratePendingExpireAtToUnix(); err != nil {
		t.Fatalf("migratePendingExpireAtToUnix() error = %v", err)
	}

	var (
		gotExpireAt int64
		storageType string
	)
	if err := rawDB.QueryRow(
		`SELECT expire_at, typeof(expire_at) FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		-100123, 42,
	).Scan(&gotExpireAt, &storageType); err != nil {
		t.Fatalf("read migrated integer expire_at error = %v", err)
	}
	if gotExpireAt != expireAtUnix {
		t.Fatalf("migrated integer expire_at = %d, want %d", gotExpireAt, expireAtUnix)
	}
	if storageType != "integer" {
		t.Fatalf("migrated integer expire_at storage = %q, want %q", storageType, "integer")
	}
}

func TestMigratePendingExpireAtToUnixConvertsLegacyNumericText(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	expireAtUnix := time.Now().UTC().Truncate(time.Second).Unix()
	if _, err := rawDB.Exec(`CREATE TABLE pending_verifications (
		chat_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		user_language TEXT NOT NULL DEFAULT 'zh-cn',
		token_timestamp INTEGER NOT NULL DEFAULT 0,
		token_rand TEXT NOT NULL DEFAULT '',
		expire_at TEXT NOT NULL,
		reminder_message_id INTEGER NOT NULL DEFAULT 0,
		private_message_id INTEGER NOT NULL DEFAULT 0,
		original_message_id INTEGER NOT NULL DEFAULT 0,
		message_thread_id INTEGER NOT NULL DEFAULT 0,
		reply_to_message_id INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (chat_id, user_id)
	)`); err != nil {
		t.Fatalf("create legacy pending_verifications error = %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO pending_verifications (`+pendingVerificationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		-100123, 42, "en", int64(1712300000), "token-a", strconv.FormatInt(expireAtUnix, 10), int64(1), int64(2), int64(3), int64(4), int64(5),
	); err != nil {
		t.Fatalf("seed legacy numeric-text pending_verifications row error = %v", err)
	}

	s := &SQLiteStore{db: rawDB}
	if err := s.migratePendingExpireAtToUnix(); err != nil {
		t.Fatalf("migratePendingExpireAtToUnix() error = %v", err)
	}

	var (
		gotExpireAt int64
		storageType string
	)
	if err := rawDB.QueryRow(
		`SELECT expire_at, typeof(expire_at) FROM pending_verifications WHERE chat_id = ? AND user_id = ?`,
		-100123, 42,
	).Scan(&gotExpireAt, &storageType); err != nil {
		t.Fatalf("read migrated numeric-text expire_at error = %v", err)
	}
	if gotExpireAt != expireAtUnix {
		t.Fatalf("migrated numeric-text expire_at = %d, want %d", gotExpireAt, expireAtUnix)
	}
	if storageType != "integer" {
		t.Fatalf("migrated numeric-text expire_at storage = %q, want %q", storageType, "integer")
	}
}

func TestNewSQLiteStoreReturnsErrorWhenDBDirCannotBeCreated(t *testing.T) {
	t.Parallel()

	parentPath := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(parentPath, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	_, err := NewSQLiteStore(filepath.Join(parentPath, "test.db"))
	if err == nil {
		t.Fatal("NewSQLiteStore() error = nil, want directory creation failure")
	}
	if !strings.Contains(err.Error(), "create db directory") {
		t.Fatalf("NewSQLiteStore() error = %q, want create db directory context", err)
	}
}

func TestMigrateSchemaVersionLeavesFutureVersionUntouched(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=user_version(99)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)

	var version int
	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version error = %v", err)
	}
	if version != 99 {
		t.Fatalf("PRAGMA user_version = %d, want 99 before migration", version)
	}

	s := &SQLiteStore{db: rawDB}
	err = s.migrateSchemaVersion()
	if err != nil {
		t.Fatalf("migrateSchemaVersion() error = %v, want nil for future schema version", err)
	}

	if err := rawDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version after migration error = %v", err)
	}
	if version != 99 {
		t.Fatalf("PRAGMA user_version after migration = %d, want 99", version)
	}
}

func TestMigrateBlacklistTableMigratesLegacyRows(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer rawDB.Close()

	if _, err := rawDB.Exec(`CREATE TABLE blacklist_words (
		word TEXT NOT NULL PRIMARY KEY,
		added_by TEXT NOT NULL DEFAULT '',
		added_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create legacy blacklist_words error = %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO blacklist_words (word, added_by, added_at) VALUES
		 ('spam', 'alice', datetime('now')),
		 ('scam', 'bob', datetime('now'))`,
	); err != nil {
		t.Fatalf("seed legacy blacklist_words error = %v", err)
	}

	s := &SQLiteStore{db: rawDB}
	if err := s.migrateBlacklistTable(); err != nil {
		t.Fatalf("migrateBlacklistTable() error = %v", err)
	}

	rows, err := rawDB.Query(`SELECT chat_id, word, added_by FROM blacklist_words ORDER BY word ASC`)
	if err != nil {
		t.Fatalf("query migrated blacklist_words error = %v", err)
	}
	defer rows.Close()

	var got [][3]string
	for rows.Next() {
		var (
			chatID  int64
			word    string
			addedBy string
		)
		if err := rows.Scan(&chatID, &word, &addedBy); err != nil {
			t.Fatalf("scan migrated row error = %v", err)
		}
		got = append(got, [3]string{strconv.FormatInt(chatID, 10), word, addedBy})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated rows error = %v", err)
	}

	want := [][3]string{
		{"0", "scam", "bob"},
		{"0", "spam", "alice"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(migrated rows) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("migrated row[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
