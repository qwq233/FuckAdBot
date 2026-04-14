package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type dualWriteEventKind string

const (
	dualWriteEventSyncUserState      dualWriteEventKind = "sync-user-state"
	dualWriteEventSyncPreference     dualWriteEventKind = "sync-preference"
	dualWriteEventSyncBlacklistScope dualWriteEventKind = "sync-blacklist-scope"
	dualWriteEventClearUserState     dualWriteEventKind = "clear-user-state"
	dualWriteDeleteBatchChunkSize                       = 512
)

type dualWriteQueue struct {
	db    *sql.DB
	stmts dualWriteQueueStatements
}

type dualWriteEvent struct {
	ID        int64
	Kind      dualWriteEventKind
	DedupeKey string
	Payload   []byte
	Attempts  int
}

type dualWriteQueueStatements struct {
	enqueue    *sql.Stmt
	peekBatch  *sql.Stmt
	deleteByID *sql.Stmt
	markFailed *sql.Stmt
	count      *sql.Stmt
	clear      *sql.Stmt
}

type dualWriteQueueItem struct {
	Kind      dualWriteEventKind
	DedupeKey string
	Payload   []byte
}

func newDualWriteQueue(path string) (*dualWriteQueue, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create dual-write queue directory: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteDataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open dual-write queue sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	queue := &dualWriteQueue{db: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping dual-write queue sqlite: %w", err)
	}
	if err := queue.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := queue.prepareStatements(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare dual-write queue statements: %w", err)
	}

	return queue, nil
}

