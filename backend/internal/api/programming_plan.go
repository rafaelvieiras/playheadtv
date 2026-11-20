// Programming plan: export and import a whole grid as one JSON document
// (issue #101).
//
// A grid is a dependency graph (collections, blocks, their items, schedules,
// their items, channels), so authoring it object by object is the reason a
// migration becomes a script nobody can reuse. This file turns the graph into a
// document with no numeric ids, no credentials and no paths — every reference
// is a name (a collection, a block, a schedule) or a catalog identity (media
// source name + external id) — and applies such a document back, idempotently,
// matched by name, with a dry run that says exactly what would change.
//
// Filler pools, rules and break templates are deliberately NOT part of the plan
// yet: they hang off blocks by id and their own scoping is a second graph. See
// docs/ARCHITECTURE.md "Programming plan".
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/blockitem"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// PlanVersion is the document format version. An import refuses a version it
// does not know, so a newer export cannot be half-applied by an older backend.
const PlanVersion = 1

func registerProgrammingPlan(mux Router, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("GET /programming-plan", exportProgrammingPlan(db))
	mux.HandleFunc("POST /programming-plan", importProgrammingPlan(db, mat))
}

// PlanCollection is one collection in a plan document.
type PlanCollection struct {
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Query       json.RawMessage `json:"query"`
}

// PlanBlockItem is one item of a block: a catalog item, a series or a criteria
// item, referenced by identity instead of by id.
type PlanBlockItem struct {
	Type           string                    `json:"type"`
	MediaSource    string                    `json:"media_source,omitempty"`
	ExternalID     string                    `json:"external_id,omitempty"`
	SeriesTitle    string                    `json:"series_title,omitempty"`
	Collection     string                    `json:"collection,omitempty"`
	PlaybackOrder  string                    `json:"playback_order,omitempty"`
	Amount         *domain.SeriesAmountInput `json:"amount,omitempty"`
	Slice          *domain.SeriesSlice       `json:"slice,omitempty"`
	Interleave     *domain.InterleaveInput   `json:"interleave,omitempty"`
	TrimDurationMs *int64                    `json:"trim_duration_ms,omitempty"`
}

// PlanBlock is one block of a plan document.
type PlanBlock struct {
	Name            string                  `json:"name"`
	Description     *string                 `json:"description,omitempty"`
	ListMode        string                  `json:"list_mode,omitempty"`
	Collection      string                  `json:"collection,omitempty"`
	PlaybackOrder   string                  `json:"playback_order,omitempty"`
	IncludeSpecials bool                    `json:"include_specials,omitempty"`
	Interleave      *domain.InterleaveInput `json:"interleave,omitempty"`
	Items           []PlanBlockItem         `json:"items,omitempty"`
}

// PlanScheduleItem is one recurrence rule: what airs, when, and in which order
// when it names a collection.
type PlanScheduleItem struct {
	Block          string `json:"block,omitempty"`
	Collection     string `json:"collection,omitempty"`
	PlaybackOrder  string `json:"playback_order,omitempty"`
	RecurrenceType string `json:"recurrence_type"`
	DaysOfWeek     []int  `json:"days_of_week,omitempty"`
	StartTimeLocal string `json:"start_time_local,omitempty"`
	StartAt        *int64 `json:"start_at,omitempty"`
	Active         *bool  `json:"active,omitempty"`
}

// PlanSchedule is one schedule of a plan document.
type PlanSchedule struct {
	Name        string             `json:"name"`
	Description *string            `json:"description,omitempty"`
	Items       []PlanScheduleItem `json:"items"`
}

// PlanChannel is one channel of a plan document.
type PlanChannel struct {
	Number       int     `json:"number"`
	Name         string  `json:"name"`
	Timezone     string  `json:"timezone"`
	GuideArtURL  *string `json:"guide_art_url,omitempty"`
	Schedule     string  `json:"schedule,omitempty"`
	BaseSchedule string  `json:"base_schedule,omitempty"`
}

// ProgrammingPlan is the document: deterministic order, no ids, no secrets.
type ProgrammingPlan struct {
	Version     int              `json:"version"`
	Collections []PlanCollection `json:"collections"`
	Blocks      []PlanBlock      `json:"blocks"`
	Schedules   []PlanSchedule   `json:"schedules"`
	Channels    []PlanChannel    `json:"channels"`
}

// PlanStep is what an import did (or would do) to one object.
type PlanStep struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"` // created, updated, unchanged, skipped, rejected
	Reason string `json:"reason,omitempty"`
}

