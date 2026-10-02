package catalogsync

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

func legacyJSON(t *testing.T, c legacyCriteria) *string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	return &s
}

func storedCriteria(t *testing.T, db storage.DB, table, id string) string {
	t.Helper()
	var out []string
	if err := db.Query(context.Background(), &out, "SELECT criteria FROM "+table+" WHERE id = ?", id); err != nil || len(out) != 1 {
		t.Fatalf("read %s %s criteria: %v %v", table, id, out, err)
	}
	return out[0]
}

func titles(t *testing.T, refs []ReviewItemRef) []string {
	t.Helper()
	var out []string
	for _, r := range refs {
		out = append(out, r.Title)
	}
	sort.Strings(out)
	return out
}

func TestMigrateLegacyCriteriaConvertsEquivalentSets(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	y := func(v int64) *int64 { return &v }
	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", y(1990))
	seedItem(t, db, "c2", "movie", "Comedy Two", "Comedy,Drama", y(2005))
	seedItem(t, db, "h1", "movie", "Horror One", "Horror", y(2005))
	seedItem(t, db, "e1", "episode", "Ep", "Comedy", y(2005))

	from, to := 2000, 2010
	seedPool(t, db, "pool-genre", "Comedy pool", legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"Comedy"}}))
	seedPool(t, db, "pool-years", "Years pool", legacyJSON(t, legacyCriteria{Kind: "movie", YearFrom: &from, YearTo: &to}))
	seedBlock(t, db, "blk-both", "Comedy 2000s", legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"comedy", "Horror"}, YearFrom: &from}))
	seedBlock(t, db, "blk-kind-year", "Episodes to 2005", legacyJSON(t, legacyCriteria{Kind: "episode", YearTo: &to}))

	// What each matched before.
	before := map[string][]string{}
	for table, ids := range map[string][]string{"filler_pool": {"pool-genre", "pool-years"}, "block": {"blk-both", "blk-kind-year"}} {
		for _, id := range ids {
			matched, err := matchStored(ctx, db, testTenantID, storedCriteria(t, db, table, id))
			if err != nil {
				t.Fatal(err)
			}
			before[id] = matched
		}
	}

	report, err := MigrateLegacyCriteria(ctx, db)
	if err != nil {
		t.Fatalf("MigrateLegacyCriteria: %v", err)
	}
	if report.Converted != 4 || report.Held != 0 {
		t.Fatalf("report = %+v, want 4 converted, 0 held", report)
	}
	for table, ids := range map[string][]string{"filler_pool": {"pool-genre", "pool-years"}, "block": {"blk-both", "blk-kind-year"}} {
		for _, id := range ids {
			raw := storedCriteria(t, db, table, id)
			if IsLegacyCriteria(raw) {
				t.Errorf("%s %s still legacy: %s", table, id, raw)
			}
			if _, err := ParseQuery([]byte(raw)); err != nil {
				t.Errorf("%s %s stored an invalid tree: %v", table, id, err)
			}
			after, err := matchStored(ctx, db, testTenantID, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before[id]) {
				t.Errorf("%s %s matches %v after, %v before", table, id, after, before[id])
			}
		}
	}
	review, err := ListCriteriaReview(ctx, db, testTenantID)
	if err != nil || len(review) != 0 {
		t.Errorf("review = %v %v, want empty", review, err)
	}

	// Idempotent: nothing left to do, nothing changes.
	stored := storedCriteria(t, db, "block", "blk-both")
	report, err = MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Converted != 0 || report.Held != 0 {
		t.Errorf("second run = %+v %v, want nothing to do", report, err)
	}
	if storedCriteria(t, db, "block", "blk-both") != stored {
		t.Error("a second run rewrote an already converted criteria")
	}
}

