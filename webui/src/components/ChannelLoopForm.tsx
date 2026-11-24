import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/http'
import { CRITERIA_ORDER_LABELS, type Channel, type Collection, type CriteriaOrder } from '@/lib/types'

// Orders that make sense for a 24/7 loop: everything but interleaved, which is
// a series rotation, not a collection loop.
const LOOP_ORDERS: CriteriaOrder[] = ['shuffle', 'random', 'title', 'release_date']

// ChannelLoopForm is issue #103's one-call shortcut: a channel that airs one
// collection, looped, without the author creating a block, a schedule and a
// schedule item by hand. It writes the same PUT /channels/{id} the Settings
// tab uses, carrying the loop object, and says where the objects it created can
// be edited afterwards.
//
// The button used to write on the first press, so the same click that picked a
// collection also created a schedule, an item and a channel assignment nobody
// asked to see. The confirmation says what those objects are before they exist.
export function ChannelLoopForm({ channel }: { channel: Channel }) {
  const queryClient = useQueryClient()
  const [collectionId, setCollectionId] = useState('')
  const [order, setOrder] = useState<CriteriaOrder>('shuffle')
  const [confirming, setConfirming] = useState(false)
  const { data: collections } = useQuery({
    queryKey: ['collections'],
    queryFn: () => api.get<Collection[]>('/collections'),
  })

  const loopMutation = useMutation({
    mutationFn: () =>
      api.put<{ channel: Channel; schedule_id: string; schedule_item_id: string }>(`/channels/${channel.id}`, {
        number: channel.number,
        name: channel.name,
        guide_art_url: channel.guide_art_url,
        timezone: channel.timezone,
        schedule_id: channel.schedule_id,
        base_schedule_id: channel.base_schedule_id,
        loop: { collection_id: collectionId, playback_order: order },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      queryClient.invalidateQueries({ queryKey: ['channels', channel.id] })
      setConfirming(false)
      toast.success('Collection looped. The schedule it created can be edited in Settings.')
    },
    // No toast on failure: the confirm dialog keeps the refusal where the click
    // was (a 409 means this channel already has a schedule).
  })

  const collectionName = collections?.find((c) => c.id === collectionId)?.name ?? 'The chosen collection'

  return (
    <div className="flex flex-wrap items-end gap-3 rounded-md border p-3" data-testid="channel-loop-form">
      <label className="flex flex-col gap-1 text-sm">
        <span className="text-muted-foreground">Collection</span>
        <select
          className="h-9 min-w-56 rounded-md border bg-background px-2 max-sm:h-11"
          value={collectionId}
          onChange={(e) => setCollectionId(e.target.value)}
          data-testid="loop-collection"
        >
          <option value="">Choose a collection…</option>
          {(collections ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1 text-sm">
        <span className="text-muted-foreground">Order</span>
        <select
          className="h-9 rounded-md border bg-background px-2 max-sm:h-11"
          value={order}
          onChange={(e) => setOrder(e.target.value as CriteriaOrder)}
          data-testid="loop-order"
        >
          {LOOP_ORDERS.map((o) => (
            <option key={o} value={o}>
              {CRITERIA_ORDER_LABELS[o]}
            </option>
          ))}
        </select>
      </label>
      <Button
        size="sm"
        disabled={!collectionId || loopMutation.isPending}
        onClick={() => setConfirming(true)}
        data-testid="loop-submit"
      >
        Loop 24/7
      </Button>

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        title={`Loop "${collectionName}" 24/7 on channel ${channel.number} "${channel.name}"?`}
        description={
          <>
            This creates a new schedule holding one continuous item that airs {collectionName} around the
            clock, in {CRITERIA_ORDER_LABELS[order].toLowerCase()} order, and assigns that schedule to this
            channel. The collection and its blocks are not changed. The schedule and its item can be edited
            in Settings afterwards.
          </>
        }
        confirmLabel="Create the loop"
        pendingLabel="Creating the loop…"
        onConfirm={() => loopMutation.mutate()}
        isPending={loopMutation.isPending}
        error={loopMutation.error}
        testId="loop-confirm"
      />
    </div>
  )
}
