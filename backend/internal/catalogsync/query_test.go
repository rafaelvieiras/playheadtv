package catalogsync

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func mustQuery(t *testing.T, raw string) *Query {
	t.Helper()
	q, err := ParseQuery([]byte(raw))
	if err != nil {
		t.Fatalf("ParseQuery(%s): %v", raw, err)
	}
	return q
}

func day(s string) *int64 {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return i64(d.UnixMilli())
}

// seedQueryCatalog upserts a small catalog through the real Upsert, so the
// link and dimension tables are filled exactly like a sync fills them.
func seedQueryCatalog(t *testing.T, db storage.DB) {
	t.Helper()
	ctx := context.Background()
	type seed struct {
		ext   string
		kind  domain.CatalogItemKind
		title string
		ser   *string
		mins  int64
		genre string
		year  *int64
		meta  domain.CatalogMetadata
	}
	seeds := []seed{
		{"alien", domain.CatalogMovie, "Alien", nil, 117, "Horror,Sci-Fi", i64(1979), domain.CatalogMetadata{
			CommunityRating: f64(8.4), CriticRating: f64(93), ContentRating: str("R"), PremiereDate: day("1979-05-25"), DateAdded: day("2024-01-10"),
			// Accented text below is deliberate test data for the accent-insensitive matching.
			Overview: str("In space no one can hear you scream. Café scene."), Studios: []string{"20th Century Fox"}, Tags: []string{"space", "Classic"},
			AudioLanguages: []string{"eng"}, SubtitleLanguages: []string{"por"},
			Directors: []domain.CatalogPerson{{Name: "Ridley Scott"}}, Cast: []domain.CatalogPerson{{Name: "Sigourney Weaver", Role: "Ripley"}}}},
		// Accented text below is deliberate test data for the accent-insensitive matching.
		{"amelie", domain.CatalogMovie, "Amélie", nil, 122, "Comedy,Romance", i64(2001), domain.CatalogMetadata{
			CommunityRating: f64(8.3), ContentRating: str("R"), PremiereDate: day("2001-04-25"), DateAdded: day("2024-02-01"),
			Studios: []string{"UGC"}, AudioLanguages: []string{"fra"}, SubtitleLanguages: []string{"eng", "por"}}},
		{"airplane", domain.CatalogMovie, "Airplane!", nil, 87, "Comedy", i64(1980), domain.CatalogMetadata{
			CommunityRating: f64(7.7), ContentRating: str("PG"), PremiereDate: day("1980-07-02")}},
		{"nameless", domain.CatalogMovie, "Nameless", nil, 95, "Drama", nil, domain.CatalogMetadata{}},
		{"zombie", domain.CatalogMovie, "Zombie Shorts", nil, 80, "Horror", i64(1983), domain.CatalogMetadata{}},
		{"comhorror", domain.CatalogMovie, "Comedy Horror Short", nil, 85, "Horror,Comedy", i64(1984), domain.CatalogMetadata{}},
		{"longhorror", domain.CatalogMovie, "Long Horror", nil, 120, "Horror", i64(1985), domain.CatalogMetadata{}},
		{"pct", domain.CatalogMovie, "100% Wolf", nil, 90, "Animation", i64(2020), domain.CatalogMetadata{}},
		{"under", domain.CatalogMovie, "Under_score", nil, 90, "Animation", i64(2020), domain.CatalogMetadata{}},
		{"pilot", domain.CatalogEpisode, "Pilot", str("Show"), 22, "Comedy", i64(2010), domain.CatalogMetadata{
			SeasonNumber: i64(1), EpisodeNumber: i64(1), PremiereDate: day("2010-09-20")}},
		{"special", domain.CatalogEpisode, "Special", str("Show"), 30, "Comedy", i64(2011), domain.CatalogMetadata{
			SeasonNumber: i64(0), EpisodeNumber: i64(1), PremiereDate: day("2011-12-24")}},
		{"comedia", domain.CatalogMovie, "Comédia Brasileira", nil, 100, "Comédia", i64(2015), domain.CatalogMetadata{}},
	}
	now := time.Now().UnixMilli()
	for _, s := range seeds {
		item := Item{ExternalID: s.ext, Kind: s.kind, Title: s.title, SeriesTitle: s.ser, DurationMs: s.mins * 60000, Genres: s.genre, ProductionYear: s.year, CatalogMetadata: s.meta}
		if err := Upsert(ctx, db, testTenantID, "ms1", item, now); err != nil {
			t.Fatalf("Upsert %s: %v", s.ext, err)
		}
	}
}

