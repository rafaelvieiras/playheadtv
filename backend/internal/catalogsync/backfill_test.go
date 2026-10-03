package catalogsync

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedUnbackfilled inserts n catalog items as an older version would have
// left them: genres and overview present, but no search_text, overview_norm
// or genre links. One set-based statement, so seeding is not the bottleneck.
// The accented overview is deliberate test data for the folding.
func seedUnbackfilled(t testing.TB, db storage.DB, n int) {
	t.Helper()
	err := db.Exec(context.Background(),
		`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		 INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, genres, overview, series_title)
		 SELECT 'bf-' || printf('%07d', i), ?, 'ms1', 'ext-' || i, 'movie', 'Título ' || i, 1000, 0,
		        CASE i % 3 WHEN 0 THEN 'Comedy,Drama' WHEN 1 THEN 'Action' ELSE '' END,
		        CASE i % 4 WHEN 0 THEN NULL ELSE 'Descrição ' || i END,
		        NULL
		   FROM seq`, n, testTenantID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func countWhere(t testing.TB, db storage.DB, query string) int {
	t.Helper()
	var out []int
	if err := db.Query(context.Background(), &out, query); err != nil || len(out) != 1 {
		t.Fatalf("%s: %v %v", query, out, err)
	}
	return out[0]
}

func runAllBackfills(ctx context.Context, db storage.DB) error {
	if err := BackfillSearchText(ctx, db); err != nil {
		return err
	}
	if err := BackfillOverviewNorm(ctx, db); err != nil {
		return err
	}
	return BackfillGenreLinks(ctx, db)
}

func TestBackfillsAreBatchedResumableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	n := BackfillBatchSize*2 + 345 // spans several transactions
	seedUnbackfilled(t, db, n)

	// Pre-fill one part of the rows (as an interrupted earlier run would have
	// left them): the backfill must only touch the rest.
	if err := db.Exec(ctx, "UPDATE catalog_item SET search_text = 'kept' WHERE id <= 'bf-0000500'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second pass is a no-op
		if err := runAllBackfills(ctx, db); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE search_text = ''"); got != 0 {
		t.Errorf("%d rows still without search_text", got)
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE search_text = 'kept'"); got != 500 {
		t.Errorf("pre-filled rows were rewritten: %d kept, want 500", got)
	}
	var st []string
	if err := db.Query(ctx, &st, "SELECT search_text FROM catalog_item WHERE id = 'bf-0000501'"); err != nil || len(st) != 1 || st[0] != "titulo 501\n" {
		t.Errorf("search_text = %q (%v)", st, err)
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE overview IS NOT NULL AND overview_norm IS NULL"); got != 0 {
		t.Errorf("%d rows still without overview_norm", got)
	}
	var on []string
	if err := db.Query(ctx, &on, "SELECT overview_norm FROM catalog_item WHERE id = 'bf-0000001'"); err != nil || len(on) != 1 || on[0] != "descricao 1" {
		t.Errorf("overview_norm = %q (%v)", on, err)
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE overview IS NULL AND overview_norm IS NOT NULL"); got != 0 {
		t.Errorf("overview_norm filled for %d rows with no overview", got)
	}
	// i%3==1 -> 1 genre, i%3==0 -> 2 genres, i%3==2 -> none.
	var wantLinks, noGenre int
	for i := 1; i <= n; i++ {
		switch i % 3 {
		case 0:
			wantLinks += 2
		case 1:
			wantLinks++
		default:
			noGenre++
		}
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item_genre"); got != wantLinks {
		t.Errorf("%d genre links, want %d", got, wantLinks)
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item c WHERE NOT EXISTS (SELECT 1 FROM catalog_item_genre g WHERE g.catalog_item_id = c.id)"); got != noGenre {
		t.Errorf("%d items without links, want %d (the ones with no genres)", got, noGenre)
	}
}

func TestBackfillStopsOnCancelledContextAndResumes(t *testing.T) {
	db := newTestDB(t)
	seedUnbackfilled(t, db, BackfillBatchSize+10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := BackfillSearchText(ctx, db); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if err := BackfillSearchText(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE search_text = ''"); got != 0 {
		t.Errorf("%d rows left after resume", got)
	}
}

func TestTransactRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedUnbackfilled(t, db, 3)
	err := db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx, "UPDATE catalog_item SET search_text = 'x'"); err != nil {
			return err
		}
		return fmt.Errorf("boom")
	})
	if err == nil {
		t.Fatal("expected the fn error")
	}
	if got := countWhere(t, db, "SELECT COUNT(*) FROM catalog_item WHERE search_text = 'x'"); got != 0 {
		t.Errorf("rollback failed: %d rows changed", got)
	}
}

// TestBackfillScale is a manual measurement, not part of the normal suite:
// PLAYHEADTV_BACKFILL_ROWS=50000 go test -run TestBackfillScale -v ./internal/catalogsync
func TestBackfillScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("PLAYHEADTV_BACKFILL_ROWS"))
	if n == 0 {
		t.Skip("set PLAYHEADTV_BACKFILL_ROWS to run the scale measurement")
	}
	ctx := context.Background()
	db := newTestDB(t)
	seedUnbackfilled(t, db, n)
	for name, fn := range map[string]func(context.Context, storage.DB) error{
		"search_text": BackfillSearchText, "overview_norm": BackfillOverviewNorm, "genre links": BackfillGenreLinks,
	} {
		start := time.Now()
		if err := fn(ctx, db); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start)
		t.Logf("MEASURE %s: %d rows in %s (%.0f rows/s)", name, n, d.Round(time.Millisecond), float64(n)/d.Seconds())
	}
}
