import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { InlineWarning } from '@/components/InlineWarning'
import { Button } from '@/components/ui/button'
import { useListModeReview } from '@/lib/authoring'
import { ApiError, api } from '@/lib/http'
import type { Block, BlockInput, ListMode } from '@/lib/types'

// Shown on a manual block that the repeat migration switched to repeat while it
// is scheduled at fixed times: its grid may have changed. Either button writes
// the mode explicitly, which settles the review (a rename does not).
export function ListModeReviewNotice({ block }: { block: Block }) {
  const queryClient = useQueryClient()
  const { data } = useListModeReview()
  const entry = data?.find((r) => r.block_id === block.id)

  const decide = useMutation({
    mutationFn: (list_mode: ListMode) =>
      api.put<Block>(`/blocks/${block.id}`, {
        name: block.name,
        description: block.description,
        collection_id: null,
        list_mode,
      } satisfies BlockInput),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('List mode saved.')
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : 'Could not save the list mode.'),
  })

  if (!entry) return null
  return (
    <div className="flex flex-col gap-3" data-testid="list-mode-review">
      <InlineWarning title="This block now repeats its list">
        It used to play its list once. It is scheduled at fixed times in{' '}
        {entry.schedules.map((s) => s.schedule_name).join(', ')}, so a list shorter than its slot now starts over instead of
        ending. Choose "Play once" to keep the old behavior, or keep repeating.
      </InlineWarning>
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" data-testid="list-mode-review-once" disabled={decide.isPending} onClick={() => decide.mutate('once')}>
          Play once
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          data-testid="list-mode-review-keep"
          disabled={decide.isPending}
          onClick={() => decide.mutate('repeat')}
        >
          Keep repeating
        </Button>
      </div>
    </div>
  )
}
