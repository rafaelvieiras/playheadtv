package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

func seedDB(t *testing.T) (string, *storage.SQLite) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "playheadtv.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES (?, 'T', 'self_hosted', 1)", api.DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if err := api.SeedAdminUser(ctx, db, "admin@example.com", "fake-seed-password"); err != nil {
		t.Fatal(err)
	}
	return path, db
}

// The command runs while the "server" (here db, an open connection with the
// single-connection pool) is alive on the same file.
func TestResetPasswordCommandBesideOpenServer(t *testing.T) {
	path, db := seedDB(t)
	var out, errOut bytes.Buffer
	if err := runResetPassword([]string{"--ttl", "5m"}, path, &out, &errOut); err != nil {
		t.Fatalf("%v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "admin@example.com") || !strings.Contains(out.String(), "/admin/reset-password#token=") {
		t.Fatalf("output = %q", out.String())
	}
	var n []int64
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM password_reset"); err != nil || n[0] != 1 {
		t.Fatalf("pending = %v %v", n, err)
	}
	// Running again replaces it.
	if err := runResetPassword(nil, path, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM password_reset"); err != nil || n[0] != 1 {
		t.Fatalf("pending after rerun = %v %v", n, err)
	}
}

func TestResetPasswordCommandWithoutServer(t *testing.T) {
	path, db := seedDB(t)
	db.Close()
	var out bytes.Buffer
	if err := runResetPassword(nil, path, &out, &out); err != nil {
		t.Fatal(err)
	}
}

func TestResetPasswordCommandErrors(t *testing.T) {
	path, _ := seedDB(t)
	var out bytes.Buffer
	cases := map[string][]string{
		"ttl too long":  {"--ttl", "24h"},
		"unknown email": {"--email", "nobody@example.com"},
		"extra arg":     {"stray"},
	}
	for name, args := range cases {
		if err := runResetPassword(args, path, &out, &out); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if err := runResetPassword(nil, missing, &out, &out); err == nil {
		t.Error("missing database: expected an error")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the command must not create a database")
	}
}

func TestResetPasswordCommandMigratesOldDatabase(t *testing.T) {
	path, db := seedDB(t)
	ctx := context.Background()
	db.Exec(ctx, "DROP TABLE password_reset")
	db.Exec(ctx, "DELETE FROM schema_migrations WHERE version LIKE '0022%'")
	db.Close()
	var out bytes.Buffer
	if err := runResetPassword(nil, path, &out, &out); err != nil {
		t.Fatal(err)
	}
}

func TestNoReferrerHeader(t *testing.T) {
	h := noReferrer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/reset-password", nil))
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
}
