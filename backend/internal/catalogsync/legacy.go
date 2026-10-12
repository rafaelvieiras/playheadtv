package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// legacyCriteria is the pre-tree JSON shape of block.criteria and
// filler_pool.criteria. Genres matched by OR over a case-insensitive
// substring of the comma-separated catalog_item.genres column; YearFrom and
// YearTo are an inclusive, independent range. Clients can no longer write it;
// it only exists as stored data until MigrateLegacyCriteria converts it (or
// holds it for review), and still resolves exactly as it always did meanwhile.
type legacyCriteria struct {
	Kind     string   `json:"kind"`
	Genres   []string `json:"genres,omitempty"`
	YearFrom *int     `json:"year_from,omitempty"`
	YearTo   *int     `json:"year_to,omitempty"`
}

func legacyWhere(tenantID string, c legacyCriteria) (string, []any) {
	where := "tenant_id = ? AND kind = ?"
	args := []any{tenantID, c.Kind}
	if c.YearFrom != nil {
		where += " AND production_year >= ?"
		args = append(args, *c.YearFrom)
	}
	if c.YearTo != nil {
		where += " AND production_year <= ?"
		args = append(args, *c.YearTo)
	}
	if len(c.Genres) > 0 {
		var clauses []string
		for _, g := range c.Genres {
			clauses = append(clauses, "genres LIKE ?")
			args = append(args, "%"+g+"%")
		}
		where += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	return where, args
}

func matchLegacy(ctx context.Context, db storage.DB, tenantID string, c legacyCriteria) ([]string, error) {
	where, args := legacyWhere(tenantID, c)
	var ids []string
	if err := db.Query(ctx, &ids, "SELECT id FROM catalog_item WHERE "+where+" ORDER BY title, id", args...); err != nil {
		return nil, fmt.Errorf("query matching catalog_item: %w", err)
	}
	return ids, nil
}

// legacyToQuery translates a legacy criteria into the condition tree that
// should mean the same thing: kind equals, the genre list as one "genre in"
// condition (exact normalized names, no longer a substring) and the year
// bounds. The translation is a proposal: the exact-match change of genres is
// why MigrateLegacyCriteria compares the matched sets before applying it.
func legacyToQuery(c legacyCriteria) (*Query, error) {
	conds := []map[string]any{{"field": "kind", "op": OpEquals, "value": c.Kind}}
	var genres []string
	for _, g := range c.Genres {
		if g = strings.TrimSpace(g); g != "" {
			genres = append(genres, g)
		}
	}
	switch {
	case len(genres) == 1:
		conds = append(conds, map[string]any{"field": "genre", "op": OpEquals, "value": genres[0]})
	case len(genres) > 1:
		conds = append(conds, map[string]any{"field": "genre", "op": OpIn, "value": genres})
	}
	switch {
	case c.YearFrom != nil && c.YearTo != nil:
		conds = append(conds, map[string]any{"field": "year", "op": OpBetween, "value": []int{*c.YearFrom, *c.YearTo}})
	case c.YearFrom != nil:
		conds = append(conds, map[string]any{"field": "year", "op": OpGte, "value": *c.YearFrom})
	case c.YearTo != nil:
		conds = append(conds, map[string]any{"field": "year", "op": OpLte, "value": *c.YearTo})
	}
	raw, err := json.Marshal(map[string]any{"version": QueryVersion, "all": conds})
	if err != nil {
		return nil, err
	}
	return ParseQuery(raw)
}

// Owner types of a criteria migration review row.
const (
	OwnerBlock      = "block"
	OwnerFillerPool = "filler_pool"
)

// reviewSampleLimit caps how many differing items a review row lists per side.
const reviewSampleLimit = 50

// ReviewItemRef identifies one catalog item in a review difference.
type ReviewItemRef struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	SeriesTitle *string `json:"series_title"`
}

