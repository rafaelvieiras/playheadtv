package storage

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at INTEGER NOT NULL
)`

// RunMigrations applies every *.sql file in fsys that isn't already recorded
// in schema_migrations, in filename order (0001_, 0002_, ...). It only talks
// to db through the DB interface, so it works the same against SQLite today
// and against a future D1 implementation — both just run SQL over a
// connection, D1's just happens to be HTTP-based.
//
// Known limitation: each statement within a migration file runs as its own
// Exec, not wrapped in a single transaction — acceptable with one migration
// file; revisit if that stops being true.
func RunMigrations(ctx context.Context, db DB, fsys fs.FS) error {
	if err := db.Exec(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	var applied []string
	if err := db.Query(ctx, &applied, "SELECT version FROM schema_migrations"); err != nil {
		return fmt.Errorf("storage: list applied migrations: %w", err)
	}
	appliedSet := make(map[string]bool, len(applied))
	for _, v := range applied {
		appliedSet[v] = true
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("storage: read migrations dir: %w", err)
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		versions = append(versions, e.Name())
	}
	sort.Strings(versions)

	for _, version := range versions {
		if appliedSet[version] {
			continue
		}
		if err := applyMigration(ctx, db, fsys, version); err != nil {
			return fmt.Errorf("storage: apply migration %s: %w", version, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db DB, fsys fs.FS, version string) error {
	content, err := fs.ReadFile(fsys, version)
	if err != nil {
		return err
	}

	for _, stmt := range splitStatements(string(content)) {
		if err := db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt, err)
		}
	}

	return db.Exec(ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		version, time.Now().UnixMilli(),
	)
}

// splitStatements strips "--" line comments (a comment can itself contain a
// ";", e.g. "-- IANA name; resolves ..." in 0001_init.sql) and then does a
// naive split on ";" — sufficient for hand-written migrations that don't
// embed semicolons inside string literals or trigger bodies. Revisit with a
// real SQL statement splitter if that stops holding.
func splitStatements(sql string) []string {
	var cleaned strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		cleaned.WriteString(line)
		cleaned.WriteByte('\n')
	}

	var out []string
	for _, stmt := range strings.Split(cleaned.String(), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		out = append(out, stmt)
	}
	return out
}
