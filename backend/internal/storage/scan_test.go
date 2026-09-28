package storage

import "testing"

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
