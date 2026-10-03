package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// oldStateDB opens a database migrated only up to 0013 (the state of an
// install before collections existed), with a tenant and a media source. The
// returned function applies every later migration, like a restart does.
func oldStateDB(t *testing.T) (*storage.SQLite, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0014_" {
			continue
		}
		b, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := storage.RunMigrations(ctx, db, pre); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('tenant1', 'T', 'self_hosted', 1)",
		"INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 'tenant1', 'local_folder', 'seed', 'push', 1, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = now
	return db, func() {
		t.Helper()
		if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
	}
}

func treeGenre(g string) string {
	return fmt.Sprintf(`{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":%q}]}`, g)
}

func treeYears(from, to int) string {
	return fmt.Sprintf(`{"version":1,"all":[{"field":"year","op":"gte","value":%d},{"field":"year","op":"lte","value":%d}]}`, from, to)
}

func mustExec(t *testing.T, db storage.DB, q string, args ...any) {
	t.Helper()
	if err := db.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func queryStrings(t *testing.T, db storage.DB, q string, args ...any) []string {
	t.Helper()
	var out []string
	if err := db.Query(context.Background(), &out, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

// ownerSets reads what every dynamic owner holds right now, keyed by owner:
// block_item catalog ids by position, filler_pool_item ids by rowid, and, for
// criteria items, the match set of the query the item currently evaluates
// (its collection's when it has one, else its embedded query).
func ownerSets(t *testing.T, db storage.DB) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	for _, id := range queryStrings(t, db, "SELECT id FROM block WHERE criteria IS NOT NULL OR collection_id IS NOT NULL") {
		out["block:"+id] = strings.Join(queryStrings(t, db, "SELECT catalog_item_id FROM block_item WHERE block_id = ? ORDER BY position", id), ",")
	}
	for _, id := range queryStrings(t, db, "SELECT id FROM filler_pool WHERE criteria IS NOT NULL OR collection_id IS NOT NULL") {
		out["pool:"+id] = strings.Join(queryStrings(t, db, "SELECT catalog_item_id FROM filler_pool_item WHERE filler_pool_id = ? ORDER BY rowid", id), ",")
	}
	var items []struct {
		ID   string
		Spec string
	}
	if err := db.Query(ctx, &items, "SELECT id, spec FROM block_item WHERE item_type = 'criteria'"); err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		var spec domain.CriteriaSpec
		if err := json.Unmarshal([]byte(it.Spec), &spec); err != nil {
			t.Fatal(err)
		}
		raw := string(spec.Query)
		if spec.CollectionID != "" {
			raw = queryStrings(t, db, "SELECT query FROM collection WHERE id = ?", spec.CollectionID)[0]
		}
		q, err := ParseQuery([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		ids, err := MatchQuery(ctx, db, "tenant1", q)
		if err != nil {
			t.Fatal(err)
		}
		out["item:"+it.ID] = strings.Join(ids, ",")
	}
	return out
}

// seedManyOwners seeds a library and many owners over a handful of distinct
// trees: blocks, pools and criteria items spread over manual blocks.
func seedManyOwners(t *testing.T, db storage.DB, blocks, pools, itemBlocks int) {
	t.Helper()
	genres := []string{"Comedy", "Drama", "Horror", "Action", "SciFi"}
	for i := 0; i < 200; i++ {
		y := int64(1980 + i%40)
		seedItem(t, db, fmt.Sprintf("ci%03d", i), "movie", fmt.Sprintf("Movie %03d", i), genres[i%5], &y)
	}
	// Ten distinct trees for the blocks: five genres and five year ranges.
	blockTrees := make([]string, 10)
	for i := range blockTrees {
		if i < 5 {
			blockTrees[i] = treeGenre(genres[i])
		} else {
			blockTrees[i] = treeYears(1980+(i-5)*5, 1984+(i-5)*5)
		}
	}
	now := time.Now().UnixMilli()
	for i := 0; i < blocks; i++ {
		mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES (?, 'tenant1', ?, ?, ?, ?)",
			fmt.Sprintf("blk%04d", i), fmt.Sprintf("Block %04d", i), blockTrees[i%10], now, now)
	}
	for i := 0; i < pools; i++ {
		mustExec(t, db, "INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES (?, 'tenant1', ?, ?, ?)",
			fmt.Sprintf("pool%04d", i), fmt.Sprintf("Pool %04d", i), blockTrees[i%5], now)
	}
	for i := 0; i < itemBlocks; i++ {
		id := fmt.Sprintf("man%04d", i)
		mustExec(t, db, "INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES (?, 'tenant1', ?, ?, ?)", id, fmt.Sprintf("Manual %04d", i), now, now)
		tree := blockTrees[i%3]
		if i%7 == 0 {
			tree = treeYears(2001, 2003) // a tree only a criteria item uses
		}
		spec := fmt.Sprintf(`{"query":%s,"playback_order":"title","seed":5,"amount":{"mode":"all"}}`, tree)
		mustExec(t, db, "INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, 0, 'criteria', NULL, ?)", fmt.Sprintf("it%04d", i), id, spec)
	}
}

func TestMigrateToCollectionsManyOwners(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 600, 120, 70)
	upgrade()
	if err := BackfillGenreLinks(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ResolveDynamic(ctx, db, "tenant1"); err != nil { // resolved from the deprecated columns, like before the upgrade
		t.Fatal(err)
	}
	before := ownerSets(t, db)
	if len(before) != 600+120+70 {
		t.Fatalf("owners = %d", len(before))
	}
	var nonEmpty int
	for _, v := range before {
		if v != "" {
			nonEmpty++
		}
	}
	if nonEmpty < len(before)-10 {
		t.Fatalf("only %d of %d owners match anything; the test would prove nothing", nonEmpty, len(before))
	}
	criteriaBefore := queryStrings(t, db, "SELECT criteria FROM block WHERE id = 'blk0003'")[0]

	start := time.Now()
	report, err := MigrateToCollections(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("migrated %d owners onto %d collections in %s", report.Assigned, report.Created, time.Since(start))
	// 10 trees for the blocks (pools reuse five of them, items three) plus the item-only one.
	if report.Assigned != 790 || report.Created != 11 || report.Held != 0 {
		t.Fatalf("report = %+v, want 790 owners on 11 collections", report)
	}
	if n := queryStrings(t, db, "SELECT COUNT(*) FROM collection")[0]; n != "11" {
		t.Fatalf("collections = %s, want 11", n)
	}
	if n := queryStrings(t, db, `SELECT (SELECT COUNT(*) FROM block WHERE criteria IS NOT NULL AND collection_id IS NULL)
		+ (SELECT COUNT(*) FROM filler_pool WHERE criteria IS NOT NULL AND collection_id IS NULL)
		+ (SELECT COUNT(*) FROM block_item WHERE item_type = 'criteria' AND json_extract(spec, '$.collection_id') IS NULL)`)[0]; n != "0" {
		t.Fatalf("%s owners are still without a collection", n)
	}
	// Owners with identical trees share one collection, named after the first
	// owner (blocks first, by name); a tree only a criteria item uses is named after its block.
	if name := queryStrings(t, db, "SELECT c.name FROM block b JOIN collection c ON c.id = b.collection_id WHERE b.id = 'blk0003'")[0]; name != "Block 0003" {
		t.Errorf("collection of the first owner of tree 3 = %q", name)
	}
	if a, b := queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'blk0003'")[0], queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'blk0013'")[0]; a != b {
		t.Error("two blocks with the same tree got different collections")
	}
	if a, b := queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'blk0001'")[0], queryStrings(t, db, "SELECT collection_id FROM filler_pool WHERE id = 'pool0001'")[0]; a != b {
		t.Error("a block and a pool with the same tree got different collections")
	}
	if names := queryStrings(t, db, "SELECT name FROM collection WHERE name LIKE 'Manual 0000 - item %'"); len(names) != 1 || names[0] != "Manual 0000 - item 1" {
		t.Errorf("item-only collection names = %v", names)
	}
	// The deprecated columns and the embedded query are untouched snapshots.
	if got := queryStrings(t, db, "SELECT criteria FROM block WHERE id = 'blk0003'")[0]; got != criteriaBefore {
		t.Errorf("deprecated criteria was rewritten: %s", got)
	}
	if spec := queryStrings(t, db, "SELECT spec FROM block_item WHERE id = 'it0001'")[0]; !strings.Contains(spec, `"collection_id"`) || !strings.Contains(spec, `"query"`) {
		t.Errorf("item spec = %s, want the collection id next to the snapshot", spec)
	}

	// Matched sets are identical per owner once everything resolves through collections.
	mustExec(t, db, "UPDATE block SET criteria = '{\"stale\":true}' WHERE collection_id IS NOT NULL") // no longer read
	mustExec(t, db, "UPDATE filler_pool SET criteria = '{\"stale\":true}' WHERE collection_id IS NOT NULL")
	if err := ResolveDynamic(ctx, db, "tenant1"); err != nil {
		t.Fatal(err)
	}
	after := ownerSets(t, db)
	if len(after) != len(before) {
		t.Fatalf("owners after = %d, before = %d", len(after), len(before))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s: matched set changed\n before %s\n after  %s", k, v, after[k])
		}
	}

	// Idempotent and re-runnable: nothing left to do, nothing duplicated.
	report, err = MigrateToCollections(ctx, db)
	if err != nil || report != (CollectionMigrationReport{}) {
		t.Fatalf("second run = %+v %v, want nothing", report, err)
	}
	// A run that did not reach some owners (a crash between batches): they
	// join the existing collections instead of creating new ones.
	mustExec(t, db, "UPDATE block SET collection_id = NULL, criteria = ? WHERE id LIKE 'blk01%'", treeGenre("Comedy"))
	report, err = MigrateToCollections(ctx, db)
	if err != nil || report.Assigned != 100 || report.Created != 0 {
		// The 100 blocks are back on the Comedy tree, which a collection already holds.
		t.Fatalf("resumed run = %+v %v", report, err)
	}
	if n := queryStrings(t, db, "SELECT COUNT(*) FROM collection")[0]; n != "11" {
		t.Fatalf("collections after resume = %s", n)
	}
}

