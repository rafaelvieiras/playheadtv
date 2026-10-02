package catalogsync

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// The non-ASCII strings below are deliberate test data: accent-insensitive
// matching is the behavior under test.
func TestFoldText(t *testing.T) {
	cases := map[string]string{
		"Episódio":    "episodio",
		"episodio":    "episodio",
		"AÇÃO":        "acao",
		"Ação":        "acao",
		"Über Straße": "uber strasse",
		"Plain":       "plain",
	}
	for in, want := range cases {
		if got := FoldText(in); got != want {
			t.Errorf("FoldText(%q) = %q, want %q", in, got, want)
		}
	}
}

func searchIDs(t *testing.T, db storage.DB, q string) []string {
	t.Helper()
	clause, arg := SearchClause(q)
	var ids []string
	if err := db.Query(context.Background(), &ids, "SELECT external_id FROM catalog_item WHERE 1=1"+clause+" ORDER BY external_id", arg); err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUpsertMaintainsSearchText(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	series := "Série Nova"
	// "Episódio" / "Ação" are deliberate accented test data.
	items := []Item{
		{ExternalID: "a", Kind: domain.CatalogEpisode, Title: "Episódio Um", SeriesTitle: &series, DurationMs: 1000},
		{ExternalID: "b", Kind: domain.CatalogMovie, Title: "Ação Total", DurationMs: 1000},
	}
	for _, it := range items {
		if err := Upsert(ctx, db, testTenantID, "ms1", it, 1); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	for q, want := range map[string][]string{
		"episodio": {"a"}, "EPISÓDIO": {"a"}, "acao": {"b"}, "Ação": {"b"},
		"serie nova": {"a"}, "SÉRIE": {"a"}, "nothing": nil,
	} {
		if got := searchIDs(t, db, q); !equalStrings(got, want) {
			t.Errorf("search %q = %v, want %v", q, got, want)
		}
	}

	// Updating an existing row (conflict path) must refresh search_text.
	if err := Upsert(ctx, db, testTenantID, "ms1", Item{ExternalID: "b", Kind: domain.CatalogMovie, Title: "Comédia", DurationMs: 1000}, 2); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if got := searchIDs(t, db, "acao"); len(got) != 0 {
		t.Errorf("old title still matches after update: %v", got)
	}
	if got := searchIDs(t, db, "comedia"); !equalStrings(got, []string{"b"}) {
		t.Errorf("new title search = %v, want [b]", got)
	}
}

func TestBackfillSearchTextOnUpgradedDB(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Schema one migration behind: everything before 0008.
	entries, _ := fs.ReadDir(migrations.FS, ".")
	pre := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() >= "0008_" {
			continue
		}
		b, _ := fs.ReadFile(migrations.FS, e.Name())
		pre[e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := storage.RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("pre-0008 migrations: %v", err)
	}
	seed := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'T', 'self_hosted', 1)")
	seed("INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 't1', 'local_folder', 'L', 'push', 1, 1)")
	seed("INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, synced_at) VALUES ('c1', 't1', 'ms1', 'old1', 'episode', 'Episódio Antigo', 'Minha Série', 1000, 1)")
	seed("INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at) VALUES ('c2', 't1', 'ms1', 'old2', 'movie', 'Ação', 1000, 1)")

	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("apply 0008: %v", err)
	}
	if got := searchIDs(t, db, "acao"); len(got) != 0 {
		t.Fatalf("before backfill nothing should match, got %v", got)
	}
	if err := BackfillSearchText(ctx, db); err != nil {
		t.Fatalf("BackfillSearchText: %v", err)
	}
	// Idempotent.
	if err := BackfillSearchText(ctx, db); err != nil {
		t.Fatalf("BackfillSearchText (2nd): %v", err)
	}
	if got := searchIDs(t, db, "acao"); !equalStrings(got, []string{"old2"}) {
		t.Errorf("acao = %v, want [old2]", got)
	}
	if got := searchIDs(t, db, "EPISODIO"); !equalStrings(got, []string{"old1"}) {
		t.Errorf("EPISODIO = %v, want [old1]", got)
	}
	if got := searchIDs(t, db, "minha serie"); !equalStrings(got, []string{"old1"}) {
		t.Errorf("series search = %v, want [old1]", got)
	}
}
