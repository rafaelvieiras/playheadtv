package catalogsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// OwnerBlockItem is the review owner type of a criteria block item.
const OwnerBlockItem = "block_item"

// collectionMigrationBatch is how many owners one migration transaction
// covers (one commit per batch, see runBackfill for why).
const collectionMigrationBatch = 200

// collectionOwner is one dynamic block, filler pool or criteria block item
// still to move into a collection.
type collectionOwner struct {
	Type     string
	ID       string
	TenantID string
	// Name is the owner's name; the collection of the first owner is named
	// after it (a criteria item has no name of its own: "<block> - item N").
	Name string
	// Criteria is the owner's stored query text (the deprecated column, or the
	// embedded query of a criteria item's spec).
	Criteria string
	// Spec is a criteria item's spec JSON.
	Spec string
}

// CollectionMigrationReport summarizes one MigrateToCollections run.
type CollectionMigrationReport struct {
	// Assigned is how many owners now reference a collection after this run.
	Assigned int
	// Created is how many collections this run created (owners with identical
	// canonical trees share one, so Created <= Assigned).
	Created int
	// Held is how many owners were left as they were and recorded in
	// criteria_migration_review: their matched set would change, or the stored
	// query does not parse.
	Held int
}

// matchSig is the identity of a matched set: how many items and a digest of
// their ids in order. Comparing signatures instead of keeping every id slice
// keeps the memory of a run flat however many distinct queries there are.
type matchSig struct {
	count int
	sum   [sha256.Size]byte
}

func sigOf(ids []string) matchSig {
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	var s matchSig
	s.count = len(ids)
	copy(s.sum[:], h.Sum(nil))
	return s
}

// collectionAfterHook lets a test substitute the "after" side of the
// before/after comparison (to prove a mismatch is held, not applied). It is
// nil in production.
var collectionAfterHook func(ownerID string, ids []string) []string