// ReviewItem is one dynamic block, pool or criteria item held for review: its
// stored query (a legacy criteria that could not be converted without changing
// what it matches, or a query that could not be moved into a collection
// unchanged) is untouched and still in use, and this row shows what the
// proposed condition tree would change. Applying it (ApplyReviewProposal, POST
// /criteria-migration-review/apply) creates or reuses a collection with the
// proposed (or an edited) query and points the owner at it.
type ReviewItem struct {
	OwnerType      string          `json:"owner_type"`
	OwnerID        string          `json:"owner_id"`
	OwnerName      string          `json:"owner_name"`
	BlockID        *string         `json:"block_id"` // the block of a block or block_item row
	LegacyCriteria any             `json:"legacy_criteria"`
	ProposedQuery  any             `json:"proposed_query"` // null when the legacy criteria cannot be translated
	Problem        *string         `json:"problem"`
	BeforeCount    int             `json:"before_count"`
	AfterCount     int             `json:"after_count"`
	OnlyBefore     []ReviewItemRef `json:"only_before"`
	OnlyAfter      []ReviewItemRef `json:"only_after"`
}

// MigrationReport summarizes one MigrateLegacyCriteria run.
type MigrationReport struct {
	Converted int
	Held      int
}

type legacyOwner struct {
	ID       string
	TenantID string
	Name     string
	Criteria string
}

// MigrateLegacyCriteria converts block.criteria and filler_pool.criteria
// values still in the legacy fixed shape into condition trees, across every
// tenant. It is idempotent and runs at startup.
//
// For each legacy value it computes the set of catalog items matched before
// (the legacy substring rule) and after (the translated tree, exact genre
// names). Identical sets: the value is rewritten. Any difference, or a value
// that cannot be translated: nothing is applied, the legacy value keeps
// working, and a criteria_migration_review row records both counts and the
// items that would change, for a human to review (for example "action"
// versus "action & adventure"). A held row is re-evaluated on every run, so
// it converts by itself once the catalog makes the sets equal.
func MigrateLegacyCriteria(ctx context.Context, db storage.DB) (MigrationReport, error) {
	var report MigrationReport
	if err := BackfillGenreLinks(ctx, db); err != nil {
		return report, err
	}
	for _, owner := range []struct{ typ, table string }{{OwnerBlock, "block"}, {OwnerFillerPool, "filler_pool"}} {
		var rows []legacyOwner
		if err := db.Query(ctx, &rows,
			"SELECT id, tenant_id, name, criteria FROM "+owner.table+" WHERE collection_id IS NULL AND criteria IS NOT NULL ORDER BY tenant_id, name, id",
		); err != nil {
			return report, fmt.Errorf("catalogsync: list %s criteria: %w", owner.table, err)
		}
		for _, row := range rows {
			if !IsLegacyCriteria(row.Criteria) {
				continue
			}
			converted, err := migrateOne(ctx, db, owner.typ, owner.table, row)
			if err != nil {
				return report, fmt.Errorf("catalogsync: migrate %s %s criteria: %w", owner.table, row.ID, err)
			}
			if converted {
				report.Converted++
			} else {
				report.Held++
			}
		}
	}
	if report.Converted+report.Held > 0 {
		logging.Infof("catalogsync: legacy criteria migration: %d converted to condition trees, %d held for review (GET /criteria-migration-review)", report.Converted, report.Held)
	}
	return report, nil
}

func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// diffIDs returns the ids of a not in b, in a's order.
func diffIDs(a []string, b map[string]bool) []string {
	var out []string
	for _, id := range a {
		if !b[id] {
			out = append(out, id)
		}
	}
	return out
}

func migrateOne(ctx context.Context, db storage.DB, ownerType, table string, row legacyOwner) (bool, error) {
	var c legacyCriteria
	var problem *string
	var proposed *Query
	if err := json.Unmarshal([]byte(row.Criteria), &c); err != nil {
		p := "the stored criteria is not valid JSON: " + err.Error()
		problem = &p
	} else if q, err := legacyToQuery(c); err != nil {
		p := "the legacy criteria cannot be translated: " + err.Error()
		problem = &p
	} else {
		proposed = q
	}

	var before []string
	if problem == nil || c.Kind != "" {
		var err error
		if before, err = matchLegacy(ctx, db, row.TenantID, c); err != nil {
			return false, err
		}
	}
	var after []string
	if proposed != nil {
		var err error
		if after, err = MatchQuery(ctx, db, row.TenantID, proposed); err != nil {
			return false, err
		}
		onlyBefore, onlyAfter := diffIDs(before, idSet(after)), diffIDs(after, idSet(before))
		if len(onlyBefore) == 0 && len(onlyAfter) == 0 {
			if err := db.Exec(ctx, "UPDATE "+table+" SET criteria = ? WHERE tenant_id = ? AND id = ?",
				proposed.JSON(), row.TenantID, row.ID); err != nil {
				return false, err
			}
			return true, ClearCriteriaReview(ctx, db, row.TenantID, ownerType, row.ID)
		}
		return false, upsertReview(ctx, db, ownerType, row, proposed, nil, before, after, onlyBefore, onlyAfter)
	}
	return false, upsertReview(ctx, db, ownerType, row, nil, problem, before, nil, before, nil)
}

