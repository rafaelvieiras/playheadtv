package catalogsync

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func i64(n int64) *int64     { return &n }
func str(s string) *string   { return &s }
func f64(f float64) *float64 { return &f }

func fullItem() Item {
	return Item{
		ExternalID: "ep-1", Kind: domain.CatalogEpisode, Title: "Pilot", SeriesTitle: str("Some Show"),
		DurationMs: 1_300_000, Genres: "Comedy,Drama",
		CatalogMetadata: domain.CatalogMetadata{
			SeasonNumber: i64(1), EpisodeNumber: i64(3), EpisodeNumberEnd: i64(4),
			SeriesExternalID: str("series-1"), SeasonName: str("Season 1"),
			Overview: str("A pilot."), ContentRating: str("TV-14"), CommunityRating: f64(8.5),
			ImdbID: str("tt123"), Width: i64(1920), Height: i64(1080), VideoCodec: str("h264"),
			ImagePrimaryTag: str("tagP"), SeriesImagePrimaryTag: str("tagS"),
			Studios: []string{"Acme Studio"}, Tags: []string{"cult"},
			AudioLanguages: []string{"eng", "por"}, SubtitleLanguages: []string{"eng"},
			Directors: []domain.CatalogPerson{{Name: "Dana Director"}},
			Cast:      []domain.CatalogPerson{{Name: "Ana Actor", Role: "Hero"}, {Name: "Bob Actor", Role: "Villain"}},
			Locations: []string{"Brazil"},
			Chapters:  []domain.CatalogChapter{{Name: "Intro", StartMs: 0}, {Name: "Act 1", StartMs: 60_000}},
		},
	}
}

func loadOne(t *testing.T, db storage.DB, extID string) domain.CatalogItem {
	t.Helper()
	var rows []domain.CatalogItem
	if err := db.Query(context.Background(), &rows,
		"SELECT "+DetailColumns+" FROM catalog_item WHERE tenant_id = ? AND external_id = ?", testTenantID, extID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows for %s = %d, want 1", extID, len(rows))
	}
	if err := LoadRelations(context.Background(), db, testTenantID, rows, true); err != nil {
		t.Fatalf("LoadRelations: %v", err)
	}
	return rows[0]
}

func count(t *testing.T, db storage.DB, table string) int {
	t.Helper()
	var n []int
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n[0]
}

func TestUpsertStoresAndLoadsMetadata(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := Upsert(ctx, db, testTenantID, "ms1", fullItem(), time.Now().UnixMilli()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got := loadOne(t, db, "ep-1")
	if got.Genres != "Comedy,Drama" {
		t.Errorf("genres column = %q, want unchanged comma string", got.Genres)
	}
	if got.SeasonNumber == nil || *got.SeasonNumber != 1 || *got.EpisodeNumber != 3 || *got.EpisodeNumberEnd != 4 {
		t.Errorf("numbers = %v %v %v", got.SeasonNumber, got.EpisodeNumber, got.EpisodeNumberEnd)
	}
	if got.CommunityRating == nil || *got.CommunityRating != 8.5 || got.CriticRating != nil {
		t.Errorf("ratings = %v %v", got.CommunityRating, got.CriticRating)
	}
	if got.SeriesImagePrimaryTag == nil || *got.SeriesImagePrimaryTag != "tagS" {
		t.Errorf("series image tag = %v", got.SeriesImagePrimaryTag)
	}
	want := map[string]any{
		"studios": []string{"Acme Studio"}, "tags": []string{"cult"},
		"audio": []string{"eng", "por"}, "subs": []string{"eng"}, "locs": []string{"Brazil"},
	}
	have := map[string]any{
		"studios": got.Studios, "tags": got.Tags,
		"audio": got.AudioLanguages, "subs": got.SubtitleLanguages, "locs": got.Locations,
	}
	if !reflect.DeepEqual(want, have) {
		t.Errorf("lists = %v, want %v", have, want)
	}
	if len(got.Directors) != 1 || got.Directors[0].Name != "Dana Director" || got.Directors[0].Type != "Director" {
		t.Errorf("directors = %+v", got.Directors)
	}
	if len(got.Cast) != 2 || got.Cast[0].Name != "Ana Actor" || got.Cast[0].Role != "Hero" || got.Cast[1].Name != "Bob Actor" {
		t.Errorf("cast = %+v (want billing order kept)", got.Cast)
	}
	if len(got.Chapters) != 2 || got.Chapters[1].StartMs != 60_000 {
		t.Errorf("chapters = %+v", got.Chapters)
	}
	// The genre dimension is filled in parallel from the comma string.
	if n := count(t, db, "catalog_item_genre"); n != 2 {
		t.Errorf("catalog_item_genre rows = %d, want 2", n)
	}
}

func TestUpsertResyncIsIdempotentAndRemovesStaleLinks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := Upsert(ctx, db, testTenantID, "ms1", fullItem(), int64(i)); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}
	tables := []string{"person", "studio", "tag", "genre", "language", "catalog_item_person", "catalog_item_studio",
		"catalog_item_tag", "catalog_item_genre", "catalog_item_language", "catalog_item"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = count(t, db, tb)
	}
	if before["catalog_item"] != 1 || before["catalog_item_person"] != 3 || before["catalog_item_language"] != 3 || before["language"] != 2 {
		t.Fatalf("unexpected counts after 3 identical upserts: %v", before)
	}

	// Change: drop Bob and the tag, swap the studio, reorder, change a role,
	// drop one subtitle language and a genre.
	changed := fullItem()
	changed.Cast = []domain.CatalogPerson{{Name: "Ana Actor", Role: "Anti-hero"}}
	changed.Tags = nil
	changed.Studios = []string{"Other Studio"}
	changed.SubtitleLanguages = nil
	changed.Genres = "Comedy"
	changed.Chapters = nil
	if err := Upsert(ctx, db, testTenantID, "ms1", changed, 10); err != nil {
		t.Fatalf("Upsert changed: %v", err)
	}
	got := loadOne(t, db, "ep-1")
	if len(got.Cast) != 1 || got.Cast[0].Role != "Anti-hero" {
		t.Errorf("cast = %+v, want only Ana with the new role", got.Cast)
	}
	if len(got.Tags) != 0 || len(got.SubtitleLanguages) != 0 || len(got.Chapters) != 0 {
		t.Errorf("stale data kept: tags=%v subs=%v chapters=%v", got.Tags, got.SubtitleLanguages, got.Chapters)
	}
	if !reflect.DeepEqual(got.Studios, []string{"Other Studio"}) {
		t.Errorf("studios = %v", got.Studios)
	}
	if n := count(t, db, "catalog_item_genre"); n != 1 {
		t.Errorf("catalog_item_genre rows = %d, want 1", n)
	}
	if n := count(t, db, "catalog_item"); n != 1 {
		t.Errorf("catalog_item rows = %d, want 1 (upsert, not insert)", n)
	}
}

