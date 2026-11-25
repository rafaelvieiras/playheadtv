import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { ChannelLoopForm } from '@/components/ChannelLoopForm'
import { ConfirmDialog } from '@/components/ConfirmDialog'
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
import { InlineWarning } from '@/components/InlineWarning'
import { ScheduleDialog } from '@/components/ScheduleDialog'
import { ScheduleItemDialog } from '@/components/ScheduleItemDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Loading, QueryError } from '@/components/QueryError'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { channelToInput } from '@/lib/deco'
import { tzLabel } from '@/lib/format'
import { api, ApiError } from '@/lib/http'
import {
  groupItemsByKind,
  legacyDailyDynamicBlock,
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
  const { data: items, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['schedule-items', scheduleId],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${scheduleId}/items`),
    enabled: !!scheduleId,
  })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const blocksById = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b])), [blocks])

  // Item counts of the manual blocks in use (dynamic ones show their collection).
  const manualBlockIds = useMemo(() => {
    const ids = new Set<string>()
    for (const item of items ?? []) {
      const block = blocksById.get(item.block_id ?? '')
      if (block && !block.dynamic) ids.add(block.id)
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
        days_of_week: [],
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
      <div className="flex flex-col items-start gap-4" data-testid="programming-no-schedule">
        <div className="flex flex-col gap-1">
          <p className="text-sm" data-testid="programming-no-schedule-text">
            This channel has no schedule, so nothing is programmed on it.
          </p>
          <p className="text-sm text-muted-foreground">
            A schedule is the list of blocks this channel airs, by time. Create one here and it is assigned to this
            channel, or loop one collection below to give the channel a 24/7 rotation in a single step.
          </p>
          <p className="text-xs text-muted-foreground" data-testid="programming-timezone">
            {tzLabel(channel.timezone)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <CreateScheduleButton channel={channel} />
          <Button variant="outline" size="sm" onClick={onGoToSettings} data-testid="programming-go-settings">
            Assign an existing schedule in Settings
          </Button>
        </div>
        <ChannelLoopForm channel={channel} />
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
      <div className="flex flex-wrap items-start justify-between gap-3">
        {/* The zone is part of the reading: the same instant rendered here and
            in the item dialog behind it must not differ. */}
        <div className="flex flex-col gap-0.5">
          <p className="text-sm text-muted-foreground" data-testid="programming-schedule-name">
            Schedule: <span className="font-medium text-foreground">{schedule?.name ?? '…'}</span>
          </p>
          <p className="text-xs text-muted-foreground" data-testid="programming-timezone">
            {tzLabel(channel.timezone)}
          </p>
        </div>
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

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the schedule items"
          error={error}
          onRetry={() => void refetch()}
          stale={items !== undefined}
        />
      )}

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
              <Table label={`${section.title} schedule items`}>
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
                    const block = blocksById.get(item.block_id ?? '')
                    const count = countByBlock.get(item.block_id ?? '')
                    return (
                      <TableRow key={item.id} data-testid="schedule-item-row">
                        <TableCell>{summarizeRecurrence(item, channel.timezone)}</TableCell>
                        {/* A long block name used to be one nowrap cell: it
                            widened the table past the card and cut the Status
                            column and every row action off the screen. */}
                        <TableCell className="whitespace-normal">
                          <div className="max-w-80 wrap-anywhere">
                            {block ? (
                              <Link
                                to={`/blocks/${block.id}`}
                                className="hover:underline"
                                title={block.name}
                                data-testid="item-block-link"
                              >
                                {block.name}
                              </Link>
                            ) : item.collection_id ? (
                              // A collection item has no block to link to, so the
                              // name links to the collection itself: the row used
                              // to print a bare uuid nobody could open.
                              <span data-testid="item-collection">
                                <Badge variant="default">Collection</Badge>{' '}
                                <Link
                                  to={`/collections/${item.collection_id}`}
                                  className="hover:underline"
                                  title={item.collection_name ?? item.collection_id}
                                  data-testid="item-collection-link"
                                >
                                  {item.collection_name ?? 'a collection that no longer exists'}
                                </Link>{' '}
                                <span className="text-muted-foreground">({item.playback_order})</span>
                              </span>
                            ) : (
                              <span className="text-muted-foreground">{item.block_id}</span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="whitespace-normal" data-testid="item-fill">
                          {!block ? (
                            item.collection_id ? 'Rule' : '—'
                          ) : block.dynamic ? (
                            <span>
                              <Badge variant="default">Dynamic</Badge>{' '}
                              <span className="text-muted-foreground">{block.collection_name ?? 'held for review (old query)'}</span>
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
                        <TableCell className="flex flex-wrap justify-end gap-2 text-right">
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
        timeZone={channel.timezone}
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

      <ConfirmDialog
        open={deletingItem !== undefined}
        onClose={() => setDeletingItem(undefined)}
        title={
          deletingItem
            ? `Delete the ${summarizeRecurrence(deletingItem, channel.timezone)} item of "${blocksById.get(deletingItem.block_id ?? '')?.name ?? 'this block'}"?`
            : 'Delete schedule item?'
        }
        description={
          sharedWith.length > 0
            ? 'Every channel sharing this schedule loses the item. This action cannot be undone.'
            : 'This action cannot be undone.'
        }
        confirmLabel="Delete item"
        pendingLabel="Deleting…"
        onConfirm={() => deletingItem && deleteMutation.mutate(deletingItem.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}

// The one action a channel with no schedule needs: create a schedule and attach
// it to the channel in the same step, so the operator never has to find the
// Settings select afterwards. Also mounted on the channel page's Guide tab,
// which has nothing to draw without a schedule.
export function CreateScheduleButton({
  channel,
  label = 'Create a schedule',
  testId = 'create-schedule',
}: {
  channel: Channel
  label?: string
  // Two tabs can mount this at once (the page keeps the inactive one alive), so
  // the id has to say which one it is.
  testId?: string
}) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const attach = useMutation({
    mutationFn: (schedule: Schedule) =>
      api.put<Channel>(`/channels/${channel.id}`, { ...channelToInput(channel), schedule_id: schedule.id }),
    onSuccess: (saved) => {
      queryClient.setQueryData(['channels', channel.id], saved)
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Schedule created and assigned to this channel.')
    },
    onError: (err) => {
      // The schedule exists either way, so the list has to be refreshed; only
      // the assignment failed.
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      toast.error(
        err instanceof ApiError
          ? `The schedule was created but could not be assigned (${err.message}). Assign it in Settings.`
          : 'The schedule was created but could not be assigned. Assign it in Settings.',
      )
    },
  })

  return (
    <>
      <Button size="sm" data-testid={testId} onClick={() => setOpen(true)}>
        {label}
      </Button>
      <ScheduleDialog open={open} onOpenChange={setOpen} onSaved={(schedule) => attach.mutate(schedule)} />
    </>
  )
}