func TestMigrateLegacyCriteriaHoldsDifferencesForReview(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	y := func(v int64) *int64 { return &v }
	seedItem(t, db, "a1", "episode", "Action Ep", "Action", y(2000))
	seedItem(t, db, "a2", "episode", "Action Adventure Ep", "Action & Adventure", y(2001))
	// Accented text below is deliberate test data for the accent-insensitive matching.
	seedItem(t, db, "c1", "movie", "Comedia Movie", "Comédia", y(2000))
	seedItem(t, db, "c2", "movie", "Comedia Plain", "Comedia", y(2000))
	seedItem(t, db, "d1", "movie", "Drama Movie", "Drama", y(2000))

	// "action" is a substring of both genres; the exact translation only matches "Action".
	actionBlock := legacyJSON(t, legacyCriteria{Kind: "episode", Genres: []string{"Action"}})
	seedBlock(t, db, "blk-action", "Action", actionBlock)
	// Accent: the legacy substring matches the accented spelling only; the normalized exact match joins both.
	comedyPool := legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"Comédia"}})
	seedPool(t, db, "pool-comedia", "Comedia", comedyPool)
	// Untranslatable: no usable kind.
	badPool := `{}`
	seedPool(t, db, "pool-bad", "Bad", &badPool)
	// Unchanged set, converts.
	seedPool(t, db, "pool-drama", "Drama", legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"Drama"}}))
	// A genre that is not an existing one: legacy matches substrings, exact matches nothing.
	seedPool(t, db, "pool-sub", "Substring", legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"ram"}}))

	report, err := MigrateLegacyCriteria(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Converted != 1 || report.Held != 4 {
		t.Fatalf("report = %+v, want 1 converted (drama), 4 held", report)
	}
	// Nothing held was applied: the legacy value is untouched and still resolves as before.
	if got := storedCriteria(t, db, "block", "blk-action"); got != *actionBlock {
		t.Errorf("held block criteria changed to %s", got)
	}
	if got := storedCriteria(t, db, "filler_pool", "pool-drama"); IsLegacyCriteria(got) {
		t.Errorf("equivalent pool was not converted: %s", got)
	}
	if err := ResolveDynamic(ctx, db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamic over held legacy criteria: %v", err)
	}
	if got := blockItemCatalogIDs(t, db, "blk-action"); !reflect.DeepEqual(got, []string{"a2", "a1"}) { // title order
		t.Errorf("held legacy block still resolves %v, want both action episodes (its behavior before the migration)", got)
	}

	review, err := ListCriteriaReview(ctx, db, testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	byOwner := map[string]ReviewItem{}
	for _, r := range review {
		byOwner[r.OwnerID] = r
	}
	if len(review) != 4 {
		t.Fatalf("review has %d entries, want 4: %+v", len(review), review)
	}

	act := byOwner["blk-action"]
	if act.OwnerType != OwnerBlock || act.OwnerName != "Action" || act.BeforeCount != 2 || act.AfterCount != 1 {
		t.Errorf("action review = %+v", act)
	}
	if got := titles(t, act.OnlyBefore); !reflect.DeepEqual(got, []string{"Action Adventure Ep"}) || len(act.OnlyAfter) != 0 {
		t.Errorf("action diff: only_before = %v, only_after = %v, want the 'Action & Adventure' episode dropped", got, act.OnlyAfter)
	}
	if _, err := ParseQuery([]byte(mustJSON(act.ProposedQuery))); err != nil {
		t.Errorf("action proposal is not a valid query: %v", err)
	}
	if !strings.Contains(mustJSON(act.ProposedQuery), `"genre"`) || !sameJSON(act.LegacyCriteria, *actionBlock) {
		t.Errorf("action proposal/legacy = %s / %s", mustJSON(act.ProposedQuery), mustJSON(act.LegacyCriteria))
	}

	com := byOwner["pool-comedia"]
	if com.OwnerType != OwnerFillerPool || com.BeforeCount != 1 || com.AfterCount != 2 || len(com.OnlyBefore) != 0 ||
		!reflect.DeepEqual(titles(t, com.OnlyAfter), []string{"Comedia Plain"}) {
		t.Errorf("accent review = %+v", com)
	}

	sub := byOwner["pool-sub"]
	if sub.BeforeCount != 1 || sub.AfterCount != 0 || !reflect.DeepEqual(titles(t, sub.OnlyBefore), []string{"Drama Movie"}) {
		t.Errorf("substring review = %+v", sub)
	}

	bad := byOwner["pool-bad"]
	if bad.Problem == nil || bad.ProposedQuery != nil {
		t.Errorf("untranslatable review = %+v, want a problem and no proposal", bad)
	}

	// Running again keeps the held ones held, without duplicating review rows.
	report, err = MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Converted != 0 || report.Held != 4 {
		t.Errorf("second run = %+v %v", report, err)
	}
	if review, _ = ListCriteriaReview(ctx, db, testTenantID); len(review) != 4 {
		t.Errorf("review has %d entries after a second run, want 4", len(review))
	}

	// Once the catalog makes the sets equal, the next run converts it by itself.
	if err := Delete(ctx, db, "ms1", "ext-a2"); err != nil {
		t.Fatal(err)
	}
	report, err = MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Converted != 1 || report.Held != 3 {
		t.Errorf("after the catalog changed: %+v %v, want the action block converted", report, err)
	}
	if IsLegacyCriteria(storedCriteria(t, db, "block", "blk-action")) {
		t.Error("the action block is still legacy")
	}
	review, _ = ListCriteriaReview(ctx, db, testTenantID)
	for _, r := range review {
		if r.OwnerID == "blk-action" {
			t.Error("a converted block is still listed for review")
		}
	}

	// Rewriting a held owner's criteria (what a PUT does) clears its review row.
	if err := ClearCriteriaReview(ctx, db, testTenantID, OwnerFillerPool, "pool-sub"); err != nil {
		t.Fatal(err)
	}
	if review, _ = ListCriteriaReview(ctx, db, testTenantID); len(review) != 2 {
		t.Errorf("review has %d entries after clearing one, want 2", len(review))
	}
}

func TestMigrateLegacyCriteriaBackfillsGenreLinks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	y := func(v int64) *int64 { return &v }
	// seedItem writes the genres column only, like a row synced before migration 0009.
	seedItem(t, db, "x1", "movie", "Old One", "Comedy,Drama", y(1999))
	seedItem(t, db, "x2", "movie", "Old Two", "Drama", y(1999))
	seedPool(t, db, "p", "Comedy", legacyJSON(t, legacyCriteria{Kind: "movie", Genres: []string{"Comedy"}}))

	var links []int
	if err := db.Query(ctx, &links, "SELECT COUNT(*) FROM catalog_item_genre"); err != nil || links[0] != 0 {
		t.Fatalf("precondition: %v %v", links, err)
	}
	report, err := MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Converted != 1 || report.Held != 0 {
		t.Fatalf("report = %+v %v: without backfilled genre links the exact match would see nothing", report, err)
	}
	if err := db.Query(ctx, &links, "SELECT COUNT(*) FROM catalog_item_genre"); err != nil || links[0] != 3 {
		t.Errorf("genre links = %v %v, want 3", links, err)
	}
}