func TestMigrateToCollectionsMismatchIsHeldAndReported(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 6, 2, 2)
	upgrade()
	if err := BackfillGenreLinks(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Make the "after" side of the comparison lose one item for blk0002 only.
	collectionAfterHook = func(ownerID string, ids []string) []string {
		if ownerID == "blk0002" && len(ids) > 0 {
			return ids[1:]
		}
		return ids
	}
	t.Cleanup(func() { collectionAfterHook = nil })

	report, err := MigrateToCollections(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Held != 1 || report.Assigned != 9 {
		t.Fatalf("report = %+v, want 1 held and 9 migrated", report)
	}
	if got := queryStrings(t, db, "SELECT COALESCE(collection_id, 'none') FROM block WHERE id = 'blk0002'")[0]; got != "none" {
		t.Fatalf("a mismatching owner was applied: collection %s", got)
	}
	review, err := ListCriteriaReview(ctx, db, "tenant1")
	if err != nil || len(review) != 1 {
		t.Fatalf("review = %+v %v", review, err)
	}
	r := review[0]
	if r.OwnerType != OwnerBlock || r.OwnerID != "blk0002" || r.OwnerName != "Block 0002" || r.BeforeCount != r.AfterCount+1 || len(r.OnlyBefore) != 1 || r.ProposedQuery == nil {
		t.Errorf("review item = %+v", r)
	}
	// It keeps playing from its stored query meanwhile.
	if err := ResolveDynamic(ctx, db, "tenant1"); err != nil {
		t.Fatal(err)
	}
	if n := queryStrings(t, db, "SELECT COUNT(*) FROM block_item WHERE block_id = 'blk0002'")[0]; n == "0" {
		t.Error("the held block lost its items")
	}

	// Once the comparison agrees (hook removed), the next run converts it and drops the review entry.
	collectionAfterHook = nil
	report, err = MigrateToCollections(ctx, db)
	if err != nil || report.Assigned != 1 || report.Held != 0 {
		t.Fatalf("rerun = %+v %v", report, err)
	}
	if review, _ = ListCriteriaReview(ctx, db, "tenant1"); len(review) != 0 {
		t.Errorf("review after the rerun = %+v", review)
	}
}

func TestMigrateToCollectionsHoldsAnUnparseableQueryAndSkipsLegacyRows(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 0, 0, 0)
	now := time.Now().UnixMilli()
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('bad', 'tenant1', 'Bad', ?, ?, ?)",
		`{"version":1,"all":[{"field":"nope","op":"equals","value":"x"}]}`, now, now)
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('legacy', 'tenant1', 'Legacy', ?, ?, ?)",
		`{"kind":"movie","genres":["Comedy"]}`, now, now)
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('man', 'tenant1', 'Man', ?, ?)", now, now)
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('manual-only', 'tenant1', 'Manual only', ?, ?)", now, now)
	mustExec(t, db, "INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('badit', 'man', 0, 'criteria', NULL, ?)",
		`{"query":{"version":1,"all":[]},"playback_order":"title","seed":1,"amount":{"mode":"all"}}`)
	upgrade()

	report, err := MigrateToCollections(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Assigned != 0 || report.Created != 0 || report.Held != 2 {
		t.Fatalf("report = %+v, want the two unparseable owners held (the legacy row is not counted: it waits for its own review)", report)
	}
	if n := queryStrings(t, db, "SELECT COUNT(*) FROM collection")[0]; n != "0" {
		t.Errorf("collections = %s, want none", n)
	}
	review, err := ListCriteriaReview(ctx, db, "tenant1")
	if err != nil || len(review) != 2 {
		t.Fatalf("review = %+v %v", review, err)
	}
	for _, r := range review {
		if r.Problem == nil || r.ProposedQuery != nil {
			t.Errorf("review item = %+v, want a problem and no proposal", r)
		}
		if r.OwnerType == OwnerBlockItem && (r.OwnerName != "Man - item 1" || r.BlockID == nil || *r.BlockID != "man") {
			t.Errorf("item review = %+v", r)
		}
	}
	// The legacy row is untouched and has no review row from this migration.
	if got := queryStrings(t, db, "SELECT COALESCE(collection_id, 'none') FROM block WHERE id = 'legacy'")[0]; got != "none" {
		t.Errorf("legacy row got a collection: %s", got)
	}
	// Re-running re-evaluates the held rows without duplicating anything.
	report, err = MigrateToCollections(ctx, db)
	if err != nil || report.Held != 2 || report.Assigned != 0 {
		t.Fatalf("rerun = %+v %v", report, err)
	}
	if review, _ = ListCriteriaReview(ctx, db, "tenant1"); len(review) != 2 {
		t.Errorf("review rows after a rerun = %d", len(review))
	}
}

