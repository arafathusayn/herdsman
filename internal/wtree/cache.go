package wtree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CacheKind separates the values kept in the cache table.
type CacheKind string

const (
	CacheDivergence CacheKind = "divergence" // Ahead and behind for (head, base).
	CacheCommit     CacheKind = "commit"     // Time and subject of a commit.
)

// cacheBatch keeps a lookup well under SQLite's bound-variable limit.
const cacheBatch = 500

// CacheKey joins object ids into a key. Only full object ids are accepted: a
// result computed from immutable objects is valid forever, which is what lets
// the cache go without any timestamp check.
func CacheKey(objects ...string) (string, error) {
	if len(objects) == 0 {
		return "", errors.New("wtree: a cache key needs an object id")
	}
	for _, object := range objects {
		if !isObjectID(object) {
			return "", fmt.Errorf("wtree: %q is not an object id", object)
		}
	}
	return strings.Join(objects, ":"), nil
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return strings.Trim(value, "0123456789abcdef") == ""
}

// Cached returns the entries found for keys.
func (s *State) Cached(ctx context.Context, kind CacheKind, keys []string) (map[string]string, error) {
	found := make(map[string]string, len(keys))
	for start := 0; start < len(keys); start += cacheBatch {
		batch := keys[start:min(start+cacheBatch, len(keys))]
		args := make([]any, 0, len(batch)+1)
		args = append(args, string(kind))
		for _, key := range batch {
			args = append(args, key)
		}
		query := `SELECT key, value FROM caches WHERE kind = ? AND key IN (?` + strings.Repeat(",?", len(batch)-1) + `)`
		if err := s.readCached(ctx, query, args, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (s *State) readCached(ctx context.Context, query string, args []any, found map[string]string) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		found[key] = value
	}
	return rows.Err()
}

// PutCached stores entries in one transaction.
func (s *State) PutCached(ctx context.Context, kind CacheKind, entries map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := writeCached(ctx, tx, kind, entries); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func writeCached(ctx context.Context, tx *sql.Tx, kind CacheKind, entries map[string]string) error {
	now := time.Now().Unix()
	for key, value := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO caches (kind, key, value, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(kind, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			string(kind), key, value, now); err != nil {
			return err
		}
	}
	return nil
}