func upsertReview(ctx context.Context, db storage.DB, ownerType string, row legacyOwner, proposed *Query, problem *string, before, after, onlyBefore, onlyAfter []string) error {
	refs := func(ids []string) (string, error) {
		out, err := itemRefs(ctx, db, row.TenantID, ids)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(out)
		return string(b), err
	}
	ob, err := refs(onlyBefore)
	if err != nil {
		return err
	}
	oa, err := refs(onlyAfter)
	if err != nil {
		return err
	}
	var proposedJSON *string
	if proposed != nil {
		s := proposed.JSON()
		proposedJSON = &s
	}
	return db.Exec(ctx,
		`INSERT INTO criteria_migration_review (tenant_id, owner_type, owner_id, legacy_criteria, proposed_query, problem, before_count, after_count, only_before, only_after, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tenant_id, owner_type, owner_id) DO UPDATE SET
		   legacy_criteria = excluded.legacy_criteria, proposed_query = excluded.proposed_query, problem = excluded.problem,
		   before_count = excluded.before_count, after_count = excluded.after_count,
		   only_before = excluded.only_before, only_after = excluded.only_after, created_at = excluded.created_at`,
		row.TenantID, ownerType, row.ID, row.Criteria, proposedJSON, problem,
		len(before), len(after), ob, oa, time.Now().UnixMilli(),
	)
}

// itemRefs loads id/title/series title of up to reviewSampleLimit of ids,
// sorted by title.
func itemRefs(ctx context.Context, db storage.DB, tenantID string, ids []string) ([]ReviewItemRef, error) {
	out := []ReviewItemRef{}
	if len(ids) > reviewSampleLimit {
		ids = ids[:reviewSampleLimit]
	}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{tenantID}
	for _, id := range ids {
		args = append(args, id)
	}
	if err := db.Query(ctx, &out,
		"SELECT id, title, series_title FROM catalog_item WHERE tenant_id = ? AND id IN ("+placeholders(len(ids))+") ORDER BY title, id", args...,
	); err != nil {
		return nil, err
	}
	return out, nil
}

type reviewRow struct {
	OwnerType      string
	OwnerID        string
	OwnerName      string
	BlockID        *string
	LegacyCriteria string
	ProposedQuery  *string
	Problem        *string
	BeforeCount    int
	AfterCount     int
	OnlyBefore     string
	OnlyAfter      string
}