func TestMigrateToCollectionsKeepsTenantsApart(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 3, 0, 0)
	mustExec(t, db, "INSERT INTO tenant (id, name, tier, created_at) VALUES ('tenant2', 'T2', 'self_hosted', 1)")
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('other', 'tenant2', 'Other', ?, 1, 1)", treeGenre("Comedy"))
	upgrade()
	report, err := MigrateToCollections(ctx, db)
	if err != nil || report.Assigned != 4 || report.Created != 4 {
		t.Fatalf("report = %+v %v, want one collection per tenant and tree (4)", report, err)
	}
	a := queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'blk0000'")[0] // Comedy, tenant1
	b := queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'other'")[0]   // Comedy, tenant2
	if a == b {
		t.Fatal("a collection is shared across tenants")
	}
	if tenant := queryStrings(t, db, "SELECT tenant_id FROM collection WHERE id = ?", b)[0]; tenant != "tenant2" {
		t.Errorf("tenant of the second collection = %s", tenant)
	}
}

// countingDB counts the catalog evaluations a resolution makes.
type countingDB struct {
	storage.DB
	matches *int32
}

func (c countingDB) Query(ctx context.Context, dest any, q string, args ...any) error {
	if strings.HasPrefix(q, "SELECT id FROM catalog_item WHERE") {
		atomic.AddInt32(c.matches, 1)
	}
	return c.DB.Query(ctx, dest, q, args...)
}

