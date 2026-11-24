import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { InlineWarning } from '@/components/InlineWarning'
import { ApiError, api } from '@/lib/http'
import type { Block } from '@/lib/types'

// How a block is scheduled, read along on GET /blocks and GET /blocks/{id}
// (domain.Block.UsageItems/UsageSchedules): how many schedule items air the
// block and across how many distinct schedules. The counts do not come with the
// schedule names: the 409 on delete is what names them, so it is kept as the
// explanation for the case the count below is out of date.
export interface BlockUsage {
  usage_items?: number
  usage_schedules?: number
}

function plural(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`
}

// Confirms deleting a block. A block a schedule still airs cannot be deleted
// (the server answers 409 and names the schedules): the usage is read fresh
// when the dialog opens, the action is disabled while it is non-zero, and the
// server's sentence is shown in place if a delete is attempted anyway (a
// schedule that started using the block after this read).
export function DeleteBlockDialog({
  block,
  onClose,
  onDeleted,
}: {
  block: (Block & BlockUsage) | undefined
  onClose: () => void
  onDeleted?: () => void
}) {
  const queryClient = useQueryClient()
  const [error, setError] = useState<string | undefined>(undefined)
  // A refusal belongs to the block it was read for, and a reopen deserves a
  // clean slate: dropping it here is the render-time equivalent of resetting it
  // in an effect, without the extra render.
  const [stateFor, setStateFor] = useState<string | undefined>(undefined)
  const blockId = block?.id
  if (blockId !== stateFor) {
    setStateFor(blockId)
    setError(undefined)
  }

  const usage = useQuery({
    queryKey: ['blocks', blockId ?? '', 'usage'],
    queryFn: () => api.get<Block & BlockUsage>(`/blocks/${blockId ?? ''}`),
    enabled: blockId !== undefined,
    // Always fresh: this read decides whether the action is offered at all.
    staleTime: 0,
    gcTime: 0,
  })

  // The list already carried the counts, so the dialog is right on the first
  // paint and only sharpens when the fresh read lands.
  const counts = usage.data ?? block
  const items = counts?.usage_items ?? 0
  const schedules = counts?.usage_schedules ?? 0
  const inUse = items > 0

  const del = useMutation({
    mutationFn: (id: string) => api.delete(`/blocks/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('Block deleted.')
      onClose()
      onDeleted?.()
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : 'Could not delete the block.')
      void usage.refetch()
    },
  })

  return (
    <ConfirmDialog
      open={block !== undefined}
      onClose={onClose}
      title={`Delete block "${block?.name}"?`}
      description="The block and its items are deleted. This cannot be undone."
      confirmLabel="Delete block"
      pendingLabel="Deleting…"
      onConfirm={() => block && del.mutate(block.id)}
      isPending={del.isPending}
      // The button is really disabled while a schedule airs the block: it used
      // to look available and only the failure explained why it was not.
      confirmDisabled={inUse}
      error={error}
      testId="delete-block-dialog"
    >
      {usage.isError && (
        <InlineWarning title="Could not check where this block is scheduled">
          A schedule that still airs it makes the backend refuse the delete, and it names those schedules when it
          does. Reopen this dialog to try the check again.
        </InlineWarning>
      )}
      {inUse && (
        <InlineWarning title="This block is scheduled and cannot be deleted">
          It airs in {plural(items, 'schedule item', 'schedule items')} across{' '}
          {plural(schedules, 'schedule', 'schedules')}. Remove it from that schedule first, on the channel that uses it
          ({' '}
          <Link to="/channels" className="underline">
            Channels
          </Link>
          ), then delete the block.
        </InlineWarning>
      )}
    </ConfirmDialog>
  )
}
