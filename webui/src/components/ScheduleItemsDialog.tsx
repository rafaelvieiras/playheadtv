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