// PlanReport is the answer of an import.
type PlanReport struct {
	DryRun bool       `json:"dry_run"`
	Steps  []PlanStep `json:"steps"`
}

func (r *PlanReport) add(kind, name, action, reason string) {
	r.Steps = append(r.Steps, PlanStep{Kind: kind, Name: name, Action: action, Reason: reason})
}

// rejected reports whether any step was rejected (a dry run always "succeeds"
// in the HTTP sense: the report carries the verdict).
func (r *PlanReport) rejected() bool {
	for _, s := range r.Steps {
		if s.Action == "rejected" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

func exportProgrammingPlan(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, err := buildProgrammingPlan(r.Context(), db)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	}
}

func buildProgrammingPlan(ctx context.Context, db storage.DB) (ProgrammingPlan, error) {
	plan := ProgrammingPlan{Version: PlanVersion,
		Collections: []PlanCollection{}, Blocks: []PlanBlock{}, Schedules: []PlanSchedule{}, Channels: []PlanChannel{}}

	// Collections, by name (the key an import resolves references with).
	var colls []struct {
		Name        string
		Description *string
		Query       string
	}
	if err := db.Query(ctx, &colls,
		"SELECT name, description, query FROM collection WHERE tenant_id = ? ORDER BY name", DefaultTenantID,
	); err != nil {
		return plan, err
	}
	for _, c := range colls {
		plan.Collections = append(plan.Collections, PlanCollection{Name: c.Name, Description: c.Description, Query: json.RawMessage(c.Query)})
	}

	blockNames := map[string]string{} // id -> name
	collectionsByID := map[string]string{}
	var colIDs []struct {
		ID   string
		Name string
	}
	if err := db.Query(ctx, &colIDs, "SELECT id, name FROM collection WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return plan, err
	}
	for _, c := range colIDs {
		collectionsByID[c.ID] = c.Name
	}

	var blocks []struct {
		ID              string
		Name            string
		Description     *string
		CollectionID    *string
		PlaybackOrder   string
		ListMode        string
		IncludeSpecials bool
		InterleaveSpec  *string
	}
	if err := db.Query(ctx, &blocks,
		`SELECT id, name, description, collection_id, playback_order, COALESCE(list_mode, '') AS list_mode,
		        include_specials, interleave_spec
		   FROM block WHERE tenant_id = ? ORDER BY name`, DefaultTenantID,
	); err != nil {
		return plan, err
	}
	for _, b := range blocks {
		blockNames[b.ID] = b.Name
		pb := PlanBlock{
			Name:            b.Name,
			Description:     b.Description,
			ListMode:        b.ListMode,
			PlaybackOrder:   b.PlaybackOrder,
			IncludeSpecials: b.IncludeSpecials,
		}
		if b.CollectionID != nil {
			pb.Collection = collectionsByID[*b.CollectionID]
		}
		if b.InterleaveSpec != nil {
			var il domain.InterleaveInput
			if err := json.Unmarshal([]byte(*b.InterleaveSpec), &il); err == nil {
				pb.Interleave = &il
			}
		}
		items, err := exportBlockItems(ctx, db, b.ID)
		if err != nil {
			return plan, err
		}
		pb.Items = items
		plan.Blocks = append(plan.Blocks, pb)
	}

	var scheds []struct {
		ID          string
		Name        string
		Description *string
	}
	if err := db.Query(ctx, &scheds,
		"SELECT id, name, description FROM schedule WHERE tenant_id = ? ORDER BY name", DefaultTenantID,
	); err != nil {
		return plan, err
	}
	for _, sc := range scheds {
		ps := PlanSchedule{Name: sc.Name, Description: sc.Description}
		var rows []struct {
			BlockID       *string
			CollectionID  *string
			PlaybackOrder *string
			Recurrence    string
			DaysOfWeek    string
			StartTime     *string
			StartAt       *int64
			Active        bool
		}
		if err := db.Query(ctx, &rows,
			`SELECT block_id, collection_id, playback_order, recurrence_type AS recurrence, days_of_week,
			        start_time_local AS start_time, start_at, active
			   FROM schedule_item WHERE tenant_id = ? AND schedule_id = ? ORDER BY created_at`, DefaultTenantID, sc.ID,
		); err != nil {
			return plan, err
		}
		for _, row := range rows {
			pi := PlanScheduleItem{
				RecurrenceType: row.Recurrence,
				DaysOfWeek:     parseDays(row.DaysOfWeek),
				StartAt:        row.StartAt,
				Active:         &row.Active,
			}
			if row.StartTime != nil {
				pi.StartTimeLocal = *row.StartTime
			}
			if row.BlockID != nil {
				pi.Block = blockNames[*row.BlockID]
			}
			if row.CollectionID != nil {
				pi.Collection = collectionsByID[*row.CollectionID]
			}
			if row.PlaybackOrder != nil {
				pi.PlaybackOrder = *row.PlaybackOrder
			}
			ps.Items = append(ps.Items, pi)
		}
		if ps.Items == nil {
			ps.Items = []PlanScheduleItem{}
		}
		plan.Schedules = append(plan.Schedules, ps)
	}

	scheduleNames := map[string]string{}
	var schedIDs []struct {
		ID   string
		Name string
	}
	if err := db.Query(ctx, &schedIDs, "SELECT id, name FROM schedule WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return plan, err
	}
	for _, s := range schedIDs {
		scheduleNames[s.ID] = s.Name
	}

	var channels []struct {
		Number         int
		Name           string
		Timezone       string
		GuideArtURL    *string
		ScheduleID     *string
		BaseScheduleID *string
	}
	if err := db.Query(ctx, &channels,
		"SELECT number, name, timezone, guide_art_url, schedule_id, base_schedule_id FROM channel WHERE tenant_id = ? ORDER BY number",
		DefaultTenantID,
	); err != nil {
		return plan, err
	}
	for _, ch := range channels {
		pc := PlanChannel{Number: ch.Number, Name: ch.Name, Timezone: ch.Timezone, GuideArtURL: ch.GuideArtURL}
		if ch.ScheduleID != nil {
			pc.Schedule = scheduleNames[*ch.ScheduleID]
		}
		if ch.BaseScheduleID != nil {
			pc.BaseSchedule = scheduleNames[*ch.BaseScheduleID]
		}
		plan.Channels = append(plan.Channels, pc)
	}
	return plan, nil
}

// parseDays turns the stored comma-separated weekday column back into a list.
func parseDays(raw string) []int {
	var out []int
	for _, part := range splitComma(raw) {
		var d int
		if _, err := fmt.Sscanf(part, "%d", &d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// exportBlockItems reads one block's items into their id-free form.
func exportBlockItems(ctx context.Context, db storage.DB, blockID string) ([]PlanBlockItem, error) {
	var rows []struct {
		ItemType      string
		CatalogItemID string
		TrimMs        *int64
		Spec          *string
	}
	if err := db.Query(ctx, &rows,
		"SELECT item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms AS trim_ms, spec FROM block_item WHERE block_id = ? ORDER BY position",
		blockID,
	); err != nil {
		return nil, err
	}
	items := make([]PlanBlockItem, 0, len(rows))
	for _, row := range rows {
		switch row.ItemType {
		case domain.BlockItemCatalog:
			var ref []struct {
				ExternalID    string
				MediaSourceID string
			}
			if err := db.Query(ctx, &ref,
				"SELECT external_id, media_source_id FROM catalog_item WHERE id = ?", row.CatalogItemID,
			); err != nil {
				return nil, err
			}
			if len(ref) == 0 {
				// A catalog item the source dropped: keep the row out of the
				// plan rather than exporting a dangling reference.
				continue
			}
			name, err := mediaSourceName(ctx, db, ref[0].MediaSourceID)
			if err != nil {
				return nil, err
			}
			items = append(items, PlanBlockItem{
				Type: domain.BlockItemCatalog, MediaSource: name, ExternalID: ref[0].ExternalID, TrimDurationMs: row.TrimMs,
			})
		case domain.BlockItemSeries:
			spec, err := series.ParseSpec(row.Spec)
			if err != nil {
				return nil, err
			}
			name, err := mediaSourceName(ctx, db, spec.MediaSourceID)
			if err != nil {
				return nil, err
			}
			amount := amountInput(spec.EffectiveAmount())
			items = append(items, PlanBlockItem{
				Type: domain.BlockItemSeries, MediaSource: name, SeriesTitle: spec.SeriesTitle,
				PlaybackOrder: spec.PlaybackOrder, Amount: amount, Slice: spec.Slice,
			})
		case domain.BlockItemCriteria:
			spec, err := blockitem.ParseCriteriaSpec(row.Spec)
			if err != nil {
				return nil, err
			}
			name := ""
			if spec.CollectionID != "" {
				if err := db.Query(ctx, &[]string{}, "SELECT 1"); err != nil { // no-op keeps sqlx-style scan honest
					return nil, err
				}
				var names []string
				if err := db.Query(ctx, &names, "SELECT name FROM collection WHERE tenant_id = ? AND id = ?", DefaultTenantID, spec.CollectionID); err != nil {
					return nil, err
				}
				if len(names) > 0 {
					name = names[0]
				}
			}
			items = append(items, PlanBlockItem{
				Type: domain.BlockItemCriteria, Collection: name, PlaybackOrder: spec.PlaybackOrder,
				Amount: amountInput(domain.SeriesAmount{Mode: spec.Amount.Mode, Count: spec.Amount.Count}),
			})
		}
	}
	return items, nil
}

func amountInput(a domain.SeriesAmount) *domain.SeriesAmountInput {
	switch a.Mode {
	case "", domain.AmountSlotEnd:
		return &domain.SeriesAmountInput{Mode: domain.AmountSlotEnd}
	case domain.AmountCount:
		n := a.Count
		return &domain.SeriesAmountInput{Mode: domain.AmountCount, Count: &n}
	case domain.AmountSeriesEnd:
		return &domain.SeriesAmountInput{Mode: domain.AmountSeriesEnd}
	}
	return &domain.SeriesAmountInput{Mode: a.Mode}
}

func mediaSourceName(ctx context.Context, db storage.DB, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var names []string
	if err := db.Query(ctx, &names, "SELECT name FROM media_source WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

func importProgrammingPlan(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var plan ProgrammingPlan
		if err := decodeJSON(r, &plan); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if plan.Version != PlanVersion {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("version must be %d", PlanVersion))
			return
		}
		dryRun := r.URL.Query().Get("dry_run") == "true" || r.URL.Query().Get("dry_run") == "1"
		conflict := r.URL.Query().Get("on_conflict")
		if conflict == "" {
			conflict = "update"
		}
		switch conflict {
		case "update", "skip", "reject":
		default:
			writeError(w, http.StatusBadRequest, "on_conflict must be update, skip or reject")
			return
		}
		imp := &planImporter{ctx: r.Context(), db: db, mat: mat, dryRun: dryRun, conflict: conflict,
			report:  &PlanReport{DryRun: dryRun},
			ids:     map[string]map[string]string{"collection": {}, "block": {}, "schedule": {}},
			exists:  map[string]map[string]string{"collection": {}, "block": {}, "schedule": {}, "channel": {}},
			touched: map[string]bool{}}
		if err := imp.load(); err != nil {
			handleDBError(w, err)
			return
		}
		if err := imp.apply(&plan); err != nil {
			handleDBError(w, err)
			return
		}
		if !dryRun && imp.applied {
			// Every write above landed in a scheduling table, so the guide of
			// the channels the plan touches has to be regenerated.
			imp.resetChannels(mat)
		}
		status := http.StatusOK
		if imp.report.rejected() {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, imp.report)
	}
}

// planImporter carries one import's state: what exists, what was created, and
// the report.
type planImporter struct {
	ctx      context.Context
	db       storage.DB
	mat      *materializer.Materializer
	dryRun   bool
	conflict string
	report   *PlanReport
	applied  bool
	// ids holds the id each name resolved to during this import (created or
	// existing); exists holds what the instance had before it started.
	ids    map[string]map[string]string
	exists map[string]map[string]string
	// touched holds the channel numbers this import created or changed.
	touched map[string]bool
}

func (i *planImporter) load() error {
	var colls []struct {
		ID   string
		Name string
	}
	if err := i.db.Query(i.ctx, &colls, "SELECT id, name FROM collection WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return err
	}
	for _, c := range colls {
		i.exists["collection"][c.Name] = c.ID
		i.ids["collection"][c.Name] = c.ID
	}
	var blocks []struct {
		ID   string
		Name string
	}
	if err := i.db.Query(i.ctx, &blocks, "SELECT id, name FROM block WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return err
	}
	for _, b := range blocks {
		i.exists["block"][b.Name] = b.ID
		i.ids["block"][b.Name] = b.ID
	}
	var scheds []struct {
		ID   string
		Name string
	}
	if err := i.db.Query(i.ctx, &scheds, "SELECT id, name FROM schedule WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return err
	}
	for _, s := range scheds {
		i.exists["schedule"][s.Name] = s.ID
		i.ids["schedule"][s.Name] = s.ID
	}
	var chans []struct {
		ID     string
		Number int
	}
	if err := i.db.Query(i.ctx, &chans, "SELECT id, number FROM channel WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return err
	}
	for _, c := range chans {
		i.exists["channel"][fmt.Sprint(c.Number)] = c.ID
	}
	return nil
}

// decide resolves the conflict policy for an object that already exists.
func (i *planImporter) decide(kind, name string) (action string, skip bool) {
	if _, ok := i.exists[kind][name]; !ok {
		return "created", false
	}
	switch i.conflict {
	case "skip":
		return "skipped", true
	case "reject":
		return "rejected", true
	}
	return "updated", false
}

func (i *planImporter) apply(plan *ProgrammingPlan) error {
	// Deployment order is the dependency order: collections, blocks, schedules,
	// channels.
	for _, c := range plan.Collections {
		if err := i.applyCollection(c); err != nil {
			return err
		}
	}
	for _, b := range plan.Blocks {
		if err := i.applyBlock(b); err != nil {
			return err
		}
	}
	for _, s := range plan.Schedules {
		if err := i.applySchedule(s); err != nil {
			return err
		}
	}
	for _, ch := range plan.Channels {
		if err := i.applyChannel(ch); err != nil {
			return err
		}
	}
	return nil
}

func (i *planImporter) applyCollection(c PlanCollection) error {
	if c.Name == "" {
		i.report.add("collection", "", "rejected", "name is required")
		return nil
	}
	if _, err := catalogsync.ParseQuery(c.Query); err != nil {
		i.report.add("collection", c.Name, "rejected", "query is invalid: "+err.Error())
		return nil
	}
	action, skip := i.decide("collection", c.Name)
	if skip {
		i.report.add("collection", c.Name, action, "")
		return nil
	}
	if i.dryRun {
		// Keep an id a previous pass resolved (an object that already exists):
		// the placeholder is only for one this run would have created.
		if _, ok := i.ids["collection"][c.Name]; !ok {
			i.ids["collection"][c.Name] = "dry-run:" + c.Name
		}
		i.report.add("collection", c.Name, action, "")
		return nil
	}
	now := time.Now().UnixMilli()
	id := i.exists["collection"][c.Name]
	if id == "" {
		id = uuid.NewString()
		if err := i.db.Exec(i.ctx,
			"INSERT INTO collection (id, tenant_id, name, description, query, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, DefaultTenantID, c.Name, c.Description, string(c.Query), now, now,
		); err != nil {
			return err
		}
	} else if err := i.db.Exec(i.ctx,
		"UPDATE collection SET description = ?, query = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
		c.Description, string(c.Query), now, DefaultTenantID, id,
	); err != nil {
		return err
	}
	i.ids["collection"][c.Name] = id
	i.applied = true
	i.report.add("collection", c.Name, action, "")
	return nil
}

func (i *planImporter) applyBlock(b PlanBlock) error {
	if b.Name == "" {
		i.report.add("block", "", "rejected", "name is required")
		return nil
	}
	var collectionID *string
	if b.Collection != "" {
		id, ok := i.ids["collection"][b.Collection]
		if !ok {
			i.report.add("block", b.Name, "rejected", "collection "+b.Collection+" is not in the plan and does not exist")
			return nil
		}
		collectionID = &id
	}
	playback, _, err := blockitem.NewBlockPlayback(collectionID != nil, b.PlaybackOrder, b.Interleave, nil)
	if err != nil {
		i.report.add("block", b.Name, "rejected", err.Error())
		return nil
	}
	listMode, _, err := blockitem.NewListMode(collectionID != nil, b.ListMode, nil)
	if err != nil {
		i.report.add("block", b.Name, "rejected", err.Error())
		return nil
	}
	// Validate every item before touching anything.
	type item struct {
		itemType string
		catalog  string
		trim     *int64
		spec     string
	}
	prepared := make([]item, 0, len(b.Items))
	for _, it := range b.Items {
		switch it.Type {
		case domain.BlockItemCatalog:
			var ids []string
			if it.ExternalID == "" {
				i.report.add("block", b.Name, "rejected", "a catalog item needs external_id")
				return nil
			}
			query := "SELECT ci.id FROM catalog_item ci JOIN media_source ms ON ms.id = ci.media_source_id WHERE ci.tenant_id = ? AND ci.external_id = ?"
			args := []any{DefaultTenantID, it.ExternalID}
			if it.MediaSource != "" {
				query += " AND ms.name = ?"
				args = append(args, it.MediaSource)
			}
			if err := i.db.Query(i.ctx, &ids, query, args...); err != nil {
				return err
			}
			if len(ids) == 0 {
				i.report.add("block", b.Name, "rejected", "catalog item "+it.ExternalID+" is not in the catalog")
				return nil
			}
			prepared = append(prepared, item{itemType: domain.BlockItemCatalog, catalog: ids[0], trim: it.TrimDurationMs})
		case domain.BlockItemSeries:
			spec := domain.SeriesSpec{SeriesTitle: it.SeriesTitle, PlaybackOrder: it.PlaybackOrder}
			if it.MediaSource != "" {
				ms, ok := i.mediaSourceNamed(it.MediaSource)
				if !ok {
					i.report.add("block", b.Name, "rejected", "media source "+it.MediaSource+" does not exist")
					return nil
				}
				spec.MediaSourceID = ms
			}
			if it.Amount != nil {
				amount, err := series.NewAmount(it.Amount)
				if err != nil {
					i.report.add("block", b.Name, "rejected", err.Error())
					return nil
				}
				spec.Amount = &amount
			}
			spec.Slice = it.Slice
			encoded, err := series.Encode(spec)
			if err != nil {
				i.report.add("block", b.Name, "rejected", err.Error())
				return nil
			}
			prepared = append(prepared, item{itemType: domain.BlockItemSeries, spec: encoded})
		case domain.BlockItemCriteria:
			id, ok := i.ids["collection"][it.Collection]
			if !ok {
				i.report.add("block", b.Name, "rejected", "collection "+it.Collection+" is not in the plan and does not exist")
				return nil
			}
			amount, err := blockitem.NewCriteriaAmount(it.Amount)
			if err != nil {
				i.report.add("block", b.Name, "rejected", err.Error())
				return nil
			}
			spec, err := blockitem.NewCriteriaSpec(i.ctx, i.db, DefaultTenantID, id, it.PlaybackOrder, it.Amount, it.Interleave)
			if err != nil {
				i.report.add("block", b.Name, "rejected", err.Error())
				return nil
			}
			spec.Amount = amount
			encoded, err := blockitem.EncodeCriteriaSpec(spec)
			if err != nil {
				return err
			}
			prepared = append(prepared, item{itemType: domain.BlockItemCriteria, spec: encoded})
		default:
			i.report.add("block", b.Name, "rejected", "item type must be catalog, series or criteria")
			return nil
		}
	}

	action, skip := i.decide("block", b.Name)
	if skip {
		i.report.add("block", b.Name, action, "")
		return nil
	}
	if i.dryRun {
		// Keep an id a previous pass resolved (an object that already exists):
		// the placeholder is only for one this run would have created.
		if _, ok := i.ids["block"][b.Name]; !ok {
			i.ids["block"][b.Name] = "dry-run:" + b.Name
		}
		i.report.add("block", b.Name, action, "")
		return nil
	}
	now := time.Now().UnixMilli()
	id := i.exists["block"][b.Name]
	if id == "" {
		id = uuid.NewString()
		if err := i.db.Exec(i.ctx,
			`INSERT INTO block (id, tenant_id, name, description, collection_id, playback_order, list_mode, include_specials,
			                    interleave_spec, playback_seed, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, DefaultTenantID, b.Name, b.Description, collectionID, playback.Order, listMode,
			b.IncludeSpecials, playback.InterleaveSpec, playback.Seed, now, now,
		); err != nil {
			return err
		}
	} else if err := i.db.Exec(i.ctx,
		`UPDATE block SET description = ?, collection_id = ?, playback_order = ?, list_mode = ?, include_specials = ?,
		        interleave_spec = ?, playback_seed = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
		b.Description, collectionID, playback.Order, listMode, b.IncludeSpecials,
		playback.InterleaveSpec, playback.Seed, now, DefaultTenantID, id,
	); err != nil {
		return err
	}
	// The item list is a full replace, like PUT /blocks/{id}/items one by one.
	if err := i.db.Exec(i.ctx, "DELETE FROM block_item WHERE block_id = ?", id); err != nil {
		return err
	}
	for pos, it := range prepared {
		var catalog any
		if it.catalog != "" {
			catalog = it.catalog
		}
		var spec any
		if it.spec != "" {
			spec = it.spec
		}
		if err := i.db.Exec(i.ctx,
			`INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), id, pos, it.itemType, catalog, it.trim, spec,
		); err != nil {
			return err
		}
	}
	i.ids["block"][b.Name] = id
	i.applied = true
	i.report.add("block", b.Name, action, "")
	return nil
}

// mediaSourceNamed resolves a media source by name.
func (i *planImporter) mediaSourceNamed(name string) (string, bool) {
	var ids []string
	if err := i.db.Query(i.ctx, &ids, "SELECT id FROM media_source WHERE tenant_id = ? AND name = ?", DefaultTenantID, name); err != nil {
		return "", false
	}
	if len(ids) == 0 {
		return "", false
	}
	return ids[0], true
}

func (i *planImporter) applySchedule(s PlanSchedule) error {
	if s.Name == "" {
		i.report.add("schedule", "", "rejected", "name is required")
		return nil
	}
	type prepared struct {
		blockID      string
		collectionID string
		order        string
		recurrence   string
		days         domain.Weekdays
		startTime    *string
		startAt      *int64
		active       bool
	}
	items := make([]prepared, 0, len(s.Items))
	for _, it := range s.Items {
		p := prepared{recurrence: it.RecurrenceType, startTime: nil, startAt: it.StartAt, active: true}
		if it.Active != nil {
			p.active = *it.Active
		}
		switch {
		case it.Block != "" && it.Collection != "":
			i.report.add("schedule", s.Name, "rejected", "a schedule item takes a block or a collection, not both")
			return nil
		case it.Block != "":
			id, ok := i.ids["block"][it.Block]
			if !ok {
				i.report.add("schedule", s.Name, "rejected", "block "+it.Block+" is not in the plan and does not exist")
				return nil
			}
			p.blockID = id
		case it.Collection != "":
			id, ok := i.ids["collection"][it.Collection]
			if !ok {
				i.report.add("schedule", s.Name, "rejected", "collection "+it.Collection+" is not in the plan and does not exist")
				return nil
			}
			p.collectionID = id
			if !domain.ValidCriteriaOrder(it.PlaybackOrder) {
				i.report.add("schedule", s.Name, "rejected", "a collection item needs playback_order (title, release_date, random, shuffle, interleaved)")
				return nil
			}
			p.order = it.PlaybackOrder
		default:
			i.report.add("schedule", s.Name, "rejected", "a schedule item needs block or collection")
			return nil
		}
		switch domain.RecurrenceType(it.RecurrenceType) {
		case domain.RecurrenceWeekly:
			days, err := domain.NormalizeWeekdays(it.DaysOfWeek)
			if err != nil {
				i.report.add("schedule", s.Name, "rejected", err.Error())
				return nil
			}
			if it.StartTimeLocal == "" {
				i.report.add("schedule", s.Name, "rejected", "start_time_local is required for weekly recurrence")
				return nil
			}
			p.days = days
			local := it.StartTimeLocal
			p.startTime = &local
			p.startAt = nil
		case domain.RecurrenceOnce:
			if it.StartAt == nil {
				i.report.add("schedule", s.Name, "rejected", "start_at is required for once recurrence")
				return nil
			}
		case domain.RecurrenceContinuous:
			p.startAt = nil
		default:
			i.report.add("schedule", s.Name, "rejected", "recurrence_type must be one of weekly, once, continuous")
			return nil
		}
		items = append(items, p)
	}
	if len(items) > 0 {
		continuous := 0
		for _, p := range items {
			if p.recurrence == string(domain.RecurrenceContinuous) {
				continuous++
			}
		}
		if continuous > 1 {
			i.report.add("schedule", s.Name, "rejected", errSecondContinuous)
			return nil
		}
	}

	action, skip := i.decide("schedule", s.Name)
	if skip {
		i.report.add("schedule", s.Name, action, "")
		return nil
	}
	if i.dryRun {
		// Keep an id a previous pass resolved (an object that already exists):
		// the placeholder is only for one this run would have created.
		if _, ok := i.ids["schedule"][s.Name]; !ok {
			i.ids["schedule"][s.Name] = "dry-run:" + s.Name
		}
		i.report.add("schedule", s.Name, action, "")
		return nil
	}
	now := time.Now().UnixMilli()
	id := i.exists["schedule"][s.Name]
	if id == "" {
		id = uuid.NewString()
		if err := i.db.Exec(i.ctx,
			"INSERT INTO schedule (id, tenant_id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			id, DefaultTenantID, s.Name, s.Description, now, now,
		); err != nil {
			return err
		}
	} else {
		if err := i.db.Exec(i.ctx, "UPDATE schedule SET description = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			s.Description, now, DefaultTenantID, id); err != nil {
			return err
		}
		// Replace the rules: detach the materialized rows that point at them
		// first (no ON DELETE action), like DELETE /schedule-items/{id} does.
		if err := i.db.Exec(i.ctx,
			"UPDATE epg_entry SET source_schedule_item_id = NULL WHERE source_schedule_item_id IN (SELECT id FROM schedule_item WHERE schedule_id = ?)", id,
		); err != nil {
			return err
		}
		if err := i.db.Exec(i.ctx, "DELETE FROM schedule_item WHERE tenant_id = ? AND schedule_id = ?", DefaultTenantID, id); err != nil {
			return err
		}
	}
	for _, p := range items {
		var block, collection, order any
		if p.blockID != "" {
			block = p.blockID
		}
		if p.collectionID != "" {
			collection = p.collectionID
		}
		if p.order != "" {
			order = p.order
		}
		if err := i.db.Exec(i.ctx,
			`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, collection_id, playback_order,
			                            recurrence_type, days_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), DefaultTenantID, id, block, collection, order,
			p.recurrence, p.days, p.startTime, p.startAt, p.active, now, now,
		); err != nil {
			return err
		}
	}
	i.ids["schedule"][s.Name] = id
	i.applied = true
	i.report.add("schedule", s.Name, action, "")
	return nil
}

func (i *planImporter) applyChannel(c PlanChannel) error {
	if c.Name == "" {
		i.report.add("channel", fmt.Sprint(c.Number), "rejected", "name is required")
		return nil
	}
	tz := c.Timezone
	if tz == "" {
		tz = "UTC"
	}
	var scheduleID, baseScheduleID *string
	if c.Schedule != "" {
		id, ok := i.ids["schedule"][c.Schedule]
		if !ok {
			i.report.add("channel", fmt.Sprint(c.Number), "rejected", "schedule "+c.Schedule+" is not in the plan and does not exist")
			return nil
		}
		scheduleID = &id
	}
	if c.BaseSchedule != "" {
		id, ok := i.ids["schedule"][c.BaseSchedule]
		if !ok {
			i.report.add("channel", fmt.Sprint(c.Number), "rejected", "base schedule "+c.BaseSchedule+" is not in the plan and does not exist")
			return nil
		}
		if scheduleID != nil && *scheduleID == id {
			i.report.add("channel", fmt.Sprint(c.Number), "rejected", "base_schedule must differ from schedule")
			return nil
		}
		baseScheduleID = &id
	}
	key := fmt.Sprint(c.Number)
	action, skip := i.decide("channel", key)
	if skip {
		i.report.add("channel", key, action, "")
		return nil
	}
	if i.dryRun {
		i.report.add("channel", key, action, "")
		return nil
	}
	now := time.Now().UnixMilli()
	if id := i.exists["channel"][key]; id != "" {
		if err := i.db.Exec(i.ctx,
			`UPDATE channel SET name = ?, guide_art_url = ?, timezone = ?, schedule_id = ?, base_schedule_id = ?, updated_at = ?
			 WHERE tenant_id = ? AND id = ?`,
			c.Name, c.GuideArtURL, tz, scheduleID, baseScheduleID, now, DefaultTenantID, id,
		); err != nil {
			return err
		}
	} else if err := i.db.Exec(i.ctx,
		`INSERT INTO channel (id, tenant_id, number, name, guide_art_url, timezone, schedule_id, base_schedule_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), DefaultTenantID, c.Number, c.Name, c.GuideArtURL, tz, scheduleID, baseScheduleID, now, now,
	); err != nil {
		return err
	}
	i.applied = true
	i.touched[key] = true
	i.report.add("channel", key, action, "")
	return nil
}

// resetChannels regenerates the guide of every channel the import created or
// changed (a plan that only touched blocks or schedules of an existing channel
// still has to reach the channels using them).
func (i *planImporter) resetChannels(mat *materializer.Materializer) {
	var channels []domain.Channel
	if err := i.db.Query(i.ctx, &channels,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND (schedule_id IS NOT NULL OR base_schedule_id IS NOT NULL)",
		DefaultTenantID,
	); err != nil {
		return
	}
	for _, ch := range channels {
		mat.ResetAfterChannelEdit(i.ctx, DefaultTenantID, ch)
	}
}
