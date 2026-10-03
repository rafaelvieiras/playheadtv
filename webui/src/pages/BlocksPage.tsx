import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { BlockDialog } from '@/components/BlockDialog'
import { api } from '@/lib/http'
import { useListModeReview } from '@/lib/authoring'
import { InlineWarning } from '@/components/InlineWarning'
import { LIST_MODE_LABELS, type Block } from '@/lib/types'

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
  const queryClient = useQueryClient()
  const { data: blocks, isLoading, isError } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
  })

  const { data: review } = useListModeReview()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [deletingBlock, setDeletingBlock] = useState<Block | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/blocks/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('Block deleted.')
      setDeletingBlock(undefined)
    },
    onError: () => {
      toast.error('Could not delete the block.')
    },
  })

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

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load blocks.</p>}

      {blocks && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>List</TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {blocks.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
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
                <TableCell className="max-w-96 truncate text-muted-foreground">
                  {block.description ?? '—'}
                </TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/blocks/${block.id}`}>Edit</Link>
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeletingBlock(block)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <BlockDialog open={dialogOpen} onOpenChange={setDialogOpen} />

      <AlertDialog open={deletingBlock !== undefined} onOpenChange={(open) => !open && setDeletingBlock(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete block?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete the block "{deletingBlock?.name}" and its items. This action
              cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingBlock && deleteMutation.mutate(deletingBlock.id)}
              disabled={deleteMutation.isPending}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
