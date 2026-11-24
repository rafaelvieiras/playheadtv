import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { BlockDialog } from '@/components/BlockDialog'
import { DeleteBlockDialog, type BlockUsage } from '@/components/DeleteBlockDialog'
import { Loading, QueryError } from '@/components/QueryError'
import { api } from '@/lib/http'
import { useListModeReview } from '@/lib/authoring'
import { InlineWarning } from '@/components/InlineWarning'
import { LIST_MODE_LABELS, type Block } from '@/lib/types'

// A block as the list reads it: the row plus the two usage counts the backend
// adds to every block (see DeleteBlockDialog).
type BlockRow = Block & BlockUsage

// How a block is scheduled, in the list: the delete is refused while a
// schedule airs it, so the answer has to be visible before the button.
function UsageCell({ block }: { block: BlockRow }) {
  const items = block.usage_items ?? 0
  const schedules = block.usage_schedules ?? 0
  return (
    // Two short lines, not one long one: the table already scrolls sideways.
    <span className="flex flex-col leading-tight" data-testid={`block-usage-${block.id}`}>
      {items === 0 ? (
        <span>Not scheduled</span>
      ) : (
        <>
          <span>
            {items} schedule {items === 1 ? 'item' : 'items'}
          </span>
          <span className="text-xs">
            in {schedules} {schedules === 1 ? 'schedule' : 'schedules'}
          </span>
        </>
      )}
    </span>
  )
}

function DynamicBadge({ dynamic }: { dynamic: boolean }) {
  if (dynamic) {
    return (
      <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
        Dynamic
      </span>
    )
  }
  return (
    <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
      Manual
    </span>
  )
}

export function BlocksPage() {
  const {
    data: blocks,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<BlockRow[]>('/blocks'),
  })

  const { data: review } = useListModeReview()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [deletingBlock, setDeletingBlock] = useState<BlockRow | undefined>(undefined)

  function openCreateDialog() {
    setDialogOpen(true)
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Blocks</h1>
        <Button onClick={openCreateDialog}>New block</Button>
      </div>

      {review && review.length > 0 && (
        <InlineWarning title="Some scheduled blocks now repeat their list">
          <span data-testid="list-mode-review-banner">
            Manual blocks used to play their list once and now repeat it until the next timed item. These are scheduled
            at fixed times, so their grid may have changed. Open each one and choose "Play once" if it should not repeat:{' '}
            {review.map((r, i) => (
              <span key={r.block_id}>
                {i > 0 && ', '}
                <Link to={`/blocks/${r.block_id}`} className="underline" data-testid={`list-mode-review-link-${r.block_id}`}>
                  {r.block_name}
                </Link>
              </span>
            ))}
            .
          </span>
        </InlineWarning>
      )}

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the blocks"
          error={error}
          onRetry={() => void refetch()}
          stale={blocks !== undefined}
        />
      )}

      {blocks && (
        <Table label="Blocks">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>List</TableHead>
              <TableHead>Used by</TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {blocks.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  No blocks yet.
                </TableCell>
              </TableRow>
            )}
            {blocks.map((block) => (
              <TableRow key={block.id}>
                <TableCell>
                  <Link to={`/blocks/${block.id}`} className="hover:underline">
                    {block.name}
                  </Link>
                </TableCell>
                <TableCell>
                  <DynamicBadge dynamic={block.dynamic} />
                </TableCell>
                <TableCell className="text-muted-foreground" data-testid={`block-list-mode-${block.id}`}>
                  {block.list_mode ? LIST_MODE_LABELS[block.list_mode] : '—'}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  <UsageCell block={block} />
                </TableCell>
                {/* Narrower than the old max-w-96: the usage column needs the room. */}
                <TableCell className="max-w-64 truncate text-muted-foreground">
                  {block.description ?? '—'}
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    <Button asChild variant="outline" size="sm">
                      <Link to={`/blocks/${block.id}`}>Edit</Link>
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      aria-label={`Delete ${block.name}`}
                      onClick={() => setDeletingBlock(block)}
                    >
                      Delete
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <BlockDialog open={dialogOpen} onOpenChange={setDialogOpen} />

      <DeleteBlockDialog block={deletingBlock} onClose={() => setDeletingBlock(undefined)} />
    </div>
  )
}
