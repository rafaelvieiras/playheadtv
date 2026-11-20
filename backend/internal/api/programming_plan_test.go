package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestProgrammingPlan_RoundTrip exports a small grid, wipes it, imports the
// export back and checks that a second export is byte-identical: the document
// carries everything the instance needs to rebuild the grid.
func TestProgrammingPlan_RoundTrip(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "m1", "Comedy")
	seedGenreBlockCatalogItem(t, db, "m2", "Comedy")

	col := newCollection(t, mux, comedyQuery)
	blockID := newBlock(t, mux, `{"name":"Comedy films"}`)
	if rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(col, "shuffle", `{"mode":"all"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("criteria item: %d %s", rec.Code, rec.Body.String())
	}
	schedID := newSchedule(t, mux, "Evenings")
	// One collection-targeted item (issue #100) and one block item.
	if rec := doRequest(t, mux, "POST", "/schedules/"+schedID+"/items",
		`{"collection_id":"`+col+`","playback_order":"random","recurrence_type":"continuous"}`); rec.Code != http.StatusCreated {
		t.Fatalf("collection item: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, mux, "POST", "/schedules/"+schedID+"/items",
		`{"block_id":"`+blockID+`","recurrence_type":"weekly","days_of_week":[4,5],"start_time_local":"22:00"}`); rec.Code != http.StatusCreated {
		t.Fatalf("weekly item: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, mux, "POST", "/channels",
		`{"number":7,"name":"Cinema","timezone":"America/Sao_Paulo","schedule_id":"`+schedID+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("channel: %d %s", rec.Code, rec.Body.String())
	}

	rec := doRequest(t, mux, "GET", "/programming-plan", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	first := rec.Body.String()
	var plan ProgrammingPlan
	mustDecode(t, rec.Body.Bytes(), &plan)
	if plan.Version != PlanVersion || len(plan.Collections) != 1 || len(plan.Blocks) != 1 || len(plan.Schedules) != 1 || len(plan.Channels) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Schedules[0].Items[0].Collection == "" || plan.Schedules[0].Items[1].Block != "Comedy films" {
		t.Errorf("schedule items lost their targets: %+v", plan.Schedules[0].Items)
	}
	if strings.Contains(first, `"id"`) || strings.Contains(first, "credential") {
		t.Errorf("plan carries ids or credentials: %s", first)
	}

	// Wipe and import: every object is created again from the document alone.
	for _, table := range []string{"channel", "schedule_item", "schedule", "block_item", "block", "collection"} {
		if err := db.Exec(t.Context(), "DELETE FROM "+table); err != nil {
			t.Fatalf("wipe %s: %v", table, err)
		}
	}
	rec = doRequest(t, mux, "POST", "/programming-plan", first)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var report PlanReport
	mustDecode(t, rec.Body.Bytes(), &report)
	for _, step := range report.Steps {
		if step.Action != "created" {
			t.Errorf("%s %s: %s %s", step.Kind, step.Name, step.Action, step.Reason)
		}
	}

	rec = doRequest(t, mux, "GET", "/programming-plan", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("re-export: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != first {
		t.Fatalf("the re-export differs:\n%s\n---\n%s", first, rec.Body.String())
	}

	// Importing the same document again changes nothing.
	rec = doRequest(t, mux, "POST", "/programming-plan?dry_run=true", first)
	var second PlanReport
	mustDecode(t, rec.Body.Bytes(), &second)
	if !second.DryRun {
		t.Error("dry run report does not say so")
	}
	for _, step := range second.Steps {
		if step.Action != "updated" && step.Action != "unchanged" {
			t.Errorf("%s %s: %s, want updated (or unchanged)", step.Kind, step.Name, step.Action)
		}
	}
}

// TestProgrammingPlan_DryRunWritesNothing checks that a dry run reports and
// does not create anything.
func TestProgrammingPlan_DryRunWritesNothing(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "m1", "Comedy")
	body := `{"version":1,"collections":[{"name":"C","query":` + comedyQuery + `}],
	          "blocks":[{"name":"B","collection":"C","playback_order":"random","items":[]}],
	          "schedules":[{"name":"S","items":[{"collection":"C","playback_order":"shuffle","recurrence_type":"continuous"}]}],
	          "channels":[{"number":3,"name":"Ch","timezone":"UTC","schedule":"S"}]}`
	rec := doRequest(t, mux, "POST", "/programming-plan?dry_run=true", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	var counts []int
	if err := db.Query(t.Context(), &counts, "SELECT COUNT(*) FROM collection"); err != nil || counts[0] != 0 {
		t.Fatalf("dry run created a collection: %v %v", counts, err)
	}
	if err := db.Query(t.Context(), &counts, "SELECT COUNT(*) FROM channel"); err != nil || counts[0] != 0 {
		t.Fatalf("dry run created a channel: %v %v", counts, err)
	}
}

// TestProgrammingPlan_RejectsAMissingReference checks the report and that a
// broken document is rejected instead of half-applied (nothing is written).
func TestProgrammingPlan_RejectsAMissingReference(t *testing.T) {
	db, mux := newTestDB(t)
	body := `{"version":1,"collections":[],"blocks":[],
	          "schedules":[{"name":"S","items":[{"block":"nope","recurrence_type":"continuous"}]}],
	          "channels":[]}`
	rec := doRequest(t, mux, "POST", "/programming-plan", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	var report PlanReport
	mustDecode(t, rec.Body.Bytes(), &report)
	if len(report.Steps) != 1 || report.Steps[0].Action != "rejected" || !strings.Contains(report.Steps[0].Reason, "nope") {
		t.Fatalf("report = %+v", report)
	}
	var counts []int
	if err := db.Query(t.Context(), &counts, "SELECT COUNT(*) FROM schedule"); err != nil || counts[0] != 0 {
		t.Fatalf("a rejected plan wrote a schedule: %v", counts)
	}
}

// TestProgrammingPlan_ConflictPolicies checks skip and reject.
func TestProgrammingPlan_ConflictPolicies(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "m1", "Comedy")
	plan := `{"version":1,"collections":[{"name":"C","query":` + comedyQuery + `}],"blocks":[],"schedules":[],"channels":[]}`
	if rec := doRequest(t, mux, "POST", "/programming-plan", plan); rec.Code != http.StatusOK {
		t.Fatalf("seed import: %d %s", rec.Code, rec.Body.String())
	}
	changed := `{"version":1,"collections":[{"name":"C","query":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]}}],"blocks":[],"schedules":[],"channels":[]}`

	// reject: the object exists, so the import refuses and keeps the old query.
	rec := doRequest(t, mux, "POST", "/programming-plan?on_conflict=reject", changed)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	// skip: reported and untouched.
	rec = doRequest(t, mux, "POST", "/programming-plan?on_conflict=skip", changed)
	if rec.Code != http.StatusOK {
		t.Fatalf("skip: %d %s", rec.Code, rec.Body.String())
	}
	var report PlanReport
	mustDecode(t, rec.Body.Bytes(), &report)
	if report.Steps[0].Action != "skipped" {
		t.Fatalf("skip report = %+v", report.Steps[0])
	}
	var raw []string
	if err := db.Query(t.Context(), &raw, "SELECT query FROM collection WHERE name = 'C'"); err != nil || !strings.Contains(raw[0], `"genre"`) {
		t.Fatalf("skip changed the collection: %v %v", raw, err)
	}
	// update (the default) applies it.
	rec = doRequest(t, mux, "POST", "/programming-plan", changed)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.Query(t.Context(), &raw, "SELECT query FROM collection WHERE name = 'C'"); err != nil || strings.Contains(raw[0], `"genre"`) {
		t.Fatalf("update did not change the collection: %v %v", raw, err)
	}
}
