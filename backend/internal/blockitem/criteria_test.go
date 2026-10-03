package blockitem

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

const tenant = "default"

// testDB opens a migrated temp database with the default tenant and a second
// one, plus a collection of each (ids "col" and "foreign").
func testDB(t *testing.T) storage.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('default', 'D', 'self_hosted', 1), ('other', 'O', 'self_hosted', 1)",
		"INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('col', 'default', 'Mine', '" + testQuery + "', 1, 1)",
		"INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('col2', 'default', 'Mine 2', '{\"version\":1,\"all\":[{\"field\":\"year\",\"op\":\"gt\",\"value\":2000}]}', 1, 1)",
		"INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('foreign', 'other', 'Theirs', '" + testQuery + "', 1, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func intp(n int) *int { return &n }

const testQuery = `{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]}`

func TestNewCriteriaAmount(t *testing.T) {
	ok := map[string]struct {
		in   *domain.SeriesAmountInput
		want domain.CriteriaAmount
	}{
		"count":              {&domain.SeriesAmountInput{Mode: "count", Count: intp(3)}, domain.CriteriaAmount{Mode: "count", Count: 3}},
		"count without mode": {&domain.SeriesAmountInput{Count: intp(2)}, domain.CriteriaAmount{Mode: "count", Count: 2}},
		"all":                {&domain.SeriesAmountInput{Mode: "all"}, domain.CriteriaAmount{Mode: "all"}},
	}
	for name, tc := range ok {
		got, err := NewCriteriaAmount(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, err, tc.want)
		}
	}
	bad := map[string]*domain.SeriesAmountInput{
		"nil":             nil,
		"empty":           {},
		"count no number": {Mode: "count"},
		"zero":            {Mode: "count", Count: intp(0)},
		"all with count":  {Mode: "all", Count: intp(1)},
		"series mode":     {Mode: "slot_end"},
		"series end":      {Mode: "series_end"},
	}
	for name, in := range bad {
		if _, err := NewCriteriaAmount(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewCriteriaSpecDefaultsAndValidation(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	spec, err := NewCriteriaSpec(ctx, db, tenant, "col", "", &domain.SeriesAmountInput{Mode: "all"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.PlaybackOrder != domain.OrderTitle || spec.Seed <= 0 || spec.Amount.Mode != "all" || spec.CollectionID != "col" || len(spec.Query) != 0 {
		t.Errorf("spec = %+v", spec)
	}
	other, _ := NewCriteriaSpec(ctx, db, tenant, "col", "shuffle", &domain.SeriesAmountInput{Mode: "all"}, nil)
	if other.Seed == spec.Seed {
		t.Error("two specs got the same seed")
	}
	for _, order := range []string{"original", "nope"} {
		if _, err := NewCriteriaSpec(ctx, db, tenant, "col", order, &domain.SeriesAmountInput{Mode: "all"}, nil); err == nil {
			t.Errorf("order %q accepted", order)
		}
	}
	for _, id := range []string{"", "nope", "foreign"} {
		if _, err := NewCriteriaSpec(ctx, db, tenant, id, "", &domain.SeriesAmountInput{Mode: "all"}, nil); err == nil {
			t.Errorf("collection %q accepted", id)
		}
	}
}

func TestResolveSpecUsesTheCollectionAndOnlyFallsBackWithoutOne(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	got, err := ResolveSpec(ctx, db, tenant, domain.CriteriaSpec{CollectionID: "col", Query: []byte(`{"stale":true}`)})
	if err != nil || string(got.Query) != testQuery {
		t.Errorf("collection wins over the deprecated embedded query: %s %v", got.Query, err)
	}
	got, err = ResolveSpec(ctx, db, tenant, domain.CriteriaSpec{Query: []byte(testQuery)})
	if err != nil || string(got.Query) != testQuery {
		t.Errorf("held item keeps its embedded query: %s %v", got.Query, err)
	}
	if _, err := ResolveSpec(ctx, db, tenant, domain.CriteriaSpec{CollectionID: "foreign"}); err == nil {
		t.Error("a collection of another tenant resolved")
	}
}

func TestUpdateCriteriaSpec(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	spec, _ := NewCriteriaSpec(ctx, db, tenant, "col", "title", &domain.SeriesAmountInput{Mode: "count", Count: intp(1)}, nil)
	seed := spec.Seed
	order := "title"
	got, changed, err := UpdateCriteriaSpec(ctx, db, tenant, spec, nil, &order, nil, nil)
	if err != nil || changed || got.Seed != seed {
		t.Errorf("same order: %+v changed=%v %v", got, changed, err)
	}
	order = "random"
	col2 := "col2"
	got, changed, err = UpdateCriteriaSpec(ctx, db, tenant, spec, &col2, &order, &domain.SeriesAmountInput{Mode: "all"}, nil)
	if err != nil || !changed || got.PlaybackOrder != "random" || got.Amount.Mode != "all" || got.Seed != seed || got.CollectionID != "col2" {
		t.Errorf("update: %+v changed=%v %v", got, changed, err)
	}
	bad := "original"
	if _, _, err := UpdateCriteriaSpec(ctx, db, tenant, spec, nil, &bad, nil, nil); err == nil {
		t.Error("series order accepted")
	}
	for _, id := range []string{"", "nope", "foreign"} {
		id := id
		if _, _, err := UpdateCriteriaSpec(ctx, db, tenant, spec, &id, nil, nil, nil); err == nil {
			t.Errorf("collection %q accepted", id)
		}
	}
}

func TestInterleaveSpecAndBlockPlayback(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	all := &domain.SeriesAmountInput{Mode: "all"}
	spec, err := NewCriteriaSpec(ctx, db, tenant, "col", "interleaved", all, nil)
	if err != nil || spec.Interleave == nil || *spec.Interleave != (domain.Interleave{PerRound: 1, OnExhaustion: "restart", SeriesOrder: "title"}) {
		t.Fatalf("defaults: %+v %v", spec.Interleave, err)
	}
	if _, err := NewCriteriaSpec(ctx, db, tenant, "col", "title", all, &domain.InterleaveInput{PerRound: intp(2)}); err == nil {
		t.Error("interleave accepted with the title order")
	}
	for _, in := range []domain.InterleaveInput{{PerRound: intp(0)}, {OnExhaustion: "x"}, {SeriesOrder: "interleaved"}, {SeriesOrder: "original"}} {
		in := in
		if _, err := NewCriteriaSpec(ctx, db, tenant, "col", "interleaved", all, &in); err == nil {
			t.Errorf("%+v accepted", in)
		}
	}
	// Only on_exhaustion changing keeps the cursor.
	order := "interleaved"
	got, restart, err := UpdateCriteriaSpec(ctx, db, tenant, spec, nil, &order, nil, &domain.InterleaveInput{OnExhaustion: "drop"})
	if err != nil || restart || got.Interleave.OnExhaustion != "drop" {
		t.Errorf("on_exhaustion: %+v restart=%v %v", got.Interleave, restart, err)
	}
	if _, restart, _ = UpdateCriteriaSpec(ctx, db, tenant, spec, nil, nil, nil, &domain.InterleaveInput{PerRound: intp(2)}); !restart {
		t.Error("per_round change did not restart")
	}

	bp, restart, err := NewBlockPlayback(true, "", nil, nil)
	if err != nil || bp.Order != "title" || bp.InterleaveSpec != nil || restart {
		t.Errorf("default block playback: %+v restart=%v %v", bp, restart, err)
	}
	if _, _, err := NewBlockPlayback(false, "interleaved", nil, nil); err == nil {
		t.Error("interleaved accepted for a manual block")
	}
	bp, restart, err = NewBlockPlayback(true, "interleaved", nil, nil)
	if err != nil || bp.Order != "interleaved" || bp.InterleaveSpec == nil || bp.Seed == 0 || !restart {
		t.Errorf("interleaved block playback: %+v restart=%v %v", bp, restart, err)
	}
}
