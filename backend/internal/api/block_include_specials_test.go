package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestBlockIncludeSpecials_DefaultsOffAndPutKeepsIt(t *testing.T) {
	_, mux := newTestDB(t)
	if b := createBlockWith(t, mux, `{"name":"Default"}`); b.IncludeSpecials {
		t.Error("default include_specials is on, want off")
	}
	on := createBlockWith(t, mux, `{"name":"On","include_specials":true}`)
	if !on.IncludeSpecials {
		t.Fatal("created with include_specials true, read back off")
	}
	get := func() domain.Block {
		var b domain.Block
		mustDecode(t, doRequest(t, mux, "GET", "/blocks/"+on.ID, "").Body.Bytes(), &b)
		return b
	}
	if !get().IncludeSpecials {
		t.Error("GET lost include_specials")
	}
	// Omitting it on a PUT keeps the stored value; an explicit value changes it.
	if rec := doRequest(t, mux, "PUT", "/blocks/"+on.ID, `{"name":"Renamed"}`); rec.Code != http.StatusOK || !get().IncludeSpecials {
		t.Errorf("PUT without the field: %d, include_specials = %v, want kept on", rec.Code, get().IncludeSpecials)
	}
	if rec := doRequest(t, mux, "PUT", "/blocks/"+on.ID, `{"name":"Renamed","include_specials":false}`); rec.Code != http.StatusOK || get().IncludeSpecials {
		t.Errorf("PUT false: %d, include_specials = %v, want off", rec.Code, get().IncludeSpecials)
	}
}

func TestBlockIncludeSpecials_SeriesItemViewAndPreviewFollowTheBlock(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 10)
	seedSpecial(t, db, "a", "Show", ms)
	body := fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"}}`, ms)
	count := func(blockID string) int {
		rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("add series item: %d %s", rec.Code, rec.Body.String())
		}
		var it domain.BlockItem
		mustDecode(t, rec.Body.Bytes(), &it)
		return it.Series.EpisodeCount
	}
	off := createBlockWith(t, mux, `{"name":"Off"}`)
	on := createBlockWith(t, mux, `{"name":"On","include_specials":true}`)
	if got := count(off.ID); got != 10 {
		t.Errorf("block without specials: episode_count = %d, want 10", got)
	}
	if got := count(on.ID); got != 11 {
		t.Errorf("block with specials: episode_count = %d, want 11", got)
	}

	preview := func(extra string) int64 {
		rec := doRequest(t, mux, "POST", "/series/slice-preview", fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show"%s}`, ms, extra))
		var out map[string]int64
		mustDecode(t, rec.Body.Bytes(), &out)
		return out["episode_count"]
	}
	if got := preview(""); got != 10 {
		t.Errorf("preview default = %d, want 10", got)
	}
	if got := preview(`,"include_specials":true`); got != 11 {
		t.Errorf("preview with specials = %d, want 11", got)
	}
	// A slice that names season 0 wins over the default.
	if got := preview(`,"slice":{"seasons":[0]}`); got != 1 {
		t.Errorf("preview seasons [0] = %d, want 1", got)
	}
}
