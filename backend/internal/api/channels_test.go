package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestChannelsCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"News 24","timezone":"America/Sao_Paulo"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created domain.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == "" || created.Name != "News 24" || created.Timezone != "America/Sao_Paulo" {
		t.Fatalf("unexpected created channel: %+v", created)
	}

	rec = doRequest(t, mux, "GET", "/channels", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var list []domain.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/channels/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "PUT", "/channels/"+created.ID, `{"number":2,"name":"News 24 HD","timezone":"UTC"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "News 24 HD" || updated.Number != 2 {
		t.Fatalf("unexpected updated channel: %+v", updated)
	}

	rec = doRequest(t, mux, "DELETE", "/channels/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "GET", "/channels/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestChannelNotFound(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "GET", "/channels/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestChannelMissingName(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/channels", `{"number":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestChannelDuplicateNumber(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/channels", `{"number":5,"name":"A"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "POST", "/channels", `{"number":5,"name":"B"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate number: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}
