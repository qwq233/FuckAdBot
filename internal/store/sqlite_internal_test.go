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

func TestParseSQLiteTimeSupportsCommonLayouts(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	cases := []string{
		now.Format(time.DateTime),
		now.Format(time.RFC3339),
		now.Format(time.RFC3339Nano),
	}

	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := parseSQLiteTime(input)
			if err != nil {
				t.Fatalf("parseSQLiteTime(%q) error = %v", input, err)
			}
			if !got.Equal(now) {
				t.Fatalf("parseSQLiteTime(%q) = %v, want %v", input, got, now)
			}
		})
	}
}

func TestParseSQLiteTimeRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	if _, err := parseSQLiteTime("2026/04/08 12:00:00"); err == nil {
		t.Fatal("parseSQLiteTime() error = nil, want unsupported format error")
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
		*(dest[5].(*string)) = expireAt.Format(time.RFC3339)
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

func TestScanPendingRejectsInvalidExpireAt(t *testing.T) {
	t.Parallel()

	pending, err := scanPending(rowScannerFunc(func(dest ...any) error {
		*(dest[0].(*int64)) = -100123
		*(dest[1].(*int64)) = 42
		*(dest[2].(*string)) = "en"
		*(dest[3].(*int64)) = 1712300000
		*(dest[4].(*string)) = "token-a"
		*(dest[5].(*string)) = "not-a-time"
		return nil
	}))
	if err == nil {
		t.Fatal("scanPending() error = nil, want invalid expire_at error")
	}
	if pending != nil {
		t.Fatalf("scanPending() = %+v, want nil on invalid expire_at", pending)
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
