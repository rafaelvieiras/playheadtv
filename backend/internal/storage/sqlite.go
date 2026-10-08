package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// SQLite is the self-hosted default storage.DB implementation: a local file,
// zero external dependency. See db.go for why this exists alongside a future
// D1 implementation.
type SQLite struct {
	db *sql.DB
}

// Open opens (creating if needed) a SQLite database at path. A single
// connection is enforced because SQLite allows only one writer at a time;
// serializing all access here avoids "database is locked" errors instead of
// surfacing them to callers.
func Open(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		// A second process on the same file (`server reset-password` run
		// beside a live server) waits for the lock instead of failing at once.
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("storage: %s: %w", pragma, err)
		}
	}

	return &SQLite{db: db}, nil
}

func (s *SQLite) Exec(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *SQLite) Query(ctx context.Context, dest any, query string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return scanRows(rows, dest)
}

// Transact implements DB.Transact with a real SQLite transaction.
func (s *SQLite) Transact(ctx context.Context, fn func(tx DB) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(sqlTx{tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// sqlTx is the DB view of an open transaction.
type sqlTx struct{ tx *sql.Tx }

func (t sqlTx) Exec(ctx context.Context, query string, args ...any) error {
	_, err := t.tx.ExecContext(ctx, query, args...)
	return err
}

func (t sqlTx) Query(ctx context.Context, dest any, query string, args ...any) error {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return scanRows(rows, dest)
}

// Transact on a transaction joins it (no nested transaction): fn runs in the
// enclosing one.
func (t sqlTx) Transact(ctx context.Context, fn func(tx DB) error) error { return fn(t) }

func (s *SQLite) Close() error {
	return s.db.Close()
}
