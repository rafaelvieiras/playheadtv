import { Link } from 'react-router-dom'

import type { CollectionUsage } from '@/lib/types'

// Who uses a collection: the dynamic blocks and filler pools that reference it
// and the criteria items of manual blocks, each linking to its editor.
export function CollectionUsageList({ usage, testId = 'collection-usage' }: { usage: CollectionUsage; testId?: string }) {
  const empty = usage.blocks.length + usage.filler_pools.length + usage.block_items.length === 0
  if (empty) {
    return (
      <p className="text-sm text-muted-foreground" data-testid={testId}>
        Nothing uses this collection yet.
      </p>
    )
  }
  return (
    <ul className="flex flex-col gap-1 text-sm" data-testid={testId}>
      {usage.blocks.map((b) => (
        <li key={`b-${b.id}`} data-testid="usage-block">
          Block{' '}
          <Link to={`/blocks/${b.id}`} className="underline">
            {b.name}
          </Link>
        </li>
      ))}
      {usage.filler_pools.map((p) => (
        <li key={`p-${p.id}`} data-testid="usage-pool">
          Filler pool{' '}
          <Link to={`/filler-pools/${p.id}`} className="underline">
            {p.name}
          </Link>
        </li>
      ))}
      {usage.block_items.map((i) => (
        <li key={`i-${i.id}`} data-testid="usage-item">
          Criteria item {i.position + 1} of block{' '}
          <Link to={`/blocks/${i.block_id}`} className="underline">
            {i.block_name}
          </Link>
        </li>
      ))}
    </ul>
  )
}
