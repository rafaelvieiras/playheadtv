package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
)

// Saving, replacing and deleting a cut rule set through REST regenerates the
// guide: a timed 4 hour movie turns into segments with the channel's midroll
// between them, and back into one program.
func TestCutRuleSetsChangeTheGeneratedEPG(t *testing.T) {
	db, mux, mat := newTestDBWithMaterializer(t)
	newEPGSeed(t, db)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		VALUES ('big', ?, 'ms', 'jf-big', 'movie', 'Big Movie', 14400000, ?)`, DefaultTenantID, now)
	exec(`INSERT INTO block (id, tenant_id, name, list_mode, created_at, updated_at) VALUES ('blk', ?, 'Block', 'once', ?, ?)`, DefaultTenantID, now, now)
	exec(`INSERT INTO block_item (id, block_id, position, catalog_item_id) VALUES ('bi', 'blk', 0, 'big')`)
	exec(`INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES ('sch', ?, 'Schedule', ?, ?)`, DefaultTenantID, now, now)
	exec(`UPDATE channel SET schedule_id = 'sch' WHERE id = 'c1'`)
	exec(`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
		VALUES ('si', ?, 'sch', 'blk', 'once', ?, 1, ?, ?)`, DefaultTenantID, time.Now().Add(30*time.Minute).UnixMilli(), now, now)
	exec(`INSERT INTO filler_pool (id, tenant_id, name, created_at) VALUES ('pool', ?, 'Pool', ?)`, DefaultTenantID, now)
	exec(`INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES ('pi', 'pool', 'clip0', 1)`)
	exec(`INSERT INTO filler_rule (id, tenant_id, scope_type, scope_id, kind, filler_pool_id, selection_strategy, position, created_at)
		VALUES ('fr', ?, 'channel', 'c1', 'midroll', 'pool', 'sequential', 0, ?)`, DefaultTenantID, now)

	mat.SetCutPlanner(materializer.RuleSetCuts(db))
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	mat.Start(runCtx)

	programs := func() (n int, offsets []float64, midrolls int, grouped bool) {
		for _, e := range getEPGJSON(t, mux, "/epg?channel_id=c1") {
			switch e["kind"] {
			case "program":
				n++
				if off, ok := e["item_offset_ms"].(float64); ok {
					offsets = append(offsets, off)
				}
				if _, ok := e["split_group"]; ok {
					grouped = true
				}
			case "midroll":
				midrolls++
			}
		}
		return
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if ok() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		n, offsets, mids, grouped := programs()
		t.Fatalf("timed out waiting for %s (programs %d, offsets %v, midrolls %d, grouped %v)", what, n, offsets, mids, grouped)
	}

	waitFor("the uncut guide", func() bool { n, _, _, _ := programs(); return n == 1 })
	if _, _, mids, grouped := programs(); mids != 0 || grouped {
		t.Fatalf("the guide is cut before any rule set exists")
	}

	body := `{"scope_type":"channel","scope_id":"c1","rules":[{"match":{"kinds":["movie"],"min_duration_ms":10800000},"strategy":{"type":"interval","every_ms":3600000}}]}`
	rec := doRequest(t, mux, "POST", "/cut-rule-sets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var set struct {
		ID string `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &set)
	// Cuts at 1h, 2h and 3h: four segments and three midrolls.
	waitFor("segments after create", func() bool { n, _, m, g := programs(); return n == 4 && m == 3 && g })
	if _, offsets, _, _ := programs(); fmt.Sprint(offsets) != "[3.6e+06 7.2e+06 1.08e+07]" {
		t.Fatalf("offsets = %v", offsets)
	}

	put := `{"scope_type":"channel","scope_id":"c1","rules":[{"strategy":{"type":"offsets","offsets_ms":[7200000]}}]}`
	if rec := doRequest(t, mux, "PUT", "/cut-rule-sets/"+set.ID, put); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	waitFor("segments after update", func() bool { n, _, m, _ := programs(); return n == 2 && m == 1 })

	if rec := doRequest(t, mux, "DELETE", "/cut-rule-sets/"+set.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	waitFor("one program after delete", func() bool { n, _, m, g := programs(); return n == 1 && m == 0 && !g })
}
