import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ScheduleItemDialog } from '@/components/ScheduleItemDialog'
import { api } from '@/lib/http'
import type { Block, Schedule, ScheduleItem } from '@/lib/types'

interface ScheduleItemsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  schedule?: Schedule
}

const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function summarizeRecurrence(item: ScheduleItem): string {
  if (item.recurrence_type === 'continuous') return 'Continuous (whenever no timed item airs)'
  if (item.recurrence_type === 'weekly') {
    const day = item.day_of_week !== null ? DAY_LABELS[item.day_of_week] : '?'
    return `Every ${day} at ${item.start_time_local ?? '?'}`
  }
  if (item.start_at === null) return 'Once (no date set)'
  const date = new Date(item.start_at)
  return `Once on ${date.toLocaleDateString('en-US')} ${date.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
  })}`
}

// A schedule made of seven weekly items at 00:00 (one per weekday) pointing
// to the same dynamic block is the old way of faking a 24/7 rotation; it can
// be replaced by one continuous item. Returns that block's id, else undefined.
function legacyDailyDynamicBlock(items: ScheduleItem[], blocksById: Map<string, Block>): string | undefined {
  if (items.length !== 7) return undefined
  if (!items.every((i) => i.recurrence_type === 'weekly' && i.start_time_local === '00:00')) return undefined
  const days = new Set(items.map((i) => i.day_of_week))
  if (days.size !== 7 || ![0, 1, 2, 3, 4, 5, 6].every((d) => days.has(d))) return undefined
  const blockId = items[0].block_id
  if (!items.every((i) => i.block_id === blockId)) return undefined
  return blocksById.get(blockId)?.criteria ? blockId : undefined
}

export function ScheduleItemsDialog({ open, onOpenChange, schedule }: ScheduleItemsDialogProps) {
  const queryClient = useQueryClient()
  const scheduleId = schedule?.id

  const { data: items, isLoading } = useQuery({
    queryKey: ['schedule-items', scheduleId],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${scheduleId}/items`),
    enabled: open && !!scheduleId,
  })
  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
    enabled: open,
  })
  const blocksById = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b])), [blocks])

  const [itemDialogOpen, setItemDialogOpen] = useState(false)
  const [editingItem, setEditingItem] = useState<ScheduleItem | undefined>(undefined)
  const [deletingItem, setDeletingItem] = useState<ScheduleItem | undefined>(undefined)
  const [convertOpen, setConvertOpen] = useState(false)

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
    onError: () => {
      toast.error('Could not convert the schedule.')
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['schedule-items', scheduleId] })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedule-items/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedule-items', scheduleId] })
      toast.success('Schedule item deleted.')
      setDeletingItem(undefined)
    },
    onError: () => {
      toast.error('Could not delete the schedule item.')
    },
  })

  function openCreateDialog() {
    setEditingItem(undefined)
    setItemDialogOpen(true)
  }

  function openEditDialog(item: ScheduleItem) {
    setEditingItem(item)
    setItemDialogOpen(true)
  }

  const sortedItems = useMemo(() => [...(items ?? [])].sort((a, b) => b.created_at - a.created_at), [items])

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Schedule items{schedule ? `: ${schedule.name}` : ''}</DialogTitle>
            <DialogDescription>The recurrence rules that make up this schedule.</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-4">
            <div className="flex justify-end">
              <Button size="sm" onClick={openCreateDialog} disabled={!scheduleId}>
                New item
              </Button>
            </div>

            {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

            {legacyBlockId && (
              <div
                data-testid="legacy-daily-banner"
                className="flex items-center justify-between gap-3 rounded-md border p-3 text-sm"
              >
                <span>
                  This schedule has seven 00:00 items (one per weekday) for the same dynamic block. A single
                  continuous item does the same without restarting the rotation every day.
                </span>
                <Button size="sm" variant="outline" onClick={() => setConvertOpen(true)}>
                  Convert to continuous
                </Button>
              </div>
            )}

            {items && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Block</TableHead>
                    <TableHead>Recurrence</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sortedItems.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={4} className="text-center text-muted-foreground">
                        No items in this schedule yet.
                      </TableCell>
                    </TableRow>
                  )}
                  {sortedItems.map((item) => (
                    <TableRow key={item.id}>
                      <TableCell>{blocksById.get(item.block_id)?.name ?? item.block_id}</TableCell>
                      <TableCell>{summarizeRecurrence(item)}</TableCell>
                      <TableCell>
                        {item.active ? (
                          <span className="text-sm text-primary">Active</span>
                        ) : (
                          <span className="text-sm text-muted-foreground">Inactive</span>
                        )}
                      </TableCell>
                      <TableCell className="flex justify-end gap-2 text-right">
                        <Button variant="outline" size="sm" onClick={() => openEditDialog(item)}>
                          Edit
                        </Button>
                        <Button variant="destructive" size="sm" onClick={() => setDeletingItem(item)}>
                          Delete
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </DialogContent>
      </Dialog>

      {scheduleId && (
        <ScheduleItemDialog
          open={itemDialogOpen}
          onOpenChange={setItemDialogOpen}
          scheduleId={scheduleId}
          item={editingItem}
        />
      )}

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
              This will permanently delete this schedule item. This action cannot be undone.
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
    </>
  )
}
