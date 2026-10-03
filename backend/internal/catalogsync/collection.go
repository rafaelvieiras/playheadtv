package catalogsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// ErrCollectionNotFound is returned when a collection id does not exist for
// the tenant (a collection of another tenant is indistinguishable from a
// missing one, on purpose).
var ErrCollectionNotFound = errors.New("collection not found")

// DynamicSQL is the predicate for "this block or filler pool is dynamic": it
// references a collection, or it is a legacy row still held for review that
// keeps playing its deprecated criteria column (collection_id NULL).
const DynamicSQL = "(collection_id IS NOT NULL OR criteria IS NOT NULL)"

// CollectionColumns is the SELECT list that scans into domain.Collection,
// including how many blocks, filler pools and criteria items use it.
const CollectionColumns = `id, tenant_id, name, description, query, created_at, updated_at,
	(SELECT COUNT(*) FROM block b WHERE b.tenant_id = collection.tenant_id AND b.collection_id = collection.id)
	+ (SELECT COUNT(*) FROM filler_pool p WHERE p.tenant_id = collection.tenant_id AND p.collection_id = collection.id)
	+ (SELECT COUNT(*) FROM block_item bi JOIN block bb ON bb.id = bi.block_id
	    WHERE bb.tenant_id = collection.tenant_id AND bi.item_type = 'criteria' AND json_extract(bi.spec, '$.collection_id') = collection.id) AS usage_count`

// GetCollection loads one collection of tenantID (ErrCollectionNotFound when
// there is none).
func GetCollection(ctx context.Context, db storage.DB, tenantID, id string) (domain.Collection, error) {
	var c domain.Collection
	if err := db.Query(ctx, &c, "SELECT "+CollectionColumns+" FROM collection WHERE tenant_id = ? AND id = ?", tenantID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrCollectionNotFound
		}
		return c, err
	}
	c.Fill()
	return c, nil
}

// LoadCollectionQuery returns the parsed query of a collection of tenantID.
func LoadCollectionQuery(ctx context.Context, db storage.DB, tenantID, id string) (*Query, error) {
	var raw []string
	if err := db.Query(ctx, &raw, "SELECT query FROM collection WHERE tenant_id = ? AND id = ?", tenantID, id); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, ErrCollectionNotFound
	}
	q, err := ParseQuery([]byte(raw[0]))
	if err != nil {
		return nil, fmt.Errorf("collection %s holds an invalid query: %w", id, err)
	}
	return q, nil
}

// CollectionUsage lists every block, filler pool and criteria block item of
// tenantID that references the collection.
func CollectionUsage(ctx context.Context, db storage.DB, tenantID, id string) (domain.CollectionUsage, error) {
	u := domain.CollectionUsage{Blocks: []domain.UsageRef{}, FillerPools: []domain.UsageRef{}, BlockItems: []domain.BlockItemUsage{}}
	if err := db.Query(ctx, &u.Blocks, "SELECT id, name FROM block WHERE tenant_id = ? AND collection_id = ? ORDER BY name, id", tenantID, id); err != nil {
		return u, err
	}
	if err := db.Query(ctx, &u.FillerPools, "SELECT id, name FROM filler_pool WHERE tenant_id = ? AND collection_id = ? ORDER BY name, id", tenantID, id); err != nil {
		return u, err
	}
	if err := db.Query(ctx, &u.BlockItems,
		`SELECT bi.id AS id, bi.block_id AS block_id, b.name AS block_name, bi.position AS position
		   FROM block_item bi JOIN block b ON b.id = bi.block_id
		  WHERE b.tenant_id = ? AND bi.item_type = 'criteria' AND json_extract(bi.spec, '$.collection_id') = ?
		  ORDER BY b.name, bi.position`, tenantID, id); err != nil {
		return u, err
	}
	return u, nil
}

// FindOrCreateCollection returns the id of the tenant's collection whose
// canonical query equals q's, creating one named name when there is none
// (created reports that). It is how migration and "apply proposal" share one
// collection between owners with identical trees. The earliest collection
// (by creation time, then id) wins when several already match.
func FindOrCreateCollection(ctx context.Context, db storage.DB, tenantID, name string, description *string, q *Query, now int64) (id string, created bool, err error) {
	canon := q.JSON()
	var ids []string
	if err := db.Query(ctx, &ids, "SELECT id FROM collection WHERE tenant_id = ? AND query = ? ORDER BY created_at, id LIMIT 1", tenantID, canon); err != nil {
		return "", false, err
	}
	if len(ids) > 0 {
		return ids[0], false, nil
	}
	id = uuid.NewString()
	if err := db.Exec(ctx,
		"INSERT INTO collection (id, tenant_id, name, description, query, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, tenantID, name, description, canon, now, now,
	); err != nil {
		return "", false, err
	}
	return id, true, nil
}