// MigrateToCollections moves every dynamic block, filler pool and criteria
// block item that has no collection yet into one (issue #5, stage 2), across
// every tenant. It is idempotent, resumable and runs at startup after
// MigrateLegacyCriteria.
//
//   - Owners are taken in a stable order (tenant, name, id; blocks, then pools,
//     then criteria items). Those whose canonical condition trees are
//     identical share ONE collection, named after the first of them.
//   - For each owner the set of catalog items its stored query matches
//     ("before") is compared with what the collection's query matches
//     ("after"). Identical: the owner gets collection_id (a criteria item gets
//     it inside its spec). Any difference, or a stored query that does not
//     parse: nothing is applied, the owner keeps playing its stored query, and
//     a criteria_migration_review row records the counts and the differing
//     items, exactly like the legacy review.
//   - Legacy-format rows still held by MigrateLegacyCriteria are skipped: they
//     stay held until the proposal is applied (see ApplyReviewProposal).
//   - Writes go in transactions of collectionMigrationBatch owners, so a crash
//     loses at most the batch in flight and the next run only sees owners
//     without a collection.
//
// The deprecated criteria columns and a criteria item's embedded query are
// left untouched: they are the pre-migration snapshot, the way back.
func MigrateToCollections(ctx context.Context, db storage.DB) (CollectionMigrationReport, error) {
	var report CollectionMigrationReport
	owners, err := listCollectionOwners(ctx, db)
	if err != nil {
		return report, err
	}
	if len(owners) == 0 {
		return report, nil
	}
	start := time.Now()
	lastLog := start
	logging.Infof("catalogsync: moving the queries of %d dynamic blocks, filler pools and criteria items into collections", len(owners))

	sigs := map[string]matchSig{} // tenant + query text -> signature
	sigFor := func(tenantID, query string) (matchSig, []string, error) {
		q, err := ParseQuery([]byte(query))
		if err != nil {
			return matchSig{}, nil, err
		}
		key := tenantID + "\x00" + q.JSON()
		if s, ok := sigs[key]; ok {
			return s, nil, nil
		}
		ids, err := MatchQuery(ctx, db, tenantID, q)
		if err != nil {
			return matchSig{}, nil, err
		}
		s := sigOf(ids)
		sigs[key] = s
		return s, ids, nil
	}

	for from := 0; from < len(owners); from += collectionMigrationBatch {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		to := from + collectionMigrationBatch
		if to > len(owners) {
			to = len(owners)
		}
		type decision struct {
			owner collectionOwner
			q     *Query
		}
		var apply []decision
		// Reads (and the held rows' review writes) first, outside the batch
		// transaction: the connection is single, and a held row must not
		// depend on the batch committing.
		for _, o := range owners[from:to] {
			q, err := ParseQuery([]byte(o.Criteria))
			if err != nil {
				p := "the stored query does not parse: " + err.Error()
				if err := upsertReview(ctx, db, o.Type, legacyOwnerOf(o), nil, &p, nil, nil, nil, nil); err != nil {
					return report, fmt.Errorf("catalogsync: hold %s %s: %w", o.Type, o.ID, err)
				}
				report.Held++
				continue
			}
			// "Before": what the owner's stored query matches today. "After":
			// what the collection (created from the canonical form) will match.
			before, beforeIDs, err := sigFor(o.TenantID, o.Criteria)
			if err != nil {
				return report, fmt.Errorf("catalogsync: evaluate %s %s: %w", o.Type, o.ID, err)
			}
			after, afterIDs, err := sigFor(o.TenantID, q.JSON())
			if err != nil {
				return report, fmt.Errorf("catalogsync: evaluate %s %s: %w", o.Type, o.ID, err)
			}
			if collectionAfterHook != nil {
				if afterIDs == nil {
					if afterIDs, err = MatchQuery(ctx, db, o.TenantID, q); err != nil {
						return report, err
					}
				}
				afterIDs = collectionAfterHook(o.ID, afterIDs)
				after = sigOf(afterIDs)
			}
			if before != after {
				// Recompute the id lists only now, for the report.
				bq, _ := ParseQuery([]byte(o.Criteria))
				if beforeIDs, err = MatchQuery(ctx, db, o.TenantID, bq); err != nil {
					return report, err
				}
				if afterIDs == nil {
					if afterIDs, err = MatchQuery(ctx, db, o.TenantID, q); err != nil {
						return report, err
					}
				}
				onlyBefore, onlyAfter := diffIDs(beforeIDs, idSet(afterIDs)), diffIDs(afterIDs, idSet(beforeIDs))
				if err := upsertReview(ctx, db, o.Type, legacyOwnerOf(o), q, nil, beforeIDs, afterIDs, onlyBefore, onlyAfter); err != nil {
					return report, fmt.Errorf("catalogsync: hold %s %s: %w", o.Type, o.ID, err)
				}
				report.Held++
				continue
			}
			apply = append(apply, decision{o, q})
		}
		err := db.Transact(ctx, func(tx storage.DB) error {
			now := time.Now().UnixMilli()
			for _, d := range apply {
				desc := "Created by the collection migration from the query of " + ownerLabel(d.owner) + "."
				id, created, err := FindOrCreateCollection(ctx, tx, d.owner.TenantID, d.owner.Name, &desc, d.q, now)
				if err != nil {
					return fmt.Errorf("%s %s: %w", d.owner.Type, d.owner.ID, err)
				}
				// Read the stored collection back: what the owner will play is
				// exactly the tree that was compared above.
				var stored []string
				if err := tx.Query(ctx, &stored, "SELECT query FROM collection WHERE tenant_id = ? AND id = ?", d.owner.TenantID, id); err != nil {
					return err
				}
				if len(stored) != 1 || stored[0] != d.q.JSON() {
					return fmt.Errorf("%s %s: collection %s does not hold the compared query", d.owner.Type, d.owner.ID, id)
				}
				if err := assignCollection(ctx, tx, d.owner, id); err != nil {
					return fmt.Errorf("%s %s: %w", d.owner.Type, d.owner.ID, err)
				}
				if err := ClearCriteriaReview(ctx, tx, d.owner.TenantID, d.owner.Type, d.owner.ID); err != nil {
					return err
				}
				if created {
					report.Created++
				}
			}
			return nil
		})
		if err != nil {
			return report, fmt.Errorf("catalogsync: collection migration: %w", err)
		}
		report.Assigned += len(apply)
		if now := time.Now(); now.Sub(lastLog) >= backfillLogEvery {
			lastLog = now
			logging.Infof("catalogsync: collection migration: %d/%d owners", to, len(owners))
		}
	}
	logging.Infof("catalogsync: collection migration: %d owners now use %d new collections, %d held for review (GET /criteria-migration-review), in %s",
		report.Assigned, report.Created, report.Held, time.Since(start).Round(time.Millisecond))
	return report, nil
}

