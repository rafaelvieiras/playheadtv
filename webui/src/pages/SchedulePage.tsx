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
import { ScheduleDialog } from '@/components/ScheduleDialog'
import { ScheduleItemsDialog } from '@/components/ScheduleItemsDialog'
import { api } from '@/lib/http'
import type { Channel, Schedule } from '@/lib/types'

export function SchedulePage() {
  const queryClient = useQueryClient()
  const { data: schedules, isLoading, isError } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
  })
  // Only needed to show which channels a schedule currently drives — a
  // schedule itself carries no channel reference (issue #7's split put that
  // on channel.schedule_id instead).
  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })

  const channelsBySchedule = useMemo(() => {
    const map = new Map<string, Channel[]>()
    for (const channel of channels ?? []) {
      if (!channel.schedule_id) continue
      const list = map.get(channel.schedule_id) ?? []
      list.push(channel)
      map.set(channel.schedule_id, list)
    }
    return map
  }, [channels])

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingSchedule, setEditingSchedule] = useState<Schedule | undefined>(undefined)
  const [deletingSchedule, setDeletingSchedule] = useState<Schedule | undefined>(undefined)
  const [itemsDialogOpen, setItemsDialogOpen] = useState(false)
  const [selectedSchedule, setSelectedSchedule] = useState<Schedule | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedules/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      toast.success('Schedule deleted.')
      setDeletingSchedule(undefined)
    },
    onError: () => {
      toast.error('Could not delete the schedule. Unassign it from every channel first.')
    },
  })

  function openCreateDialog() {
    setEditingSchedule(undefined)
    setDialogOpen(true)
  }

  function openEditDialog(schedule: Schedule) {
    setEditingSchedule(schedule)
    setDialogOpen(true)
  }

  function openItemsDialog(schedule: Schedule) {
    setSelectedSchedule(schedule)
    setItemsDialogOpen(true)
  }

  const sortedSchedules = useMemo(() => [...(schedules ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [schedules])

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Schedule</h1>
        <Button onClick={openCreateDialog}>New schedule</Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load schedules.</p>}

      {schedules && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Description</TableHead>
              <TableHead>Channels</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sortedSchedules.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  No schedules yet.
                </TableCell>
              </TableRow>
            )}
            {sortedSchedules.map((schedule) => {
              const drivenChannels = channelsBySchedule.get(schedule.id) ?? []
              return (
                <TableRow key={schedule.id}>
                  <TableCell>{schedule.name}</TableCell>
                  <TableCell className="max-w-64 truncate text-muted-foreground">
                    {schedule.description ?? '—'}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {drivenChannels.length === 0
                      ? 'None'
                      : drivenChannels.map((c) => `${c.number} — ${c.name}`).join(', ')}
                  </TableCell>
                  <TableCell className="flex justify-end gap-2 text-right">
                    <Button variant="outline" size="sm" onClick={() => openItemsDialog(schedule)}>
                      Items
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => openEditDialog(schedule)}>
                      Edit
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => setDeletingSchedule(schedule)}>
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      <ScheduleDialog open={dialogOpen} onOpenChange={setDialogOpen} schedule={editingSchedule} />

      <ScheduleItemsDialog open={itemsDialogOpen} onOpenChange={setItemsDialogOpen} schedule={selectedSchedule} />

      <AlertDialog open={deletingSchedule !== undefined} onOpenChange={(open) => !open && setDeletingSchedule(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete schedule?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete the schedule "{deletingSchedule?.name}" and its items. This action cannot
              be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingSchedule && deleteMutation.mutate(deletingSchedule.id)}
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