func TestUpsertPeopleCapDirectorsFirst(t *testing.T) {
	db := newTestDB(t)
	item := Item{ExternalID: "m1", Kind: domain.CatalogMovie, Title: "Big Cast", DurationMs: 1000}
	item.Directors = []domain.CatalogPerson{{Name: "Dir One"}, {Name: "Dir Two"}}
	for i := 0; i < 30; i++ {
		item.Cast = append(item.Cast, domain.CatalogPerson{Name: fmt.Sprintf("Actor %02d", i), Role: "R"})
	}
	if err := Upsert(context.Background(), db, testTenantID, "ms1", item, 1); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got := loadOne(t, db, "m1")
	if len(got.Directors) != 2 {
		t.Errorf("directors = %d, want 2 (directors are kept first)", len(got.Directors))
	}
	if len(got.Cast) != MaxPeople-2 || got.Cast[0].Name != "Actor 00" || got.Cast[MaxPeople-3].Name != fmt.Sprintf("Actor %02d", MaxPeople-3) {
		t.Errorf("cast = %d entries (first %v), want the first %d in billing order", len(got.Cast), got.Cast[0], MaxPeople-2)
	}
	if n := count(t, db, "catalog_item_person"); n != MaxPeople {
		t.Errorf("person links = %d, want %d", n, MaxPeople)
	}
}

func TestDimensionsAreFoldedAndPerTenant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES ('t2', 'Other', 'self_hosted', 1)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, `INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		VALUES ('ms2', 't2', 'local_folder', 'x', 'push', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	// "José" and "JOSE" differ only by accent/case, deliberate test data for the
	// folded dimension key.
	a := Item{ExternalID: "a", Kind: domain.CatalogMovie, Title: "A", DurationMs: 1}
	a.Cast = []domain.CatalogPerson{{Name: "José"}}
	b := Item{ExternalID: "b", Kind: domain.CatalogMovie, Title: "B", DurationMs: 1}
	b.Cast = []domain.CatalogPerson{{Name: "JOSE"}}
	for _, it := range []Item{a, b} {
		if err := Upsert(ctx, db, testTenantID, "ms1", it, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := Upsert(ctx, db, "t2", "ms2", a, 1); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "person"); n != 2 {
		t.Fatalf("person rows = %d, want 2 (one per tenant, folded within a tenant)", n)
	}
	if got := loadOne(t, db, "b"); got.Cast[0].Name != "José" {
		t.Errorf("display name = %q, want first spelling seen", got.Cast[0].Name)
	}
}

func TestDeleteCascadesLinks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := Upsert(ctx, db, testTenantID, "ms1", fullItem(), 1); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, db, "ms1", "ep-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, tb := range []string{"catalog_item_person", "catalog_item_studio", "catalog_item_tag", "catalog_item_genre", "catalog_item_language"} {
		if n := count(t, db, tb); n != 0 {
			t.Errorf("%s rows after Delete = %d, want 0", tb, n)
		}
	}
}