func ownerLabel(o collectionOwner) string {
	switch o.Type {
	case OwnerBlock:
		return "dynamic block \"" + o.Name + "\""
	case OwnerFillerPool:
		return "filler pool \"" + o.Name + "\""
	}
	return "criteria item \"" + o.Name + "\""
}

func legacyOwnerOf(o collectionOwner) legacyOwner {
	return legacyOwner{ID: o.ID, TenantID: o.TenantID, Name: o.Name, Criteria: o.Criteria}
}

// assignCollection points an owner at a collection (and leaves its deprecated
// query in place as the snapshot).
func assignCollection(ctx context.Context, db storage.DB, o collectionOwner, collectionID string) error {
	switch o.Type {
	case OwnerBlock, OwnerFillerPool:
		table := "block"
		if o.Type == OwnerFillerPool {
			table = "filler_pool"
		}
		return db.Exec(ctx, "UPDATE "+table+" SET collection_id = ? WHERE tenant_id = ? AND id = ?", collectionID, o.TenantID, o.ID)
	case OwnerBlockItem:
		var spec domain.CriteriaSpec
		if err := json.Unmarshal([]byte(o.Spec), &spec); err != nil {
			return err
		}
		spec.CollectionID = collectionID
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		return db.Exec(ctx, "UPDATE block_item SET spec = ? WHERE id = ?", string(b), o.ID)
	}
	return fmt.Errorf("unknown owner type %q", o.Type)
}

type collectionOwnerRow struct {
	ID        string
	TenantID  string
	Name      string
	Criteria  string
	Position  int
	BlockName string
	Spec      string
}

// listCollectionOwners lists the owners without a collection whose stored
// query is in the condition-tree format (legacy-format rows are held by
// MigrateLegacyCriteria and skipped here), in the order the collections are
// named after.
func listCollectionOwners(ctx context.Context, db storage.DB) ([]collectionOwner, error) {
	var out []collectionOwner
	for _, t := range []struct{ typ, table string }{{OwnerBlock, "block"}, {OwnerFillerPool, "filler_pool"}} {
		var rows []collectionOwnerRow
		if err := db.Query(ctx, &rows,
			"SELECT id, tenant_id, name, criteria FROM "+t.table+" WHERE collection_id IS NULL AND criteria IS NOT NULL ORDER BY tenant_id, name, id",
		); err != nil {
			return nil, fmt.Errorf("catalogsync: list %s without collection: %w", t.table, err)
		}
		for _, r := range rows {
			if IsLegacyCriteria(r.Criteria) {
				continue
			}
			out = append(out, collectionOwner{Type: t.typ, ID: r.ID, TenantID: r.TenantID, Name: r.Name, Criteria: r.Criteria})
		}
	}
	var items []collectionOwnerRow
	if err := db.Query(ctx, &items,
		`SELECT bi.id AS id, b.tenant_id AS tenant_id, b.name AS block_name, bi.position AS position, bi.spec AS spec
		   FROM block_item bi JOIN block b ON b.id = bi.block_id
		  WHERE bi.item_type = 'criteria' AND json_extract(bi.spec, '$.collection_id') IS NULL
		  ORDER BY b.tenant_id, b.name, b.id, bi.position`,
	); err != nil {
		return nil, fmt.Errorf("catalogsync: list criteria items without collection: %w", err)
	}
	for _, r := range items {
		var spec domain.CriteriaSpec
		if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
			// Not even a spec: hold it with the problem so it is reported.
			out = append(out, collectionOwner{Type: OwnerBlockItem, ID: r.ID, TenantID: r.TenantID, Name: itemName(r), Criteria: "", Spec: r.Spec})
			continue
		}
		out = append(out, collectionOwner{Type: OwnerBlockItem, ID: r.ID, TenantID: r.TenantID, Name: itemName(r), Criteria: string(spec.Query), Spec: r.Spec})
	}
	return out, nil
}

func itemName(r collectionOwnerRow) string {
	return fmt.Sprintf("%s - item %d", r.BlockName, r.Position+1)
}

