package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

func TestSQLiteMigrateAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "playheadtv.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// Running twice must be a no-op, not an error.
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (second run): %v", err)
	}

	want := domain.Tenant{
		ID:        "t1",
		Name:      "Test Instance",
		Tier:      domain.TierSelfHosted,
		CreatedAt: 1700000000000,
	}
	err = db.Exec(ctx,
		"INSERT INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		want.ID, want.Name, string(want.Tier), want.CreatedAt,
	)
	if err != nil {
		t.Fatalf("Exec insert tenant: %v", err)
	}

	var got domain.Tenant
	if err := db.Query(ctx, &got, "SELECT id, name, tier, created_at FROM tenant WHERE id = ?", want.ID); err != nil {
		t.Fatalf("Query tenant: %v", err)
	}
	if got != want {
		t.Errorf("round-tripped tenant = %+v, want %+v", got, want)
	}

	// A media_source exercises the nullable-pointer-field scan path
	// (BaseURL, CredentialCipher, LastProbeAt, LastProbeOK).
	err = db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, base_url, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'jellyfin', 'Home Jellyfin', ?, 'pull', ?, ?)`,
		"ms1", want.ID, "http://jellyfin.example.com", want.CreatedAt, want.CreatedAt,
	)
	if err != nil {
		t.Fatalf("Exec insert media_source: %v", err)
	}

	var source domain.MediaSource
	err = db.Query(ctx, &source,
		"SELECT id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at FROM media_source WHERE id = ?",
		"ms1",
	)
	if err != nil {
		t.Fatalf("Query media_source: %v", err)
	}
	if source.BaseURL == nil || *source.BaseURL != "http://jellyfin.example.com" {
		t.Errorf("source.BaseURL = %v, want http://jellyfin.example.com", source.BaseURL)
	}
	if source.LastProbeAt != nil {
		t.Errorf("source.LastProbeAt = %v, want nil", source.LastProbeAt)
	}
	if source.LastProbeOK != nil {
		t.Errorf("source.LastProbeOK = %v, want nil", source.LastProbeOK)
	}

	var tenants []domain.Tenant
	if err := db.Query(ctx, &tenants, "SELECT id, name, tier, created_at FROM tenant"); err != nil {
		t.Fatalf("Query all tenants: %v", err)
	}
	if len(tenants) != 1 || tenants[0] != want {
		t.Errorf("tenants = %+v, want [%+v]", tenants, want)
	}

	if err := db.Query(ctx, new(domain.Tenant), "SELECT id, name, tier, created_at FROM tenant WHERE id = ?", "missing"); err == nil {
		t.Error("Query for missing tenant: want an error (sql.ErrNoRows), got nil")
	}
}
