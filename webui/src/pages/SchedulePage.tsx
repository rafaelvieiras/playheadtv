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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ScheduleEntryDialog } from '@/components/ScheduleEntryDialog'
import { api } from '@/lib/http'
import type { Block, Channel, ScheduleEntry } from '@/lib/types'

const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function summarizeRecurrence(entry: ScheduleEntry): string {
  if (entry.recurrence_type === 'weekly') {
    const day = entry.day_of_week !== null ? DAY_LABELS[entry.day_of_week] : '?'
    return `Every ${day} at ${entry.start_time_local ?? '?'}`
  }
  if (entry.start_at === null) return 'Once (no date set)'
  const date = new Date(entry.start_at)
  return `Once on ${date.toLocaleDateString('en-US')} ${date.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
  })}`
}

export function SchedulePage() {
  const queryClient = useQueryClient()
  const { data: entries, isLoading, isError } = useQuery({
    queryKey: ['schedule-entries'],
    queryFn: () => api.get<ScheduleEntry[]>('/schedule-entries'),
  })
  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })
  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
  })

  const channelsById = useMemo(() => new Map((channels ?? []).map((c) => [c.id, c])), [channels])
  const blocksById = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b])), [blocks])

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingEntry, setEditingEntry] = useState<ScheduleEntry | undefined>(undefined)
  const [deletingEntry, setDeletingEntry] = useState<ScheduleEntry | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedule-entries/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedule-entries'] })
      toast.success('Schedule entry deleted.')
      setDeletingEntry(undefined)
    },
    onError: () => {
      toast.error('Could not delete the schedule entry.')
    },
  })

  function openCreateDialog() {
    setEditingEntry(undefined)
    setDialogOpen(true)
  }

  function openEditDialog(entry: ScheduleEntry) {
    setEditingEntry(entry)
    setDialogOpen(true)
  }

  const sortedEntries = useMemo(
    () => [...(entries ?? [])].sort((a, b) => b.created_at - a.created_at),
    [entries],
  )

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Schedule</h1>
        <Button onClick={openCreateDialog}>New schedule entry</Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load the schedule.</p>}

      {entries && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Channel</TableHead>
              <TableHead>Block</TableHead>
              <TableHead>Recurrence</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sortedEntries.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
                  No schedule entries yet.
                </TableCell>
              </TableRow>
            )}
            {sortedEntries.map((entry) => {
              const channel = channelsById.get(entry.channel_id)
              const block = blocksById.get(entry.block_id)
              return (
                <TableRow key={entry.id}>
                  <TableCell>{channel ? `${channel.number} — ${channel.name}` : entry.channel_id}</TableCell>
                  <TableCell>{block?.name ?? entry.block_id}</TableCell>
                  <TableCell>{summarizeRecurrence(entry)}</TableCell>
                  <TableCell>
                    {entry.active ? (
                      <span className="text-sm text-primary">Active</span>
                    ) : (
                      <span className="text-sm text-muted-foreground">Inactive</span>
                    )}
                  </TableCell>
                  <TableCell className="flex justify-end gap-2 text-right">
                    <Button variant="outline" size="sm" onClick={() => openEditDialog(entry)}>
                      Edit
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => setDeletingEntry(entry)}>
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      <ScheduleEntryDialog open={dialogOpen} onOpenChange={setDialogOpen} entry={editingEntry} />

      <AlertDialog open={deletingEntry !== undefined} onOpenChange={(open) => !open && setDeletingEntry(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete schedule entry?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete this schedule entry. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingEntry && deleteMutation.mutate(deletingEntry.id)}
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