// ErrNotHeld is returned by ApplyReviewProposal for an owner that is not
// waiting for review.
var ErrNotHeld = errors.New("this block, pool or item is not held for review")

// loadOwner reads one owner of tenantID that has no collection yet.
func loadOwner(ctx context.Context, db storage.DB, tenantID, ownerType, ownerID string) (collectionOwner, error) {
	switch ownerType {
	case OwnerBlock, OwnerFillerPool:
		table := "block"
		if ownerType == OwnerFillerPool {
			table = "filler_pool"
		}
		var rows []collectionOwnerRow
		if err := db.Query(ctx, &rows,
			"SELECT id, tenant_id, name, COALESCE(criteria, '') AS criteria FROM "+table+" WHERE tenant_id = ? AND id = ? AND collection_id IS NULL",
			tenantID, ownerID); err != nil {
			return collectionOwner{}, err
		}
		if len(rows) == 0 {
			return collectionOwner{}, ErrNotHeld
		}
		r := rows[0]
		return collectionOwner{Type: ownerType, ID: r.ID, TenantID: r.TenantID, Name: r.Name, Criteria: r.Criteria}, nil
	case OwnerBlockItem:
		var rows []collectionOwnerRow
		if err := db.Query(ctx, &rows,
			`SELECT bi.id AS id, b.tenant_id AS tenant_id, b.name AS block_name, bi.position AS position, COALESCE(bi.spec, '') AS spec
			   FROM block_item bi JOIN block b ON b.id = bi.block_id
			  WHERE b.tenant_id = ? AND bi.id = ? AND bi.item_type = 'criteria' AND json_extract(bi.spec, '$.collection_id') IS NULL`,
			tenantID, ownerID); err != nil {
			return collectionOwner{}, err
		}
		if len(rows) == 0 {
			return collectionOwner{}, ErrNotHeld
		}
		r := rows[0]
		var spec domain.CriteriaSpec
		_ = json.Unmarshal([]byte(r.Spec), &spec)
		return collectionOwner{Type: ownerType, ID: r.ID, TenantID: r.TenantID, Name: itemName(r), Criteria: string(spec.Query), Spec: r.Spec}, nil
	}
	return collectionOwner{}, fmt.Errorf("owner_type must be block, filler_pool or block_item")
}

// ApplyReviewProposal resolves one row held for review: it creates (or reuses,
// when the canonical tree is identical) a collection holding the proposed
// query, or override when given, and points the owner at it, then drops the
// review row. The owner's deprecated stored query stays as the snapshot. The
// caller re-resolves dynamic membership and triggers the materializer. It
// returns the collection id and whether it was created. An owner with no
// review row (or one that already uses a collection) is ErrNotHeld; a held
// legacy criteria with no translation needs an override.
func ApplyReviewProposal(ctx context.Context, db storage.DB, tenantID, ownerType, ownerID string, override json.RawMessage) (string, bool, error) {
	owner, err := loadOwner(ctx, db, tenantID, ownerType, ownerID)
	if err != nil {
		return "", false, err
	}
	var proposed []*string
	if err := db.Query(ctx, &proposed,
		"SELECT proposed_query FROM criteria_migration_review WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?",
		tenantID, ownerType, ownerID); err != nil {
		return "", false, err
	}
	if len(proposed) == 0 {
		return "", false, ErrNotHeld
	}
	var raw []byte
	switch {
	case len(override) > 0 && string(override) != "null":
		raw = override
	case proposed[0] != nil:
		raw = []byte(*proposed[0])
	default:
		return "", false, errors.New("this legacy criteria has no proposed translation: send the query to use")
	}
	q, err := ParseQuery(raw)
	if err != nil {
		return "", false, fmt.Errorf("query: %w", err)
	}
	var id string
	var created bool
	err = db.Transact(ctx, func(tx storage.DB) error {
		desc := "Created from the migration review of " + ownerLabel(owner) + "."
		var err error
		if id, created, err = FindOrCreateCollection(ctx, tx, tenantID, owner.Name, &desc, q, time.Now().UnixMilli()); err != nil {
			return err
		}
		if err := assignCollection(ctx, tx, owner, id); err != nil {
			return err
		}
		return ClearCriteriaReview(ctx, tx, tenantID, ownerType, ownerID)
	})
	return id, created, err
}