func TestBackfillOverviewNorm(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedItem(t, db, "o1", "movie", "With", "", nil)
	seedItem(t, db, "o2", "movie", "Without", "", nil)
	// Accented text below is deliberate test data for the accent-insensitive matching.
	if err := db.Exec(ctx, "UPDATE catalog_item SET overview = ? WHERE id = 'o1'", "Café au LAIT"); err != nil {
		t.Fatal(err)
	}
	if err := BackfillOverviewNorm(ctx, db); err != nil {
		t.Fatal(err)
	}
	var got []*string
	if err := db.Query(ctx, &got, "SELECT overview_norm FROM catalog_item ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] == nil || *got[0] != "cafe au lait" || got[1] != nil {
		t.Errorf("overview_norm = %v", got)
	}
}

// TestMigration0012OnPopulatedDatabase runs migration 0012 over a database at
// 0011 that holds dynamic blocks and pools, manual blocks with catalog and
// series items, and a series cursor, and checks that nothing is lost and the
// criteria item type is then accepted.
func TestMigration0012OnPopulatedDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	upTo11 := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "0012" {
			b, err := fs.ReadFile(migrations.FS, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			upTo11[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err := storage.RunMigrations(ctx, db, upTo11); err != nil {
		t.Fatalf("migrate to 0011: %v", err)
	}

	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("INSERT INTO tenant (id, name, tier, created_at) VALUES (?, 'T', ?, ?)", testTenantID, string(domain.TierSelfHosted), now)
	exec(`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', ?, 'local_folder', 's', 'push', ?, ?)`, testTenantID, now, now)
	y := func(v int64) *int64 { return &v }
	seedItem(t, db, "m1", "movie", "Movie", "Comedy", y(2000))
	seedItem(t, db, "m2", "movie", "Movie Two", "Comedy", y(2001))
	// Gives the new overview_norm column something to backfill.
	// Accented text below is deliberate test data for the accent-insensitive matching.
	exec("UPDATE catalog_item SET overview = 'Café' WHERE id = 'm1'")

	legacy := `{"kind":"movie","genres":["Comedy"]}`
	exec("INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', ?, 'Dyn', ?, ?, ?)", testTenantID, legacy, now, now)
	exec("INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('man', ?, 'Manual', ?, ?)", testTenantID, now, now)
	exec("INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES ('pool', ?, 'Pool', ?, ?)", testTenantID, legacy, now)
	exec("INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('bi1', 'man', 0, 'catalog', 'm1')")
	exec(`INSERT INTO block_item (id, block_id, position, item_type, spec) VALUES ('bi2', 'man', 1, 'series', '{"media_source_id":"ms1","series_title":"S","playback_order":"original","seed":1}')`)
	exec("INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('ch', ?, 1, 'C', 'UTC', ?, ?)", testTenantID, now, now)
	exec("INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, 'ch', 'bi2', 5)", testTenantID)

	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrate to 0012: %v", err)
	}

	var items []struct {
		ID       string
		ItemType string
	}
	if err := db.Query(ctx, &items, "SELECT id, item_type FROM block_item ORDER BY position"); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "bi1" || items[0].ItemType != "catalog" || items[1].ID != "bi2" || items[1].ItemType != "series" {
		t.Fatalf("block_item rows after migration = %+v", items)
	}
	var cursors []int
	if err := db.Query(ctx, &cursors, "SELECT COUNT(*) FROM series_cursor"); err != nil || cursors[0] != 1 {
		t.Errorf("series_cursor rows = %v %v, want the cursor kept", cursors, err)
	}
	// The criteria type is accepted now, and the CHECK still rejects a malformed one.
	exec(`INSERT INTO block_item (id, block_id, position, item_type, spec) VALUES ('bi3', 'man', 2, 'criteria', '{"query":{}}')`)
	if err := db.Exec(ctx, "INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('bi4', 'man', 3, 'criteria', 'm1', '{}')"); err == nil {
		t.Error("a criteria item with a catalog_item_id was accepted")
	}
	if err := db.Exec(ctx, "INSERT INTO block_item (id, block_id, position, item_type) VALUES ('bi5', 'man', 4, 'criteria')"); err == nil {
		t.Error("a criteria item without a spec was accepted")
	}
	// Deleting a block still cascades to its items.
	exec("DELETE FROM block WHERE id = 'man'")
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM block_item"); err != nil || n[0] != 0 {
		t.Errorf("block_item rows after deleting the block = %v %v", n, err)
	}

	// The data migration then converts the existing dynamic block and pool
	// with an unchanged matched set, comparing before and after.
	if err := BackfillOverviewNorm(ctx, db); err != nil {
		t.Fatal(err)
	}
	beforeBlock, err := matchStored(ctx, db, testTenantID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	report, err := MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Converted != 2 || report.Held != 0 {
		t.Fatalf("report = %+v %v", report, err)
	}
	afterBlock, err := matchStored(ctx, db, testTenantID, storedCriteria(t, db, "block", "dyn"))
	if err != nil || !reflect.DeepEqual(beforeBlock, afterBlock) || len(afterBlock) != 2 {
		t.Errorf("matched set before %v, after %v (%v)", beforeBlock, afterBlock, err)
	}
	var norm []*string
	if err := db.Query(ctx, &norm, "SELECT overview_norm FROM catalog_item WHERE id = 'm1'"); err != nil || norm[0] == nil || *norm[0] != "cafe" {
		t.Errorf("overview_norm = %v %v", norm, err)
	}
}

// sameJSON reports whether v (a decoded JSON value) equals the JSON text raw.
func sameJSON(v any, raw string) bool {
	var other any
	if err := json.Unmarshal([]byte(raw), &other); err != nil {
		return false
	}
	return reflect.DeepEqual(v, other)
}