func TestResolveDynamicEvaluatesEachCollectionOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	y := int64(2000)
	seedItem(t, db, "a", "movie", "A", "Comedy", &y)
	seedItem(t, db, "b", "movie", "B", "Horror", &y)
	if err := BackfillGenreLinks(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, c := range []struct{ id, q string }{{"cA", treeGenre("Comedy")}, {"cB", treeGenre("Horror")}} {
		q, _ := ParseQuery([]byte(c.q))
		mustExec(t, db, "INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", c.id, testTenantID, c.id, q.JSON(), now, now)
	}
	for i := 0; i < 5; i++ {
		mustExec(t, db, "INSERT INTO block (id, tenant_id, name, collection_id, created_at, updated_at) VALUES (?, ?, 'b', 'cA', ?, ?)", fmt.Sprintf("ba%d", i), testTenantID, now, now)
		mustExec(t, db, "INSERT INTO filler_pool (id, tenant_id, name, collection_id, created_at) VALUES (?, ?, 'p', 'cA', ?)", fmt.Sprintf("pa%d", i), testTenantID, now)
	}
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, collection_id, created_at, updated_at) VALUES ('bb', ?, 'b', 'cB', ?, ?)", testTenantID, now, now)

	var n int32
	if err := ResolveDynamic(ctx, countingDB{db, &n}, testTenantID); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("catalog evaluations = %d for 11 owners on 2 collections, want 2", n)
	}
	if ids := poolItemCatalogIDs(t, db, "pa3"); len(ids) != 1 || ids[0] != "a" {
		t.Errorf("pool items = %v", ids)
	}
	if ids := queryStrings(t, db, "SELECT catalog_item_id FROM block_item WHERE block_id = 'ba4'"); len(ids) != 1 || ids[0] != "a" {
		t.Errorf("block items = %v", ids)
	}

	// Editing a collection re-resolves only its own users, with one evaluation.
	mustExec(t, db, "UPDATE collection SET query = ? WHERE id = 'cA'", mustParse(t, treeGenre("Horror")))
	n = 0
	if err := ResolveCollection(ctx, countingDB{db, &n}, testTenantID, "cA"); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ResolveCollection evaluations = %d, want 1", n)
	}
	if ids := queryStrings(t, db, "SELECT catalog_item_id FROM block_item WHERE block_id = 'ba0'"); len(ids) != 1 || ids[0] != "b" {
		t.Errorf("block items after the edit = %v", ids)
	}
}

