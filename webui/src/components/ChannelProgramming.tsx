import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { InlineWarning } from '@/components/InlineWarning'
import { ScheduleItemDialog } from '@/components/ScheduleItemDialog'
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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { api } from '@/lib/http'
import {
  groupItemsByKind,
  legacyDailyDynamicBlock,
  parseBlockCriteria,
  summarizeCriteria,
  summarizeRecurrence,
} from '@/lib/schedule'
import type { Block, BlockItem, Channel, Schedule, ScheduleItem } from '@/lib/types'

// Programming tab: the items of the channel's schedule, grouped by kind and
// ordered by time, each showing the block that feeds it and how it is filled.
export function ChannelProgramming({
  channel,
  channels,
  onGoToSettings,
}: {
  channel: Channel
  channels: Channel[]
  onGoToSettings: () => void
}) {
  const queryClient = useQueryClient()
  const scheduleId = channel.schedule_id

  const { data: schedule } = useQuery({
    queryKey: ['schedules', scheduleId],
    queryFn: () => api.get<Schedule>(`/schedules/${scheduleId}`),
    enabled: !!scheduleId,
  })
  const { data: items, isLoading, isError } = useQuery({
    queryKey: ['schedule-items', scheduleId],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${scheduleId}/items`),
    enabled: !!scheduleId,
  })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const blocksById = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b])), [blocks])

  // Item counts of the manual blocks in use (dynamic ones show their criteria).
  const manualBlockIds = useMemo(() => {
    const ids = new Set<string>()
    for (const item of items ?? []) {
      const block = blocksById.get(item.block_id)
      if (block && !block.criteria) ids.add(block.id)
    }
    return [...ids]
  }, [items, blocksById])
  const itemCounts = useQueries({
    queries: manualBlockIds.map((id) => ({
      queryKey: ['blocks', id, 'items'],
      queryFn: () => api.get<BlockItem[]>(`/blocks/${id}/items`),
    })),
  })
  const countByBlock = new Map<string, number>()
  manualBlockIds.forEach((id, i) => {
    const data = itemCounts[i]?.data
    if (data) countByBlock.set(id, data.length)
  })

  const sharedWith = scheduleId ? channels.filter((c) => c.schedule_id === scheduleId && c.id !== channel.id) : []

  const [itemDialogOpen, setItemDialogOpen] = useState(false)
  const [editingItem, setEditingItem] = useState<ScheduleItem | undefined>(undefined)
  const [deletingItem, setDeletingItem] = useState<ScheduleItem | undefined>(undefined)
  const [convertOpen, setConvertOpen] = useState(false)

  function invalidateItems() {
    queryClient.invalidateQueries({ queryKey: ['schedule-items', scheduleId] })
    queryClient.invalidateQueries({ queryKey: ['schedules', scheduleId, 'items'] })
  }

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedule-items/${id}`),
    onSuccess: () => {
      invalidateItems()
      toast.success('Schedule item deleted.')
      setDeletingItem(undefined)
    },
    onError: () => toast.error('Could not delete the schedule item.'),
  })

  const legacyBlockId = useMemo(
    () => (items ? legacyDailyDynamicBlock(items, blocksById) : undefined),
    [items, blocksById],
  )
  // Creates the continuous item first, then drops the seven weekly ones, so a
  // failure midway never leaves the schedule empty.
  const convertMutation = useMutation({
    mutationFn: async () => {
      if (!legacyBlockId || !items) return
      await api.post(`/schedules/${scheduleId}/items`, {
        block_id: legacyBlockId,
        recurrence_type: 'continuous',
        day_of_week: null,
        start_time_local: null,
        start_at: null,
        active: true,
      })
      for (const item of items) {
        await api.delete(`/schedule-items/${item.id}`)
      }
    },
    onSuccess: () => {
      toast.success('Schedule converted to a continuous item.')
      setConvertOpen(false)
    },
    onError: () => toast.error('Could not convert the schedule.'),
    onSettled: invalidateItems,
  })

  if (!scheduleId) {
    return (
      <div className="flex flex-col items-start gap-3" data-testid="programming-no-schedule">
        <p className="text-sm text-muted-foreground">
          This channel has no schedule yet. Assign or create one in Settings to program it.
        </p>
        <Button variant="outline" size="sm" onClick={onGoToSettings}>
          Go to Settings
        </Button>
      </div>
    )
  }

  const groups = groupItemsByKind(items ?? [])
  const sections: { key: string; title: string; rows: ScheduleItem[] }[] = [
    { key: 'continuous', title: 'Continuous', rows: groups.continuous },
    { key: 'weekly', title: 'Weekly', rows: groups.weekly },
    { key: 'once', title: 'Once', rows: groups.once },
  ]
  const hasOtherContinuous = (items ?? []).some(
    (i) => i.recurrence_type === 'continuous' && i.id !== editingItem?.id,
  )

  function openCreate() {
    setEditingItem(undefined)
    setItemDialogOpen(true)
  }
  function openEdit(item: ScheduleItem) {
    setEditingItem(item)
    setItemDialogOpen(true)
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground" data-testid="programming-schedule-name">
          Schedule: <span className="font-medium text-foreground">{schedule?.name ?? '…'}</span>
        </p>
        <Button size="sm" onClick={openCreate} data-testid="new-schedule-item">
          New item
        </Button>
      </div>

      {sharedWith.length > 0 && (
        <InlineWarning title="Shared schedule">
          <span data-testid="shared-schedule-banner">
            This schedule also drives{' '}
            {sharedWith.map((c, i) => (
              <span key={c.id}>
                {i > 0 && ', '}
                <Link to={`/channels/${c.id}`} className="underline">
                  {c.number} — {c.name}
                </Link>
              </span>
            ))}
            . Editing its items changes all of these channels, and resets the grid of each one.
          </span>
        </InlineWarning>
      )}

      {legacyBlockId && (
        <div
          data-testid="legacy-daily-banner"
          className="flex items-center justify-between gap-3 rounded-md border p-3 text-sm"
        >
          <span>
            This schedule has seven 00:00 items (one per weekday) for the same dynamic block. A single continuous item
            does the same without restarting the rotation every day.
          </span>
          <Button size="sm" variant="outline" onClick={() => setConvertOpen(true)}>
            Convert to continuous
          </Button>
        </div>
      )}

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load the schedule items.</p>}

      {items && items.length === 0 && (
        <p className="text-sm text-muted-foreground" data-testid="programming-empty">
          No items in this schedule yet.
        </p>
      )}

      {items &&
        sections
          .filter((s) => s.rows.length > 0)
          .map((section) => (
            <section key={section.key} className="flex flex-col gap-2" data-testid={`items-${section.key}`}>
              <h2 className="text-sm font-medium">{section.title}</h2>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>When</TableHead>
                    <TableHead>Block</TableHead>
                    <TableHead>Filled by</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {section.rows.map((item) => {
                    const block = blocksById.get(item.block_id)
                    const criteria = block ? parseBlockCriteria(block) : null
                    const count = countByBlock.get(item.block_id)
                    return (
                      <TableRow key={item.id} data-testid="schedule-item-row">
                        <TableCell>{summarizeRecurrence(item)}</TableCell>
                        <TableCell>
                          {block ? (
                            <Link to={`/blocks/${block.id}`} className="hover:underline" data-testid="item-block-link">
                              {block.name}
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">{item.block_id}</span>
                          )}
                        </TableCell>
                        <TableCell data-testid="item-fill">
                          {!block ? (
                            '—'
                          ) : criteria ? (
                            <span>
                              <Badge variant="default">Dynamic</Badge>{' '}
                              <span className="text-muted-foreground">{summarizeCriteria(criteria)}</span>
                            </span>
                          ) : (
                            <span>
                              <Badge variant="secondary">Manual</Badge>{' '}
                              {count === undefined ? (
                                <span className="text-muted-foreground">…</span>
                              ) : count === 0 ? (
                                <span className="text-destructive">0 items (empty block)</span>
                              ) : (
                                <span className="text-muted-foreground">
                                  {count} item{count === 1 ? '' : 's'}
                                </span>
                              )}
                            </span>
                          )}
                        </TableCell>
                        <TableCell>
                          {item.active ? (
                            <span className="text-sm text-primary">Active</span>
                          ) : (
                            <span className="text-sm text-muted-foreground">Inactive</span>
                          )}
                        </TableCell>
                        <TableCell className="flex justify-end gap-2 text-right">
                          {block && (
                            <Button asChild variant="outline" size="sm">
                              <Link to={`/blocks/${block.id}`}>Edit block</Link>
                            </Button>
                          )}
                          <Button variant="outline" size="sm" onClick={() => openEdit(item)}>
                            Edit
                          </Button>
                          <Button variant="destructive" size="sm" onClick={() => setDeletingItem(item)}>
                            Delete
                          </Button>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </section>
          ))}

      <ScheduleItemDialog
        open={itemDialogOpen}
        onOpenChange={setItemDialogOpen}
        scheduleId={scheduleId}
        item={editingItem}
        hasOtherContinuous={hasOtherContinuous}
      />

      <AlertDialog open={convertOpen} onOpenChange={setConvertOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Convert to a continuous item?</AlertDialogTitle>
            <AlertDialogDescription>
              The seven weekly 00:00 items will be replaced by one continuous item for the same block. The grid
              already published is kept; the change applies to what is generated after the freeze margin.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                convertMutation.mutate()
              }}
              disabled={convertMutation.isPending}
            >
              Convert
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={deletingItem !== undefined} onOpenChange={(o) => !o && setDeletingItem(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete schedule item?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete this schedule item
              {sharedWith.length > 0 ? ' for every channel sharing this schedule' : ''}. This action cannot be
              undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingItem && deleteMutation.mutate(deletingItem.id)}
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
