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

func (s *SQLite) Close() error {
	return s.db.Close()
}