func mustParse(t *testing.T, q string) string {
	t.Helper()
	p, err := ParseQuery([]byte(q))
	if err != nil {
		t.Fatal(err)
	}
	return p.JSON()
}

func TestApplyReviewProposal(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 0, 0, 0)
	now := time.Now().UnixMilli()
	legacy := `{"kind":"movie","genres":["Comedy"]}`
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('lb', 'tenant1', 'Legacy block', ?, ?, ?)", legacy, now, now)
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('lb2', 'tenant1', 'Legacy block 2', ?, ?, ?)", legacy, now, now)
	mustExec(t, db, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('lbad', 'tenant1', 'Untranslatable', '{\"genres\":[\"x\"]}', ?, ?)", now, now)
	upgrade()
	// A genre whose name only partly matches makes the proposal differ.
	y := int64(2000)
	seedItem(t, db, "c", "movie", "C", "Comedy", &y)
	seedItem(t, db, "cr", "movie", "CR", "Comedy Romance", &y)
	if _, err := MigrateLegacyCriteria(ctx, db); err != nil {
		t.Fatal(err)
	}
	if r, _ := ListCriteriaReview(ctx, db, "tenant1"); len(r) != 3 {
		t.Fatalf("held = %d, want 3", len(r))
	}
	if _, err := MigrateToCollections(ctx, db); err != nil {
		t.Fatal(err)
	}

	id, created, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lb", nil)
	if err != nil || !created || id == "" {
		t.Fatalf("apply = %q %v %v", id, created, err)
	}
	if got := queryStrings(t, db, "SELECT collection_id FROM block WHERE id = 'lb'")[0]; got != id {
		t.Errorf("block collection = %s", got)
	}
	if got := queryStrings(t, db, "SELECT name FROM collection WHERE id = ?", id)[0]; got != "Legacy block" {
		t.Errorf("collection name = %s", got)
	}
	if got := queryStrings(t, db, "SELECT criteria FROM block WHERE id = 'lb'")[0]; got != legacy {
		t.Errorf("legacy snapshot was lost: %s", got)
	}
	// The same proposal for the second legacy block reuses the collection.
	id2, created2, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lb2", nil)
	if err != nil || created2 || id2 != id {
		t.Fatalf("second apply = %q %v %v, want the same collection", id2, created2, err)
	}
	// MigrateLegacyCriteria must leave an applied row (legacy snapshot kept) alone.
	before := queryStrings(t, db, "SELECT COUNT(*) FROM criteria_migration_review")[0]
	if _, err := MigrateLegacyCriteria(ctx, db); err != nil {
		t.Fatal(err)
	}
	review, _ := ListCriteriaReview(ctx, db, "tenant1")
	if len(review) != 1 || review[0].OwnerID != "lbad" {
		t.Fatalf("review = %+v (before: %s rows)", review, before)
	}

	// Not held any more, unknown, wrong type, and a translation that does not exist.
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lb", nil); err != ErrNotHeld {
		t.Errorf("reapply = %v", err)
	}
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "nope", nil); err != ErrNotHeld {
		t.Errorf("unknown = %v", err)
	}
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant2", OwnerBlock, "lbad", nil); err != ErrNotHeld {
		t.Errorf("other tenant = %v", err)
	}
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant1", "channel", "lbad", nil); err == nil {
		t.Error("bad owner type accepted")
	}
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lbad", nil); err == nil || !strings.Contains(err.Error(), "no proposed translation") {
		t.Errorf("untranslatable without a query = %v", err)
	}
	if _, _, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lbad", json.RawMessage(`{"version":1,"all":[]}`)); err == nil {
		t.Error("invalid override accepted")
	}
	id3, _, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlock, "lbad", json.RawMessage(treeGenre("Drama")))
	if err != nil || id3 == id {
		t.Errorf("override apply = %q %v (a different tree, so a different collection)", id3, err)
	}
	if review, _ = ListCriteriaReview(ctx, db, "tenant1"); len(review) != 0 {
		t.Errorf("review after everything applied = %+v", review)
	}
}

