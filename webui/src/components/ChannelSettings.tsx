import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { SaveBar } from '@/components/SaveBar'
import { ScheduleDialog } from '@/components/ScheduleDialog'
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
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ApiError, api } from '@/lib/http'
import type { Channel, ChannelInput, Schedule } from '@/lib/types'

// NONE_VALUE stands in for schedule_id: null — Radix Select can't use an
// empty string as an item value.
const NONE_VALUE = '__none__'

function toInput(channel: Channel): ChannelInput {
  return {
    number: channel.number,
    name: channel.name,
    guide_art_url: channel.guide_art_url,
    timezone: channel.timezone,
    schedule_id: channel.schedule_id,
  }
}

// Settings tab: channel fields and schedule assignment saved through the
// SaveBar, plus the schedule library (create, rename, delete, which channels
// use each one). Schedule create/rename/delete are their own requests; only
// the assignment is part of the channel draft.
export function ChannelSettings({ channel, channels }: { channel: Channel; channels: Channel[] }) {
  const queryClient = useQueryClient()
  const initial = useMemo(() => toInput(channel), [channel])
  const [draft, setDraft] = useState<ChannelInput>(initial)
  const [error, setError] = useState<string | null>(null)
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial)

  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
  })
  const sortedSchedules = useMemo(
    () => [...(schedules ?? [])].sort((a, b) => a.name.localeCompare(b.name)),
    [schedules],
  )
  const channelsBySchedule = useMemo(() => {
    const map = new Map<string, Channel[]>()
    for (const c of channels) {
      if (!c.schedule_id) continue
      map.set(c.schedule_id, [...(map.get(c.schedule_id) ?? []), c])
    }
    return map
  }, [channels])

  const saveMutation = useMutation({
    mutationFn: () => api.put<Channel>(`/channels/${channel.id}`, { ...draft, name: draft.name.trim() }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Channel saved.')
      setError(null)
    },
    onError: (err) =>
      setError(err instanceof ApiError ? `Could not save the channel (${err.message})` : 'Could not save the channel.'),
  })

  function save() {
    if (draft.name.trim() === '') return setError('Name is required.')
    if (!Number.isInteger(draft.number) || draft.number < 1) return setError('Number must be a positive integer.')
    if (draft.timezone.trim() === '') return setError('Timezone is required.')
    setError(null)
    saveMutation.mutate()
  }

  function discard() {
    setDraft(initial)
    setError(null)
  }

  const [scheduleDialogOpen, setScheduleDialogOpen] = useState(false)
  const [editingSchedule, setEditingSchedule] = useState<Schedule | undefined>(undefined)
  const [deletingSchedule, setDeletingSchedule] = useState<Schedule | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedules/${id}`),
    onSuccess: (_, id) => {
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      setDraft((d) => (d.schedule_id === id ? { ...d, schedule_id: null } : d))
      toast.success('Schedule deleted.')
      setDeletingSchedule(undefined)
    },
    onError: () => toast.error('Could not delete the schedule. Unassign it from every channel first.'),
  })

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-col gap-4">
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="channel-settings-number">Number</FieldLabel>
            <Input
              id="channel-settings-number"
              type="number"
              value={Number.isNaN(draft.number) ? '' : draft.number}
              onChange={(e) => setDraft((d) => ({ ...d, number: e.target.value === '' ? NaN : Number(e.target.value) }))}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="channel-settings-name">Name</FieldLabel>
            <Input
              id="channel-settings-name"
              value={draft.name}
              onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="channel-settings-timezone">Timezone</FieldLabel>
            <Input
              id="channel-settings-timezone"
              value={draft.timezone}
              onChange={(e) => setDraft((d) => ({ ...d, timezone: e.target.value }))}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="channel-settings-art">Guide art URL (optional)</FieldLabel>
            <Input
              id="channel-settings-art"
              type="url"
              value={draft.guide_art_url ?? ''}
              onChange={(e) => setDraft((d) => ({ ...d, guide_art_url: e.target.value || null }))}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="channel-settings-schedule">Schedule</FieldLabel>
            <div className="flex items-center gap-2">
              <Select
                value={draft.schedule_id ?? NONE_VALUE}
                onValueChange={(v) => setDraft((d) => ({ ...d, schedule_id: v === NONE_VALUE ? null : v }))}
              >
                <SelectTrigger id="channel-settings-schedule" className="w-full">
                  <SelectValue placeholder="No schedule" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE_VALUE}>None</SelectItem>
                  {sortedSchedules.map((schedule) => (
                    <SelectItem key={schedule.id} value={schedule.id}>
                      {schedule.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="outline"
                className="shrink-0"
                data-testid="new-schedule"
                onClick={() => {
                  setEditingSchedule(undefined)
                  setScheduleDialogOpen(true)
                }}
              >
                New schedule
              </Button>
            </div>
          </Field>
        </FieldGroup>
        <SaveBar dirty={dirty} saving={saveMutation.isPending} onSave={save} onDiscard={discard} error={error} />
      </section>

      <section className="flex flex-col gap-2" data-testid="schedule-library">
        <h2 className="text-sm font-medium">Schedules</h2>
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
              const users = channelsBySchedule.get(schedule.id) ?? []
              return (
                <TableRow key={schedule.id} data-testid="schedule-row">
                  <TableCell>{schedule.name}</TableCell>
                  <TableCell className="max-w-64 truncate text-muted-foreground">
                    {schedule.description ?? '—'}
                  </TableCell>
                  <TableCell className="text-muted-foreground" data-testid="schedule-channels">
                    {users.length === 0
                      ? 'None'
                      : users.map((c, i) => (
                          <span key={c.id}>
                            {i > 0 && ', '}
                            <Link to={`/channels/${c.id}`} className="hover:underline">
                              {c.number} — {c.name}
                            </Link>
                          </span>
                        ))}
                  </TableCell>
                  <TableCell className="flex justify-end gap-2 text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setEditingSchedule(schedule)
                        setScheduleDialogOpen(true)
                      }}
                    >
                      Rename
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={users.length > 0}
                      title={users.length > 0 ? 'In use by a channel; unassign it first.' : undefined}
                      onClick={() => setDeletingSchedule(schedule)}
                    >
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </section>

      <ScheduleDialog
        open={scheduleDialogOpen}
        onOpenChange={setScheduleDialogOpen}
        schedule={editingSchedule}
        onSaved={(saved) => {
          if (!editingSchedule) setDraft((d) => ({ ...d, schedule_id: saved.id }))
        }}
      />

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
              onClick={(e) => {
                e.preventDefault()
                if (deletingSchedule) deleteMutation.mutate(deletingSchedule.id)
              }}
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