// ListCriteriaReview returns the dynamic blocks, filler pools and criteria
// block items still held for review (a legacy criteria that cannot be
// converted without changing what it matches, or a query that could not be
// moved into a collection unchanged), ordered by owner type and name. A row
// whose owner is gone or already uses a collection is not listed.
func ListCriteriaReview(ctx context.Context, db storage.DB, tenantID string) ([]ReviewItem, error) {
	var rows []reviewRow
	const cols = "r.owner_type, r.owner_id, r.legacy_criteria, r.proposed_query, r.problem, r.before_count, r.after_count, r.only_before, r.only_after"
	if err := db.Query(ctx, &rows,
		`SELECT `+cols+`, b.name AS owner_name, b.id AS block_id
		   FROM criteria_migration_review r JOIN block b ON b.tenant_id = r.tenant_id AND b.id = r.owner_id
		  WHERE r.tenant_id = ? AND r.owner_type = 'block' AND b.collection_id IS NULL AND b.criteria = r.legacy_criteria
		 UNION ALL
		 SELECT `+cols+`, p.name AS owner_name, NULL AS block_id
		   FROM criteria_migration_review r JOIN filler_pool p ON p.tenant_id = r.tenant_id AND p.id = r.owner_id
		  WHERE r.tenant_id = ? AND r.owner_type = 'filler_pool' AND p.collection_id IS NULL AND p.criteria = r.legacy_criteria
		 UNION ALL
		 SELECT `+cols+`, b.name || ' - item ' || (bi.position + 1) AS owner_name, b.id AS block_id
		   FROM criteria_migration_review r JOIN block_item bi ON bi.id = r.owner_id JOIN block b ON b.id = bi.block_id AND b.tenant_id = r.tenant_id
		  WHERE r.tenant_id = ? AND r.owner_type = 'block_item' AND json_extract(bi.spec, '$.collection_id') IS NULL`,
		tenantID, tenantID, tenantID,
	); err != nil {
		return nil, fmt.Errorf("catalogsync: list criteria review: %w", err)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OwnerType != rows[j].OwnerType {
			return rows[i].OwnerType < rows[j].OwnerType
		}
		if rows[i].OwnerName != rows[j].OwnerName {
			return rows[i].OwnerName < rows[j].OwnerName
		}
		return rows[i].OwnerID < rows[j].OwnerID
	})
	out := make([]ReviewItem, 0, len(rows))
	for _, r := range rows {
		item := ReviewItem{
			OwnerType: r.OwnerType, OwnerID: r.OwnerID, OwnerName: r.OwnerName, BlockID: r.BlockID,
			Problem: r.Problem, BeforeCount: r.BeforeCount, AfterCount: r.AfterCount,
			OnlyBefore: []ReviewItemRef{}, OnlyAfter: []ReviewItemRef{},
		}
		if err := json.Unmarshal([]byte(r.LegacyCriteria), &item.LegacyCriteria); err != nil {
			item.LegacyCriteria = r.LegacyCriteria // not valid JSON: show it as the text it is
		}
		if r.ProposedQuery != nil {
			if err := json.Unmarshal([]byte(*r.ProposedQuery), &item.ProposedQuery); err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal([]byte(r.OnlyBefore), &item.OnlyBefore); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(r.OnlyAfter), &item.OnlyAfter); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// ClearCriteriaReview drops the review row of one block or pool, called when
// its criteria is rewritten with a condition tree or the owner is deleted.
func ClearCriteriaReview(ctx context.Context, db storage.DB, tenantID, ownerType, ownerID string) error {
	return db.Exec(ctx, "DELETE FROM criteria_migration_review WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?", tenantID, ownerType, ownerID)
}

type genreBackfillRow struct {
	ID       string
	TenantID string
	Genres   string
}

// BackfillGenreLinks creates the catalog_item_genre links of items that
// predate them (synced before migration 0009 and not synced since): the
// genre field of a query reads those links, while only the comma-separated
// genres column existed before. Safe on every startup and resumable (batched
// transactions, see runBackfill); an item whose genres column holds no names
// has no links and is looked at again on the next startup, which costs one
// cheap scan.
func BackfillGenreLinks(ctx context.Context, db storage.DB) error {
	return runBackfill(ctx, db, backfill[genreBackfillRow]{
		name: "genre links",
		where: `COALESCE(genres, '') != ''
		    AND NOT EXISTS (SELECT 1 FROM catalog_item_genre g WHERE g.tenant_id = catalog_item.tenant_id AND g.catalog_item_id = catalog_item.id)`,
		cols: "id, tenant_id, genres",
		id:   func(r genreBackfillRow) string { return r.ID },
		apply: func(ctx context.Context, tx storage.DB, r genreBackfillRow) error {
			return syncOneLink(ctx, tx, r.TenantID, r.ID, genreLinks, plainEntries(splitGenres(r.Genres), ""))
		},
	})
}

type overviewBackfillRow struct {
	ID       string
	Overview string
}

// BackfillOverviewNorm fills catalog_item.overview_norm (the folded overview
// the overview field matches against) for rows that predate the column.
// Resumable and idempotent, batched like the other backfills.
func BackfillOverviewNorm(ctx context.Context, db storage.DB) error {
	return runBackfill(ctx, db, backfill[overviewBackfillRow]{
		name:  "overview_norm",
		where: "overview IS NOT NULL AND overview_norm IS NULL",
		cols:  "id, overview",
		id:    func(r overviewBackfillRow) string { return r.ID },
		apply: func(ctx context.Context, tx storage.DB, r overviewBackfillRow) error {
			return tx.Exec(ctx, "UPDATE catalog_item SET overview_norm = ? WHERE id = ?", FoldText(r.Overview), r.ID)
		},
	})
}
