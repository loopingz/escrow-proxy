// Package index maintains a local SQLite index of cache entries for fast
// queries (cache list/show) and LRU eviction. It is L1-only — the index
// reflects what's on the local disk; cloud tiers manage their own
// lifecycle.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/loopingz/escrow-proxy/internal/storage"
)

// Entry is one row in the index. CreatedAt and LastAccessedAt are unix
// seconds.
type Entry struct {
	Key            string
	Method         string
	URL            string
	Status         int
	BodySize       int64
	CreatedAt      int64
	LastAccessedAt int64
	HitCount       int64
}

// ListFilter selects entries for List queries. Exactly one of Key, URL,
// URLPrefix may be set; Method narrows URL/URLPrefix; Limit caps rows
// (0 = unlimited).
type ListFilter struct {
	Key       string
	URL       string
	URLPrefix string
	Method    string
	Limit     int
}

// Options configures the in-memory hit map flush behavior.
type Options struct {
	// FlushInterval is the maximum time between automatic flushes of the
	// hit map. Zero means default (30s).
	FlushInterval time.Duration
	// FlushThreshold triggers a flush when the dirty map size meets this
	// count. Zero means default (1000).
	FlushThreshold int
}

const (
	defaultFlushInterval  = 30 * time.Second
	defaultFlushThreshold = 1000
)

// Index is the local SQLite cache index.
type Index struct {
	db *sql.DB

	// hit map for buffered Touch updates
	mu       sync.Mutex
	dirty    map[string]*hitDelta
	signal   chan struct{} // notifies the flusher of a threshold breach
	stop     chan struct{}
	stopped  chan struct{}
	options  Options
}

type hitDelta struct {
	hits           int64
	lastAccessedAt int64
	// meta is set by RecordHit so the flusher can INSERT a row when
	// the UPDATE finds nothing (lazy reconcile). nil for plain Touch.
	meta *Entry
}

