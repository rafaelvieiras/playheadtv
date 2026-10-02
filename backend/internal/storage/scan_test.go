package storage

import (
	"context"
	"testing"
)

func TestToSnakeCase(t *testing.T) {
	cases := map[string]string{
		"TenantID":         "tenant_id",
		"GuideArtURL":      "guide_art_url",
		"LastProbeOK":      "last_probe_ok",
		"APITokenHash":     "api_token_hash",
		"CredentialCipher": "credential_cipher",
		"DurationMs":       "duration_ms",
		"SeriesTitle":      "series_title",
		"ID":               "id",
		"Name":             "name",
	}
	for input, want := range cases {
		if got := toSnakeCase(input); got != want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestScanFlattensEmbeddedStructAndNullableFloat(t *testing.T) {
	type Inner struct {
		Rating *float64
		Note   *string
	}
	type Outer struct {
		ID string
		Inner
	}
	db, err := Open(t.TempDir() + "/scan.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows []Outer
	if err := db.Query(context.Background(), &rows,
		"SELECT 'a' AS id, 7.5 AS rating, NULL AS note UNION ALL SELECT 'b', NULL, 'x'"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 || rows[0].Rating == nil || *rows[0].Rating != 7.5 || rows[0].Note != nil ||
		rows[1].Rating != nil || rows[1].Note == nil || *rows[1].Note != "x" {
		t.Fatalf("rows = %+v", rows)
	}
}
