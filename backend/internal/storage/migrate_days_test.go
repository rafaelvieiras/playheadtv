package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// TestMigrationDaysOfWeek_ExistingWeeklyItemsBecomeAListOfOne applies every
// migration before the days_of_week one, seeds weekly/once/continuous items
// with the old single day_of_week, then applies the rest for real.
func TestMigrationDaysOfWeek_ExistingWeeklyItemsBecomeAListOfOne(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() >= "0016_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, before); err != nil {
		t.Fatalf("RunMigrations (before): %v", err)
	}

	now := int64(1700000000000)
	seed := func(query string, args ...any) {
		t.Helper()
		if err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed("INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', ?)", now)
	seed("INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES ('s1', 't1', 'S', ?, ?)", now, now)
	seed("INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('b1', 't1', 'B', ?, ?)", now, now)
	item := `INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
	         VALUES (?, 't1', 's1', 'b1', ?, ?, ?, ?, 1, ?, ?)`
	seed(item, "sun", "weekly", 0, "08:00", nil, now, now)
	seed(item, "thu", "weekly", 4, "22:00", nil, now, now)
	seed(item, "once", "once", nil, nil, now, now, now)
	seed(item, "cont", "continuous", nil, nil, nil, now, now)

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (days_of_week): %v", err)
	}

	var rows []struct {
		ID         string
		DaysOfWeek string
	}
	if err := db.Query(ctx, &rows, "SELECT id, days_of_week FROM schedule_item ORDER BY id"); err != nil {
		t.Fatalf("query: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.ID] = r.DaysOfWeek
	}
	want := map[string]string{"sun": "0", "thu": "4", "once": "", "cont": ""}
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("%s: days_of_week = %q (present %v), want %q", id, g, ok, w)
		}
	}

	// The old column is gone, not kept as an alias.
	if err := db.Exec(ctx, "SELECT day_of_week FROM schedule_item"); err == nil {
		t.Error("day_of_week still exists after the migration")
	}
}