// Open creates or opens the SQLite database at path, applies the schema,
// and starts the flush goroutine.
func Open(path string, opts Options) (*Index, error) {
	if opts.FlushInterval == 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.FlushThreshold == 0 {
		opts.FlushThreshold = defaultFlushThreshold
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc.org/sqlite serializes writes; cap connections to keep things
	// predictable.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	idx := &Index{
		db:      db,
		dirty:   make(map[string]*hitDelta),
		signal:  make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
		options: opts,
	}
	go idx.flushLoop()
	return idx, nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS entries (
    key              TEXT PRIMARY KEY,
    method           TEXT NOT NULL,
    url              TEXT NOT NULL,
    status           INTEGER NOT NULL,
    body_size        INTEGER NOT NULL,
    created_at       INTEGER NOT NULL,
    last_accessed_at INTEGER NOT NULL,
    hit_count        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_entries_url           ON entries(url);
CREATE INDEX IF NOT EXISTS idx_entries_last_accessed ON entries(last_accessed_at);

CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO schema_version VALUES (1);
`

// Close drains pending Touch updates, stops the flush goroutine, and
// closes the database.
func (i *Index) Close() error {
	close(i.stop)
	<-i.stopped
	return i.db.Close()
}

// Insert writes (or replaces) the row for e.Key.
func (i *Index) Insert(ctx context.Context, e Entry) error {
	_, err := i.db.ExecContext(ctx, `
		INSERT INTO entries (key, method, url, status, body_size, created_at, last_accessed_at, hit_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			method = excluded.method,
			url = excluded.url,
			status = excluded.status,
			body_size = excluded.body_size,
			created_at = excluded.created_at,
			last_accessed_at = excluded.last_accessed_at,
			hit_count = excluded.hit_count
	`, e.Key, e.Method, e.URL, e.Status, e.BodySize, e.CreatedAt, e.LastAccessedAt, e.HitCount)
	if err != nil {
		return fmt.Errorf("insert %s: %w", e.Key, err)
	}
	return nil
}

// Upsert refreshes the meta columns for an existing row while preserving
// usage stats. On a missing row, inserts with current time and hit_count=0.
func (i *Index) Upsert(ctx context.Context, e Entry) error {
	now := time.Now().Unix()
	_, err := i.db.ExecContext(ctx, `
		INSERT INTO entries (key, method, url, status, body_size, created_at, last_accessed_at, hit_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(key) DO UPDATE SET
			method = excluded.method,
			url = excluded.url,
			status = excluded.status,
			body_size = excluded.body_size
	`, e.Key, e.Method, e.URL, e.Status, e.BodySize, now, now)
	if err != nil {
		return fmt.Errorf("upsert %s: %w", e.Key, err)
	}
	return nil
}

// Get returns the row for key, or storage.ErrNotFound.
func (i *Index) Get(ctx context.Context, key string) (Entry, error) {
	var e Entry
	err := i.db.QueryRowContext(ctx, `
		SELECT key, method, url, status, body_size, created_at, last_accessed_at, hit_count
		FROM entries
		WHERE key = ?
	`, key).Scan(&e.Key, &e.Method, &e.URL, &e.Status, &e.BodySize, &e.CreatedAt, &e.LastAccessedAt, &e.HitCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("get %s: %w", key, err)
	}
	return e, nil
}

// Delete removes the row for key. A missing key is not an error.
func (i *Index) Delete(ctx context.Context, key string) error {
	_, err := i.db.ExecContext(ctx, `DELETE FROM entries WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	// Drop any pending hits for this key so a flush after Delete doesn't
	// resurrect a stat update for a now-gone row.
	i.mu.Lock()
	delete(i.dirty, key)
	i.mu.Unlock()
	return nil
}

// List returns rows matching filter, ordered by last_accessed_at DESC
// (most recent first).
func (i *Index) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	q := `SELECT key, method, url, status, body_size, created_at, last_accessed_at, hit_count FROM entries`
	var args []any
	var clauses []string
	switch {
	case f.Key != "":
		clauses = append(clauses, "key = ?")
		args = append(args, f.Key)
	case f.URL != "":
		clauses = append(clauses, "url = ?")
		args = append(args, f.URL)
	case f.URLPrefix != "":
		clauses = append(clauses, "url LIKE ?")
		args = append(args, f.URLPrefix+"%")
	}
	if f.Method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}
	if len(clauses) > 0 {
		q += " WHERE " + clauses[0]
		for _, c := range clauses[1:] {
			q += " AND " + c
		}
	}
	q += " ORDER BY last_accessed_at DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := i.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Method, &e.URL, &e.Status, &e.BodySize, &e.CreatedAt, &e.LastAccessedAt, &e.HitCount); err != nil {
			return nil, fmt.Errorf("list scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count returns the number of rows in the index.
func (i *Index) Count(ctx context.Context) (int64, error) {
	var n int64
	err := i.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries`).Scan(&n)
	return n, err
}

// TotalBodySize returns the sum of body_size across all rows.
func (i *Index) TotalBodySize(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := i.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(body_size), 0) FROM entries`).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// LRUEntries returns up to limit entries ordered by last_accessed_at ASC.
// If cutoff > 0, only entries with last_accessed_at < cutoff are returned.
// Limit 0 means unlimited.
func (i *Index) LRUEntries(ctx context.Context, cutoff int64, limit int) ([]Entry, error) {
	q := `SELECT key, method, url, status, body_size, created_at, last_accessed_at, hit_count FROM entries`
	var args []any
	if cutoff > 0 {
		q += ` WHERE last_accessed_at < ?`
		args = append(args, cutoff)
	}
	q += ` ORDER BY last_accessed_at ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := i.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("lru: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Method, &e.URL, &e.Status, &e.BodySize, &e.CreatedAt, &e.LastAccessedAt, &e.HitCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllKeys returns every key currently in the index. Used by reindex to
// detect orphan rows (in DB, not on disk). Caller may receive a large
// slice; for 200G with 200K-2M entries this is acceptable.
func (i *Index) AllKeys(ctx context.Context) ([]string, error) {
	rows, err := i.db.QueryContext(ctx, `SELECT key FROM entries`)
	if err != nil {
		return nil, fmt.Errorf("all keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
