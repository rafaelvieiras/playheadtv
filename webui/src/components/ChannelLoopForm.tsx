import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

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
export function ChannelLoopForm({ channel }: { channel: Channel }) {
  const queryClient = useQueryClient()
  const [collectionId, setCollectionId] = useState('')
  const [order, setOrder] = useState<CriteriaOrder>('shuffle')
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
      toast.success('Collection looped. The schedule it created can be edited in Settings.')
    },
    onError: () => toast.error('Could not loop the collection.'),
  })

  return (
    <div className="flex flex-wrap items-end gap-3 rounded-md border p-3" data-testid="channel-loop-form">
      <label className="flex flex-col gap-1 text-sm">
        <span className="text-muted-foreground">Collection</span>
        <select
          className="h-9 min-w-56 rounded-md border bg-background px-2"
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
          className="h-9 rounded-md border bg-background px-2"
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
        onClick={() => loopMutation.mutate()}
        data-testid="loop-submit"
      >
        Loop 24/7
      </Button>
    </div>
  )
}
