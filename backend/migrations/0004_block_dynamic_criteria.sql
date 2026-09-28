-- block gains the same optional dynamic-membership rule filler_pool already
-- has (migration 0003). NULL keeps today's behavior (a manually managed
-- ordered list via block_item). Non-NULL makes catalogsync.ResolveDynamicBlocks
-- own block_item for this block, recomputed after every catalog sync — see
-- internal/catalogsync/criteria.go and internal/materializer/expand.go's
-- looping-block behavior for a dynamic block.
ALTER TABLE block ADD COLUMN criteria TEXT;