func TestApplyReviewProposalForACriteriaItem(t *testing.T) {
	db, upgrade := oldStateDB(t)
	ctx := context.Background()
	seedManyOwners(t, db, 0, 0, 1)
	upgrade()
	if err := BackfillGenreLinks(ctx, db); err != nil {
		t.Fatal(err)
	}
	collectionAfterHook = func(_ string, _ []string) []string { return nil }
	t.Cleanup(func() { collectionAfterHook = nil })
	if report, err := MigrateToCollections(ctx, db); err != nil || report.Held != 1 {
		t.Fatalf("report = %+v %v", report, err)
	}
	collectionAfterHook = nil
	id, created, err := ApplyReviewProposal(ctx, db, "tenant1", OwnerBlockItem, "it0000", nil)
	if err != nil || !created {
		t.Fatalf("apply = %q %v %v", id, created, err)
	}
	spec := queryStrings(t, db, "SELECT spec FROM block_item WHERE id = 'it0000'")[0]
	if !strings.Contains(spec, id) {
		t.Errorf("spec = %s", spec)
	}
	if r, _ := ListCriteriaReview(ctx, db, "tenant1"); len(r) != 0 {
		t.Errorf("review = %+v", r)
	}
	u, err := CollectionUsage(ctx, db, "tenant1", id)
	if err != nil || len(u.BlockItems) != 1 || u.BlockItems[0].ID != "it0000" || u.BlockItems[0].BlockName != "Manual 0000" {
		t.Errorf("usage = %+v %v", u, err)
	}
	// Another tenant sees none of it.
	if u, _ := CollectionUsage(ctx, db, "tenant2", id); u.Count() != 0 {
		t.Errorf("foreign usage = %+v", u)
	}
	if _, err := GetCollection(ctx, db, "tenant2", id); err != ErrCollectionNotFound {
		t.Errorf("foreign get = %v", err)
	}
}