// matchExt runs q and returns the external ids of the matches, sorted.
func matchExt(t *testing.T, db storage.DB, raw string) []string {
	t.Helper()
	q := mustQuery(t, raw)
	ids, err := MatchQuery(context.Background(), db, testTenantID, q)
	if err != nil {
		t.Fatalf("MatchQuery(%s): %v", raw, err)
	}
	var exts []string
	for _, id := range ids {
		var e []string
		if err := db.Query(context.Background(), &e, "SELECT external_id FROM catalog_item WHERE id = ?", id); err != nil {
			t.Fatal(err)
		}
		exts = append(exts, e[0])
	}
	sort.Strings(exts)
	return exts
}

func cond(field, op, value string) string {
	return `{"field":"` + field + `","op":"` + op + `","value":` + value + `}`
}

func tree(node string) string { return `{"version":1,` + strings.TrimPrefix(node, "{") }

var allExt = []string{"airplane", "alien", "amelie", "comedia", "comhorror", "longhorror", "nameless", "pct", "pilot", "special", "under", "zombie"}

func without(list []string, drop ...string) []string {
	var out []string
	for _, v := range list {
		keep := true
		for _, d := range drop {
			if v == d {
				keep = false
			}
		}
		if keep {
			out = append(out, v)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestQueryOperators(t *testing.T) {
	db := newTestDB(t)
	seedQueryCatalog(t, db)

	tests := []struct {
		name string
		q    string
		want []string
	}{
		// Curated list fields: exact on the normalized name, never a substring.
		{"genre equals is accent and case insensitive", tree(cond("genre", "equals", `"COMEDIA"`)), []string{"comedia"}},
		{"genre equals is exact, not a substring", tree(cond("genre", "equals", `"Come"`)), nil},
		{"genre equals", tree(cond("genre", "equals", `"romance"`)), []string{"amelie"}},
		{"genre in is an OR", tree(cond("genre", "in", `["Drama","Romance"]`)), []string{"amelie", "nameless"}},
		{"studio equals", tree(cond("studio", "equals", `"20th century fox"`)), []string{"alien"}},
		{"tag in", tree(cond("tag", "in", `["classic","nope"]`)), []string{"alien"}},
		{"audio language", tree(cond("audio_language", "equals", `"fra"`)), []string{"amelie"}},
		{"audio language ignores subtitle links", tree(cond("audio_language", "equals", `"por"`)), nil},
		{"subtitle language", tree(cond("subtitle_language", "equals", `"por"`)), []string{"alien", "amelie"}},
		// Free text: contains, accent and case insensitive, like catalog search.
		{"title contains folds accents", tree(cond("title", "contains", `"amelie"`)), []string{"amelie"}},
		{"title contains folds case", tree(cond("title", "contains", `"ALIEN"`)), []string{"alien"}},
		{"title contains also reads the series title", tree(cond("title", "contains", `"show"`)), []string{"pilot", "special"}},
		{"title percent is literal", tree(cond("title", "contains", `"100%"`)), []string{"pct"}},
		{"title percent alone is not a wildcard", tree(cond("title", "contains", `"%"`)), []string{"pct"}},
		{"title underscore is literal", tree(cond("title", "contains", `"r_s"`)), []string{"under"}},
		{"title underscore is not a wildcard", tree(cond("title", "contains", `"unde_"`)), nil},
		{"overview contains folds accents", tree(cond("overview", "contains", `"cafe"`)), []string{"alien"}},
		{"person contains matches director", tree(cond("person", "contains", `"RIDLEY"`)), []string{"alien"}},
		{"person contains matches cast", tree(cond("person", "contains", `"weaver"`)), []string{"alien"}},
		{"actor contains matches cast", tree(cond("actor", "contains", `"WEAVER"`)), []string{"alien"}},
		{"actor contains does not match a director", tree(cond("actor", "contains", `"ridley"`)), nil},
		{"director contains matches director", tree(cond("director", "contains", `"Ridley"`)), []string{"alien"}},
		{"director contains does not match cast", tree(cond("director", "contains", `"weaver"`)), nil},
		// Enumerations: exact.
		{"kind equals episode", tree(cond("kind", "equals", `"episode"`)), []string{"pilot", "special"}},
		{"kind in", tree(cond("kind", "in", `["episode","movie"]`)), allExt},
		// A whole source at once: how a channel is built from a plugin-backed one.
		{"media source equals", tree(cond("media_source_id", "equals", `"ms1"`)), allExt},
		{"media source is exact", tree(cond("media_source_id", "equals", `"ms2"`)), nil},
		{"content rating equals", tree(cond("content_rating", "equals", `"PG"`)), []string{"airplane"}},
		{"content rating is exact", tree(cond("content_rating", "equals", `"P"`)), nil},
		{"content rating in", tree(cond("content_rating", "in", `["PG","R"]`)), []string{"airplane", "alien", "amelie"}},
		// Numbers.
		{"year equals", tree(cond("year", "equals", `1980`)), []string{"airplane"}},
		{"year lt", tree(cond("year", "lt", `1980`)), []string{"alien"}},
		{"year lte", tree(cond("year", "lte", `1980`)), []string{"airplane", "alien"}},
		{"year gt", tree(cond("year", "gt", `2015`)), []string{"pct", "under"}},
		{"year gte", tree(cond("year", "gte", `2015`)), []string{"comedia", "pct", "under"}},
		{"year between is inclusive", tree(cond("year", "between", `[1983,1984]`)), []string{"comhorror", "zombie"}},
		{"a missing year never satisfies a comparison", tree(cond("year", "lt", `3000`)), without(allExt, "nameless")},
		{"duration is in minutes", tree(cond("duration", "lt", `90`)), []string{"airplane", "comhorror", "pilot", "special", "zombie"}},
		{"duration lte", tree(cond("duration", "lte", `87`)), []string{"airplane", "pilot", "special", "comhorror", "zombie"}},
		{"duration equals", tree(cond("duration", "equals", `117`)), []string{"alien"}},
		{"community rating gte", tree(cond("community_rating", "gte", `8.3`)), []string{"alien", "amelie"}},
		{"critic rating", tree(cond("critic_rating", "gt", `90`)), []string{"alien"}},
		{"season zero is the specials", tree(cond("season_number", "equals", `0`)), []string{"special"}},
		{"episode number between", tree(cond("episode_number", "between", `[1,1]`)), []string{"pilot", "special"}},
		// Dates: a date is a whole UTC day.
		{"premiere equals the day", tree(cond("premiere_date", "equals", `"1979-05-25"`)), []string{"alien"}},
		{"premiere lt excludes the day", tree(cond("premiere_date", "lt", `"1979-05-25"`)), nil},
		{"premiere lte includes the day", tree(cond("premiere_date", "lte", `"1979-05-25"`)), []string{"alien"}},
		{"premiere gt excludes the day", tree(cond("premiere_date", "gt", `"2001-04-25"`)), []string{"pilot", "special"}},
		{"premiere gte includes the day", tree(cond("premiere_date", "gte", `"2001-04-25"`)), []string{"amelie", "pilot", "special"}},
		{"premiere between", tree(cond("premiere_date", "between", `["1979-01-01","1980-12-31"]`)), []string{"airplane", "alien"}},
		{"date added", tree(cond("date_added", "gte", `"2024-02-01"`)), []string{"amelie"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			got := matchExt(t, db, tc.q)
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestQueryComposition(t *testing.T) {
	db := newTestDB(t)
	seedQueryCatalog(t, db)

	// "movies from the 80s, horror, shorter than 90 minutes and not comedy"
	q := `{"version":1,"all":[` +
		cond("kind", "equals", `"movie"`) + `,` +
		cond("year", "between", `[1980,1989]`) + `,` +
		cond("genre", "equals", `"Horror"`) + `,` +
		cond("duration", "lt", `90`) + `,` +
		`{"not":` + cond("genre", "equals", `"Comedy"`) + `}]}`
	if got, want := matchExt(t, db, q), []string{"zombie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("80s horror query = %v, want %v", got, want)
	}

	// any: either branch.
	anyQ := `{"version":1,"any":[` + cond("studio", "equals", `"UGC"`) + `,` + cond("tag", "equals", `"space"`) + `]}`
	if got, want := matchExt(t, db, anyQ), []string{"alien", "amelie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("any = %v, want %v", got, want)
	}

	// nested all/any/not: (comedy or horror) and not episode and year < 1990.
	nested := `{"version":1,"all":[` +
		`{"any":[` + cond("genre", "equals", `"Comedy"`) + `,` + cond("genre", "equals", `"Horror"`) + `]},` +
		`{"not":` + cond("kind", "equals", `"episode"`) + `},` +
		cond("year", "lt", `1990`) + `]}`
	want := []string{"airplane", "alien", "comhorror", "longhorror", "zombie"}
	if got := matchExt(t, db, nested); !reflect.DeepEqual(got, want) {
		t.Errorf("nested = %v, want %v", got, want)
	}

	// not of a group; nameless has no year, so "year >= 1980" is false for it
	// and the negation holds.
	notAny := `{"version":1,"not":{"any":[` + cond("kind", "equals", `"episode"`) + `,` + cond("year", "gte", `1980`) + `]}}`
	if got, want := matchExt(t, db, notAny), []string{"alien", "nameless"}; !reflect.DeepEqual(got, want) {
		t.Errorf("not(any) = %v, want %v", got, want)
	}
	double := `{"version":1,"not":{"not":` + cond("genre", "equals", `"Drama"`) + `}}`
	if got, want := matchExt(t, db, double), []string{"nameless"}; !reflect.DeepEqual(got, want) {
		t.Errorf("not not = %v, want %v", got, want)
	}
	notLt := `{"version":1,"not":` + cond("year", "lt", `1990`) + `}`
	got := matchExt(t, db, notLt)
	if !contains(got, "nameless") || contains(got, "alien") {
		t.Errorf("not(year<1990) = %v, want it to include the item without a year", got)
	}
}

func TestQueryIsTenantScoped(t *testing.T) {
	db := newTestDB(t)
	seedQueryCatalog(t, db)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES ('other', 'Other', ?, ?)", string(domain.TierSelfHosted), now); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, `INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms2', 'other', 'local_folder', 'x', 'push', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(ctx, db, "other", "ms2", Item{ExternalID: "theirs", Kind: domain.CatalogMovie, Title: "Alien Too", DurationMs: 60000, Genres: "Horror",
		CatalogMetadata: domain.CatalogMetadata{Studios: []string{"20th Century Fox"}}}, now); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		tree(cond("title", "contains", `"alien"`)),
		tree(cond("genre", "equals", `"Horror"`)),
		tree(cond("studio", "equals", `"20th Century Fox"`)),
	} {
		for _, ext := range matchExt(t, db, q) {
			if ext == "theirs" {
				t.Errorf("query %s matched another tenant's item", q)
			}
		}
	}
}

func TestParseQueryValidation(t *testing.T) {
	bad := map[string]string{
		"not an object":            `[]`,
		"null":                     `null`,
		"no version":               `{"all":[` + cond("kind", "equals", `"movie"`) + `]}`,
		"unknown version":          `{"version":2,"all":[` + cond("kind", "equals", `"movie"`) + `]}`,
		"unknown root key":         `{"version":1,"bogus":1,"all":[` + cond("kind", "equals", `"movie"`) + `]}`,
		"unknown field":            tree(cond("director", "equals", `"x"`)),
		"field injection":          tree(cond("title) OR 1=1 --", "contains", `"x"`)),
		"op not valid for field":   tree(cond("year", "contains", `"x"`)),
		"text field with equals":   tree(cond("title", "equals", `"x"`)),
		"list field with lt":       tree(cond("genre", "lt", `"x"`)),
		"enum with contains":       tree(cond("kind", "contains", `"movie"`)),
		"missing value":            `{"version":1,"field":"year","op":"equals"}`,
		"missing op":               `{"version":1,"field":"year","value":1}`,
		"empty all":                `{"version":1,"all":[]}`,
		"empty any":                `{"version":1,"any":[]}`,
		"empty object":             `{"version":1}`,
		"group and condition":      `{"version":1,"all":[` + cond("kind", "equals", `"movie"`) + `],"field":"kind","op":"equals","value":"movie"}`,
		"two groups":               `{"version":1,"all":[` + cond("kind", "equals", `"movie"`) + `],"any":[` + cond("kind", "equals", `"movie"`) + `]}`,
		"not with an array":        `{"version":1,"not":[` + cond("kind", "equals", `"movie"`) + `]}`,
		"bad kind value":           tree(cond("kind", "equals", `"series"`)),
		"string for a number":      tree(cond("year", "equals", `"1980"`)),
		"fractional year":          tree(cond("year", "equals", `1980.5`)),
		"fractional season":        tree(cond("season_number", "gte", `1.5`)),
		"between needs a pair":     tree(cond("year", "between", `[1980]`)),
		"between reversed":         tree(cond("year", "between", `[1990,1980]`)),
		"between scalar":           tree(cond("year", "between", `1980`)),
		"in needs an array":        tree(cond("genre", "in", `"Horror"`)),
		"in empty":                 tree(cond("genre", "in", `[]`)),
		"in with blank":            tree(cond("genre", "in", `["Horror",""]`)),
		"equals with an array":     tree(cond("genre", "equals", `["Horror"]`)),
		"blank text":               tree(cond("title", "contains", `"   "`)),
		"control character":        tree(cond("title", "contains", `"a\nb"`)),
		"bad date":                 tree(cond("premiere_date", "equals", `"1979-13-45"`)),
		"date not a string":        tree(cond("premiere_date", "equals", `19790525`)),
		"date with time":           tree(cond("premiere_date", "equals", `"1979-05-25T10:00:00Z"`)),
		"text too long":            tree(cond("title", "contains", `"`+strings.Repeat("a", 201)+`"`)),
		"too many values":          tree(cond("genre", "in", `[`+strings.Join(manyNames(101), ",")+`]`)),
		"content rating too long":  tree(cond("content_rating", "equals", `"`+strings.Repeat("R", 33)+`"`)),
		"huge number":              tree(cond("duration", "lt", `1e300`)),
		"trailing data":            tree(cond("kind", "equals", `"movie"`)) + ` {}`,
		"child with a version key": `{"version":1,"all":[{"version":1,"field":"kind","op":"equals","value":"movie"}]}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseQuery([]byte(raw)); err == nil {
				t.Errorf("ParseQuery accepted %s", raw)
			}
		})
	}

	if _, err := ParseQuery([]byte(`{"kind":"movie","genres":["Comedy"]}`)); !errors.Is(err, ErrLegacyCriteria) {
		t.Errorf("legacy shape: err = %v, want ErrLegacyCriteria", err)
	}

	// Errors name the offending node.
	_, err := ParseQuery([]byte(`{"version":1,"all":[` + cond("kind", "equals", `"movie"`) + `,{"not":` + cond("year", "contains", `"x"`) + `}]}`))
	if err == nil || !strings.Contains(err.Error(), "all[1].not") || !strings.Contains(err.Error(), `"contains" is not valid for field "year"`) {
		t.Errorf("error = %v, want it to locate all[1].not and name the bad operator", err)
	}
}

func manyNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = `"g` + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + `"`
	}
	return out
}

func TestParseQueryLimits(t *testing.T) {
	node := cond("kind", "equals", `"movie"`)
	for i := 0; i < 8; i++ {
		node = `{"not":` + node + `}`
	}
	if _, err := ParseQuery([]byte(tree(node))); err == nil {
		t.Error("a query nested 9 levels deep was accepted")
	}
	node = cond("kind", "equals", `"movie"`)
	for i := 0; i < 4; i++ {
		node = `{"not":` + node + `}`
	}
	if _, err := ParseQuery([]byte(tree(node))); err != nil {
		t.Errorf("a query nested 5 levels deep was rejected: %v", err)
	}
	conds := make([]string, maxQueryNodes+1)
	for i := range conds {
		conds[i] = cond("year", "equals", `1980`)
	}
	if _, err := ParseQuery([]byte(`{"version":1,"all":[` + strings.Join(conds, ",") + `]}`)); err == nil {
		t.Error("a query with too many nodes was accepted")
	}
}

func TestQueryCanonicalForm(t *testing.T) {
	raw := `{ "version": 1, "all": [ {"field":"genre","op":"in","value":["Horror","horror"," Sci-Fi "]},
		{"field":"premiere_date","op":"between","value":["1979-05-25","1980-01-01"]},
		{"field":"duration","op":"lt","value":90.0},
		{"not":{"field":"kind","op":"equals","value":"episode"}} ] }`
	q := mustQuery(t, raw)
	want := `{"version":1,"all":[{"field":"genre","op":"in","value":["Horror","Sci-Fi"]},{"field":"premiere_date","op":"between","value":["1979-05-25","1980-01-01"]},{"field":"duration","op":"lt","value":90},{"not":{"field":"kind","op":"equals","value":"episode"}}]}`
	if got := q.JSON(); got != want {
		t.Errorf("canonical JSON =\n%s\nwant\n%s", got, want)
	}
	if again := mustQuery(t, q.JSON()); again.JSON() != want {
		t.Errorf("canonical form is not stable: %s", again.JSON())
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestQueryCompilationIsInjectionSafe(t *testing.T) {
	db := newTestDB(t)
	seedQueryCatalog(t, db)
	ctx := context.Background()

	evil := `x'); DROP TABLE catalog_item; --`
	for _, q := range []string{
		tree(cond("title", "contains", mustJSON(evil))),
		tree(cond("genre", "equals", mustJSON(evil))),
		tree(cond("genre", "in", mustJSON([]string{evil, "Horror"}))),
		tree(cond("studio", "equals", mustJSON(evil))),
		tree(cond("person", "contains", mustJSON(evil))),
		tree(cond("content_rating", "equals", mustJSON(evil))),
		tree(cond("overview", "contains", mustJSON(evil))),
	} {
		parsed := mustQuery(t, q)
		where, args := parsed.Where(testTenantID)
		if strings.Contains(where, "DROP") || strings.Contains(where, evil) {
			t.Errorf("a value reached the SQL text: %s", where)
		}
		found := false
		for _, a := range args {
			if s, ok := a.(string); ok && strings.Contains(strings.ToLower(s), "drop table") {
				found = true
			}
		}
		if !found {
			t.Errorf("the value was not bound as an argument: %v", args)
		}
		if _, err := MatchQuery(ctx, db, testTenantID, parsed); err != nil {
			t.Errorf("MatchQuery: %v", err)
		}
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM catalog_item"); err != nil || n[0] == 0 {
		t.Fatalf("catalog_item was damaged: %v %v", n, err)
	}
	complexQ := mustQuery(t, `{"version":1,"all":[`+cond("genre", "in", `["a","b","c"]`)+`,{"any":[`+cond("year", "between", `[1,2]`)+`,{"not":`+cond("title", "contains", `"t"`)+`}]}]}`)
	where, args := complexQ.Where(testTenantID)
	if strings.Count(where, "?") != len(args) {
		t.Errorf("%d placeholders for %d args: %s", strings.Count(where, "?"), len(args), where)
	}
}

func TestIsLegacyCriteria(t *testing.T) {
	cases := map[string]bool{
		`{"kind":"movie","genres":["x"]}`:     true,
		`{}`:                                  true,
		`{"version":1,"all":[]}`:              false,
		`not json`:                            false,
		`{"version":1,"all":[{"field":"x"}]}`: false,
	}
	for raw, want := range cases {
		if got := IsLegacyCriteria(raw); got != want {
			t.Errorf("IsLegacyCriteria(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestResolveDynamicWithQueryTree(t *testing.T) {
	db := newTestDB(t)
	seedQueryCatalog(t, db)
	ctx := context.Background()
	q := mustQuery(t, tree(cond("genre", "equals", `"Horror"`))).JSON()
	seedPool(t, db, "p1", "Horror pool", &q)
	seedBlock(t, db, "b1", "Horror block", &q)
	if err := ResolveDynamic(ctx, db, testTenantID); err != nil {
		t.Fatal(err)
	}
	want := matchExt(t, db, q)
	var got []string
	if err := db.Query(ctx, &got,
		"SELECT c.external_id FROM filler_pool_item i JOIN catalog_item c ON c.id = i.catalog_item_id WHERE i.filler_pool_id = 'p1'"); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) || len(want) != 4 {
		t.Errorf("pool items = %v, want %v (4 horror items)", got, want)
	}
	if ids := blockItemCatalogIDs(t, db, "b1"); len(ids) != 4 {
		t.Errorf("block items = %d, want 4", len(ids))
	}
}
