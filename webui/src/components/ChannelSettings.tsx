import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { SaveBar } from '@/components/SaveBar'
import { ScheduleDialog } from '@/components/ScheduleDialog'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { channelToInput } from '@/lib/deco'
import { ApiError, api } from '@/lib/http'
import type { Channel, ChannelInput, Schedule } from '@/lib/types'

// NONE_VALUE stands in for schedule_id: null — Radix Select can't use an
// empty string as an item value.
const NONE_VALUE = '__none__'

type FieldErrors = Partial<Record<'number' | 'name' | 'timezone', string>>

// Settings tab: channel fields and schedule assignment saved through the
// SaveBar, plus the schedule library (create, rename, delete, which channels
// use each one). Schedule create/rename/delete are their own requests; only
// the assignment is part of the channel draft.
export function ChannelSettings({
  channel,
  channels,
  onGoToDeco,
}: {
  channel: Channel
  channels: Channel[]
  onGoToDeco: () => void
}) {
  const queryClient = useQueryClient()
  const initial = useMemo(() => channelToInput(channel), [channel])
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({})
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
      for (const id of new Set([c.schedule_id, c.base_schedule_id])) {
        if (!id) continue
        map.set(id, [...(map.get(id) ?? []), c])
      }
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
    // All three at once, under their own fields: the single `setError` at the
    // bottom of the page answered one problem per click.
    const found: FieldErrors = {}
    if (draft.name.trim() === '') found.name = 'Name is required.'
    if (!Number.isInteger(draft.number) || draft.number < 1) found.number = 'Enter a whole number, 1 or more.'
    if (draft.timezone.trim() === '') found.timezone = 'Timezone is required.'
    setFieldErrors(found)
    if (Object.keys(found).length > 0) return
    setError(null)
    saveMutation.mutate()
  }

  function discard() {
    setDraft(initial)
    setFieldErrors({})
    setError(null)
  }

  const [scheduleDialogOpen, setScheduleDialogOpen] = useState(false)
  const [editingSchedule, setEditingSchedule] = useState<Schedule | undefined>(undefined)
  const [deletingSchedule, setDeletingSchedule] = useState<Schedule | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/schedules/${id}`),
    onSuccess: (_, id) => {
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      setDraft((d) => ({
        ...d,
        schedule_id: d.schedule_id === id ? null : d.schedule_id,
        base_schedule_id: d.base_schedule_id === id ? null : d.base_schedule_id,
      }))
      toast.success('Schedule deleted.')
      setDeletingSchedule(undefined)
    },
    onError: () => toast.error('Could not delete the schedule. Unassign it from every channel first.'),
  })

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-col gap-4">
        <FieldGroup>
          <Field data-invalid={!!fieldErrors.number}>
            <FieldLabel htmlFor="channel-settings-number">
              Number <span className="text-destructive">*</span>
            </FieldLabel>
            <Input
              id="channel-settings-number"
              type="number"
              inputMode="numeric"
              min={1}
              aria-invalid={!!fieldErrors.number}
              aria-describedby={fieldErrors.number ? 'channel-settings-number-error' : undefined}
              value={Number.isNaN(draft.number) ? '' : draft.number}
              onChange={(e) => {
                setDraft((d) => ({ ...d, number: e.target.value === '' ? NaN : Number(e.target.value) }))
                setFieldErrors((current) => ({ ...current, number: undefined }))
              }}
            />
            <FieldError id="channel-settings-number-error">{fieldErrors.number}</FieldError>
          </Field>
          <Field data-invalid={!!fieldErrors.name}>
            <FieldLabel htmlFor="channel-settings-name">
              Name <span className="text-destructive">*</span>
            </FieldLabel>
            <Input
              id="channel-settings-name"
              aria-invalid={!!fieldErrors.name}
              aria-describedby={fieldErrors.name ? 'channel-settings-name-error' : undefined}
              value={draft.name}
              onChange={(e) => {
                setDraft((d) => ({ ...d, name: e.target.value }))
                setFieldErrors((current) => ({ ...current, name: undefined }))
              }}
            />
            <FieldError id="channel-settings-name-error">{fieldErrors.name}</FieldError>
          </Field>
          <Field data-invalid={!!fieldErrors.timezone}>
            <FieldLabel htmlFor="channel-settings-timezone">
              Timezone <span className="text-destructive">*</span>
            </FieldLabel>
            <Input
              id="channel-settings-timezone"
              aria-invalid={!!fieldErrors.timezone}
              aria-describedby={fieldErrors.timezone ? 'channel-settings-timezone-error' : undefined}
              value={draft.timezone}
              onChange={(e) => {
                setDraft((d) => ({ ...d, timezone: e.target.value }))
                setFieldErrors((current) => ({ ...current, timezone: undefined }))
              }}
            />
            <FieldError id="channel-settings-timezone-error">{fieldErrors.timezone}</FieldError>
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
                onValueChange={(v) =>
                  setDraft((d) => {
                    const schedule_id = v === NONE_VALUE ? null : v
                    // The two layers are different schedules: picking the
                    // current base as the pinned one clears the base.
                    return {
                      ...d,
                      schedule_id,
                      base_schedule_id: d.base_schedule_id === schedule_id ? null : d.base_schedule_id,
                    }
                  })
                }
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

      <p className="text-sm text-muted-foreground" data-testid="settings-deco-pointer">
        Base schedule, watermark and fallback filler live in the{' '}
        <button type="button" className="underline" data-testid="settings-deco-link" onClick={onGoToDeco}>
          Deco
        </button>{' '}
        tab.
      </p>

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

      <ConfirmDialog
        open={deletingSchedule !== undefined}
        onClose={() => setDeletingSchedule(undefined)}
        title={`Delete schedule "${deletingSchedule?.name}"?`}
        description="Every item in it goes too. A schedule a channel still uses is refused by the backend until it is unassigned."
        confirmLabel="Delete schedule"
        pendingLabel="Deleting…"
        onConfirm={() => deletingSchedule && deleteMutation.mutate(deletingSchedule.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