func (q *dualWriteQueue) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS redis_sync_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL,
			dedupe_key TEXT NOT NULL DEFAULT '',
			payload BLOB NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redis_sync_queue_id ON redis_sync_queue(id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_redis_sync_queue_kind_dedupe ON redis_sync_queue(kind, dedupe_key)`,
	}

	for _, query := range queries {
		if _, err := q.db.Exec(query); err != nil {
			return fmt.Errorf("migrate dual-write queue: %w", err)
		}
	}

	if err := q.migrateLegacySchema(); err != nil {
		return err
	}

	return nil
}

func (q *dualWriteQueue) migrateLegacySchema() error {
	rows, err := q.db.Query(`PRAGMA table_info(redis_sync_queue)`)
	if err != nil {
		return fmt.Errorf("pragma redis_sync_queue: %w", err)
	}
	defer rows.Close()

	hasDedupeKey := false
	hasUpdatedAt := false
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
			return fmt.Errorf("scan redis_sync_queue columns: %w", err)
		}
		switch name {
		case "dedupe_key":
			hasDedupeKey = true
		case "updated_at":
			hasUpdatedAt = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate redis_sync_queue columns: %w", err)
	}

	if hasDedupeKey && hasUpdatedAt {
		return nil
	}

	tx, err := q.db.Begin()
	if err != nil {
		return fmt.Errorf("begin dual-write queue migration tx: %w", err)
	}
	defer tx.Rollback()

	queries := []string{
		`DROP INDEX IF EXISTS idx_redis_sync_queue_id`,
		`DROP INDEX IF EXISTS idx_redis_sync_queue_kind_dedupe`,
		`CREATE TABLE redis_sync_queue_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL,
			dedupe_key TEXT NOT NULL DEFAULT '',
			payload BLOB NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		)`,
		`INSERT INTO redis_sync_queue_new (
			id,
			kind,
			dedupe_key,
			payload,
			attempts,
			last_error,
			created_at,
			updated_at
		)
		SELECT
			id,
			kind,
			printf('%s:%d', kind, id),
			payload,
			attempts,
			last_error,
			CASE
				WHEN typeof(created_at) = 'integer' THEN created_at
				WHEN created_at IS NULL OR TRIM(CAST(created_at AS TEXT)) = '' THEN CAST(strftime('%s','now') AS INTEGER)
				ELSE CAST(strftime('%s', created_at) AS INTEGER)
			END,
			CASE
				WHEN typeof(created_at) = 'integer' THEN created_at
				WHEN created_at IS NULL OR TRIM(CAST(created_at AS TEXT)) = '' THEN CAST(strftime('%s','now') AS INTEGER)
				ELSE CAST(strftime('%s', created_at) AS INTEGER)
			END
		FROM redis_sync_queue`,
		`DROP TABLE redis_sync_queue`,
		`ALTER TABLE redis_sync_queue_new RENAME TO redis_sync_queue`,
		`CREATE INDEX idx_redis_sync_queue_id ON redis_sync_queue(id)`,
		`CREATE UNIQUE INDEX idx_redis_sync_queue_kind_dedupe ON redis_sync_queue(kind, dedupe_key)`,
	}

	for _, query := range queries {
		if _, err := tx.Exec(query); err != nil {
			return fmt.Errorf("migrate legacy dual-write queue schema: %w", err)
		}
	}

	return tx.Commit()
}

func (q *dualWriteQueue) prepareStatements() error {
	var prepared []*sql.Stmt
	prepare := func(dst **sql.Stmt, query string) error {
		stmt, err := q.db.Prepare(query)
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
		{&q.stmts.enqueue, `INSERT INTO redis_sync_queue (
			kind,
			dedupe_key,
			payload,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(kind, dedupe_key) DO UPDATE SET
			payload = excluded.payload,
			updated_at = excluded.updated_at,
			attempts = 0,
			last_error = ''
		RETURNING id, created_at = updated_at`},
		{&q.stmts.peekBatch, `SELECT id, kind, dedupe_key, payload, attempts
		 FROM redis_sync_queue
		 ORDER BY updated_at ASC, id ASC
		 LIMIT ?`},
		{&q.stmts.deleteByID, `DELETE FROM redis_sync_queue WHERE id = ?`},
		{&q.stmts.markFailed, `UPDATE redis_sync_queue
		 SET attempts = attempts + 1,
		     last_error = ?
		 WHERE id = ?`},
		{&q.stmts.count, `SELECT COUNT(*) FROM redis_sync_queue`},
		{&q.stmts.clear, `DELETE FROM redis_sync_queue`},
	} {
		if err := prepare(item.dst, item.query); err != nil {
			closePrepared()
			return err
		}
	}

	return nil
}

func (q *dualWriteQueue) closeStatements() {
	for _, stmt := range []*sql.Stmt{
		q.stmts.enqueue,
		q.stmts.peekBatch,
		q.stmts.deleteByID,
		q.stmts.markFailed,
		q.stmts.count,
		q.stmts.clear,
	} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}

func (q *dualWriteQueue) Close() error {
	if q == nil || q.db == nil {
		return nil
	}
	q.closeStatements()
	return q.db.Close()
}

func (q *dualWriteQueue) Enqueue(kind dualWriteEventKind, payload any) error {
	dedupeKey, err := dualWriteDedupeKeyForPayload(kind, payload)
	if err != nil {
		return err
	}

	encoded, err := dualWriteEncodePayload(kind, payload)
	if err != nil {
		return fmt.Errorf("encode dual-write event payload: %w", err)
	}

	if _, err := q.EnqueueBatch([]dualWriteQueueItem{{
		Kind:      kind,
		DedupeKey: dedupeKey,
		Payload:   encoded,
	}}); err != nil {
		return fmt.Errorf("insert dual-write event: %w", err)
	}
	return nil
}

func (q *dualWriteQueue) EnqueueBatch(events []dualWriteQueueItem) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}

	tx, err := q.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(q.stmts.enqueue)
	defer stmt.Close()

	inserted := 0
	nowUnix := time.Now().UTC().UnixNano()
	for index, event := range events {
		dedupeKey := event.DedupeKey
		if dedupeKey == "" {
			dedupeKey, err = dualWriteDedupeKeyFromBytes(event.Kind, event.Payload)
			if err != nil {
				return 0, err
			}
		}

		timestamp := nowUnix + int64(index)
		var (
			id         int64
			isInserted bool
		)
		if err := stmt.QueryRow(string(event.Kind), dedupeKey, event.Payload, timestamp, timestamp).Scan(&id, &isInserted); err != nil {
			return 0, err
		}
		if isInserted {
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (q *dualWriteQueue) PeekBatch(limit int) ([]dualWriteEvent, error) {
	rows, err := q.stmts.peekBatch.Query(limit)
	if err != nil {
		return nil, fmt.Errorf("query dual-write queue: %w", err)
	}
	defer rows.Close()

	events := make([]dualWriteEvent, 0, limit)
	for rows.Next() {
		var event dualWriteEvent
		if err := rows.Scan(&event.ID, &event.Kind, &event.DedupeKey, &event.Payload, &event.Attempts); err != nil {
			return nil, fmt.Errorf("scan dual-write queue row: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dual-write queue rows: %w", err)
	}

	return events, nil
}

func (q *dualWriteQueue) Delete(id int64) (int, error) {
	return q.DeleteBatch([]int64{id})
}

func (q *dualWriteQueue) DeleteBatch(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := q.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := 0
	for start := 0; start < len(ids); start += dualWriteDeleteBatchChunkSize {
		end := start + dualWriteDeleteBatchChunkSize
		if end > len(ids) {
			end = len(ids)
		}

		result, err := tx.Exec(dualWriteDeleteBatchQuery(len(ids[start:end])), dualWriteDeleteBatchArgs(ids[start:end])...)
		if err != nil {
			return 0, fmt.Errorf("delete dual-write batch [%d:%d]: %w", start, end, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += int(rowsAffected)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (q *dualWriteQueue) MarkFailed(id int64, message string) error {
	_, err := q.stmts.markFailed.Exec(message, id)
	if err != nil {
		return fmt.Errorf("mark dual-write event %d failed: %w", id, err)
	}
	return nil
}

func (q *dualWriteQueue) Count() (int, error) {
	var count int
	if err := q.stmts.count.QueryRow().Scan(&count); err != nil {
		return 0, fmt.Errorf("count dual-write queue: %w", err)
	}
	return count, nil
}

func (q *dualWriteQueue) Clear() error {
	if _, err := q.stmts.clear.Exec(); err != nil {
		return fmt.Errorf("clear dual-write queue: %w", err)
	}
	return nil
}

func dualWriteDedupeKeyForPayload(kind dualWriteEventKind, payload any) (string, error) {
	switch kind {
	case dualWriteEventSyncUserState:
		value, ok := payload.(dualWriteUserStatePayload)
		if !ok {
			return "", fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return dualWriteUserStateDedupeKey(value.ChatID, value.UserID), nil
	case dualWriteEventSyncPreference, dualWriteEventClearUserState:
		value, ok := payload.(dualWriteUserPayload)
		if !ok {
			return "", fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return dualWriteUserDedupeKey(value.UserID), nil
	case dualWriteEventSyncBlacklistScope:
		value, ok := payload.(dualWriteBlacklistPayload)
		if !ok {
			return "", fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return dualWriteBlacklistScopeDedupeKey(value.ChatID), nil
	default:
		return "", fmt.Errorf("unsupported dual-write event kind: %s", kind)
	}
}

func dualWriteEncodePayload(kind dualWriteEventKind, payload any) ([]byte, error) {
	switch kind {
	case dualWriteEventSyncUserState:
		value, ok := payload.(dualWriteUserStatePayload)
		if !ok {
			return nil, fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return []byte(dualWriteUserStateDedupeKey(value.ChatID, value.UserID)), nil
	case dualWriteEventSyncPreference, dualWriteEventClearUserState:
		value, ok := payload.(dualWriteUserPayload)
		if !ok {
			return nil, fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return []byte(dualWriteUserDedupeKey(value.UserID)), nil
	case dualWriteEventSyncBlacklistScope:
		value, ok := payload.(dualWriteBlacklistPayload)
		if !ok {
			return nil, fmt.Errorf("unexpected payload type %T for %s", payload, kind)
		}
		return []byte(dualWriteBlacklistScopeDedupeKey(value.ChatID)), nil
	default:
		return nil, fmt.Errorf("unsupported dual-write event kind: %s", kind)
	}
}

func dualWriteDedupeKeyFromBytes(kind dualWriteEventKind, payload []byte) (string, error) {
	switch kind {
	case dualWriteEventSyncUserState:
		value, err := dualWriteDecodeUserStatePayload(payload)
		if err != nil {
			return "", fmt.Errorf("decode user-state payload for dedupe key: %w", err)
		}
		return dualWriteUserStateDedupeKey(value.ChatID, value.UserID), nil
	case dualWriteEventSyncPreference, dualWriteEventClearUserState:
		value, err := dualWriteDecodeUserPayload(payload)
		if err != nil {
			return "", fmt.Errorf("decode user payload for dedupe key: %w", err)
		}
		return dualWriteUserDedupeKey(value.UserID), nil
	case dualWriteEventSyncBlacklistScope:
		value, err := dualWriteDecodeBlacklistPayload(payload)
		if err != nil {
			return "", fmt.Errorf("decode blacklist payload for dedupe key: %w", err)
		}
		return dualWriteBlacklistScopeDedupeKey(value.ChatID), nil
	default:
		return "", fmt.Errorf("unsupported dual-write event kind: %s", kind)
	}
}

func dualWriteDecodeUserStatePayload(payload []byte) (dualWriteUserStatePayload, error) {
	encoded := string(payload)
	chatValue, userValue, ok := strings.Cut(encoded, ":")
	if ok {
		chatID, chatErr := strconv.ParseInt(chatValue, 10, 64)
		userID, userErr := strconv.ParseInt(userValue, 10, 64)
		if chatErr == nil && userErr == nil {
			return dualWriteUserStatePayload{ChatID: chatID, UserID: userID}, nil
		}
	}

	var value dualWriteUserStatePayload
	if err := json.Unmarshal(payload, &value); err != nil {
		return dualWriteUserStatePayload{}, err
	}
	return value, nil
}

func dualWriteDecodeUserPayload(payload []byte) (dualWriteUserPayload, error) {
	userID, err := strconv.ParseInt(string(payload), 10, 64)
	if err == nil {
		return dualWriteUserPayload{UserID: userID}, nil
	}

	var value dualWriteUserPayload
	if decodeErr := json.Unmarshal(payload, &value); decodeErr != nil {
		return dualWriteUserPayload{}, decodeErr
	}
	return value, nil
}

func dualWriteDecodeBlacklistPayload(payload []byte) (dualWriteBlacklistPayload, error) {
	chatID, err := strconv.ParseInt(string(payload), 10, 64)
	if err == nil {
		return dualWriteBlacklistPayload{ChatID: chatID}, nil
	}

	var value dualWriteBlacklistPayload
	if decodeErr := json.Unmarshal(payload, &value); decodeErr != nil {
		return dualWriteBlacklistPayload{}, decodeErr
	}
	return value, nil
}

func dualWriteUserStateDedupeKey(chatID, userID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
}

func dualWriteUserDedupeKey(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

func dualWriteBlacklistScopeDedupeKey(chatID int64) string {
	return strconv.FormatInt(chatID, 10)
}

func dualWriteBufferedMapKey(kind dualWriteEventKind, dedupeKey string) string {
	return string(kind) + "\x00" + dedupeKey
}

func dualWriteDeleteBatchQuery(size int) string {
	var builder strings.Builder
	builder.Grow(len("DELETE FROM redis_sync_queue WHERE id IN ()") + size*2)
	builder.WriteString("DELETE FROM redis_sync_queue WHERE id IN (")
	for index := 0; index < size; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('?')
	}
	builder.WriteByte(')')
	return builder.String()
}

func dualWriteDeleteBatchArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return args
}

func dualWriteUserIDFromStateDedupeKey(dedupeKey string) (int64, bool) {
	_, userValue, ok := strings.Cut(dedupeKey, ":")
	if !ok {
		return 0, false
	}
	userID, err := strconv.ParseInt(userValue, 10, 64)
	if err != nil {
		return 0, false
	}
	return userID, true
}
