package domain

import "encoding/json"

// Collection is a named, reusable catalog query (issue #5, stage 2): the
// condition tree (catalogsync.Query) and nothing else. Dynamic blocks, filler
// pools and criteria block items reference it by id; how its matches are
// played (order, amount, interleave, seed) stays on the referencing block or
// item, so one collection can be played differently by different users.
type Collection struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Query is the stored condition tree JSON; internal, clients read Tree.
	Query string `json:"-"`
	// Tree is Query as a decoded JSON value (not a RawMessage, so the MCP
	// output schema accepts it), filled by Fill.
	Tree any `json:"query"`
	// UsageCount is how many blocks, filler pools and criteria items use the
	// collection (set by the list/get queries).
	UsageCount int   `json:"usage_count"`
	CreatedAt  int64 `json:"created_at"`
	UpdatedAt  int64 `json:"updated_at"`
}

// Fill decodes Query into Tree.
func (c *Collection) Fill() {
	c.Tree = nil
	_ = json.Unmarshal([]byte(c.Query), &c.Tree)
}

// UsageRef names one block or filler pool that uses a collection.
type UsageRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BlockItemUsage identifies one criteria block item that uses a collection.
type BlockItemUsage struct {
	ID        string `json:"id"`
	BlockID   string `json:"block_id"`
	BlockName string `json:"block_name"`
	Position  int    `json:"position"`
}

// CollectionUsage lists everything that references a collection.
type CollectionUsage struct {
	Blocks      []UsageRef       `json:"blocks"`
	FillerPools []UsageRef       `json:"filler_pools"`
	BlockItems  []BlockItemUsage `json:"block_items"`
}

// Count is the number of users.
func (u CollectionUsage) Count() int { return len(u.Blocks) + len(u.FillerPools) + len(u.BlockItems) }
