package index

import (
	"context"
	"fmt"
	"time"
)

// Touch buffers a hit for key. On flush, the hit becomes an UPDATE; if
// the row doesn't exist the hit is dropped. Use RecordHit instead when
// the caller can supply meta to reconcile a missing row.
func (i *Index) Touch(key string) {
	i.bufferHit(key, nil, 0)
}

// RecordHit buffers a hit and carries the entry meta so that on flush, a
// missing row is reconciled via INSERT (preserving usage stats already
// in flight). Use this from Cache.Get hot path.
func (i *Index) RecordHit(e Entry) {
	i.bufferHit(e.Key, &e, e.BodySize)
}

func (i *Index) bufferHit(key string, meta *Entry, _ int64) {
	now := time.Now().Unix()
	i.mu.Lock()
	d, ok := i.dirty[key]
	if !ok {
		d = &hitDelta{}
		i.dirty[key] = d
	}
	d.hits++
	if now > d.lastAccessedAt {
		d.lastAccessedAt = now
	}
	if meta != nil {
		d.meta = meta
	}
	overThreshold := len(i.dirty) >= i.options.FlushThreshold
	i.mu.Unlock()

	if overThreshold {
		select {
		case i.signal <- struct{}{}:
		default:
		}
	}
}

// Flush drains the in-memory hit map into a single SQL transaction. Safe
// to call concurrently; one caller wins the swap and the rest are no-ops.
func (i *Index) Flush(ctx context.Context) error {
	i.mu.Lock()
	if len(i.dirty) == 0 {
		i.mu.Unlock()
		return nil
	}
	pending := i.dirty
	i.dirty = make(map[string]*hitDelta)
	i.mu.Unlock()

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		i.requeue(pending)
		return fmt.Errorf("begin tx: %w", err)
	}
	updateStmt, err := tx.PrepareContext(ctx, `
		UPDATE entries
		SET last_accessed_at = MAX(last_accessed_at, ?),
		    hit_count = hit_count + ?
		WHERE key = ?
	`)
	if err != nil {
		_ = tx.Rollback()
		i.requeue(pending)
		return fmt.Errorf("prepare flush: %w", err)
	}
	defer updateStmt.Close()

	upsertStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO entries
		    (key, method, url, status, body_size, created_at, last_accessed_at, hit_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
		    last_accessed_at = MAX(last_accessed_at, excluded.last_accessed_at),
		    hit_count = hit_count + excluded.hit_count
	`)
	if err != nil {
		_ = tx.Rollback()
		i.requeue(pending)
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer upsertStmt.Close()

	for key, d := range pending {
		if d.meta != nil {
			// RecordHit path: INSERT or bump stats on conflict.
			if _, err := upsertStmt.ExecContext(ctx,
				key, d.meta.Method, d.meta.URL, d.meta.Status, d.meta.BodySize,
				d.lastAccessedAt, d.lastAccessedAt, d.hits,
			); err != nil {
				_ = tx.Rollback()
				i.requeue(pending)
				return fmt.Errorf("flush upsert %s: %w", key, err)
			}
			continue
		}
		// Plain Touch: UPDATE only; missing rows silently dropped.
		if _, err := updateStmt.ExecContext(ctx, d.lastAccessedAt, d.hits, key); err != nil {
			_ = tx.Rollback()
			i.requeue(pending)
			return fmt.Errorf("flush exec %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		i.requeue(pending)
		return fmt.Errorf("flush commit: %w", err)
	}
	return nil
}

// requeue merges a previously-dequeued batch back into the dirty map on
// flush failure. Newer Touch updates that arrived in the meantime take
// precedence for last_accessed_at via MAX semantics; hits accumulate.
func (i *Index) requeue(pending map[string]*hitDelta) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for key, d := range pending {
		existing, ok := i.dirty[key]
		if !ok {
			i.dirty[key] = d
			continue
		}
		existing.hits += d.hits
		if d.lastAccessedAt > existing.lastAccessedAt {
			existing.lastAccessedAt = d.lastAccessedAt
		}
	}
}

func (i *Index) flushLoop() {
	defer close(i.stopped)
	timer := time.NewTimer(i.options.FlushInterval)
	defer timer.Stop()

	for {
		select {
		case <-i.stop:
			_ = i.Flush(context.Background())
			return
		case <-timer.C:
			_ = i.Flush(context.Background())
			timer.Reset(i.options.FlushInterval)
		case <-i.signal:
			_ = i.Flush(context.Background())
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(i.options.FlushInterval)
		}
	}
}
