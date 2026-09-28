package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestEPGEmptyBeforeMaterializer(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "GET", "/epg", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var entries []domain.EpgEntry
	json.Unmarshal(rec.Body.Bytes(), &entries)
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 (no materializer yet)", len(entries))
	}

	rec = doRequest(t, mux, "GET", "/epg?channel_id=chan-1&start=0&end=1000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestEPGMetaDefaultsWhenNoRevisionRow(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "GET", "/epg/meta", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var meta struct {
		Revision   int64 `json:"revision"`
		ValidUntil int64 `json:"valid_until"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.Revision != 0 || meta.ValidUntil != 0 {
		t.Fatalf("meta = %+v, want zero values", meta)
	}
}

func TestEPGMetaReflectsRevisionRow(t *testing.T) {
	db, mux := newTestDB(t)

	ctx := context.Background()
	if err := db.Exec(ctx,
		"INSERT INTO epg_revision (tenant_id, revision, valid_until, updated_at) VALUES (?, ?, ?, ?)",
		DefaultTenantID, 3, 1700000000000, 1700000000000,
	); err != nil {
		t.Fatalf("seed epg_revision: %v", err)
	}

	rec := doRequest(t, mux, "GET", "/epg/meta", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var meta struct {
		Revision   int64 `json:"revision"`
		ValidUntil int64 `json:"valid_until"`
	}
	json.Unmarshal(rec.Body.Bytes(), &meta)
	if meta.Revision != 3 || meta.ValidUntil != 1700000000000 {
		t.Fatalf("meta = %+v, want {3 1700000000000}", meta)
	}
}
