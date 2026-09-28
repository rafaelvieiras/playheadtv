import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/http'
import type { Block, Channel, RecurrenceType, ScheduleEntry, ScheduleEntryInput } from '@/lib/types'

interface ScheduleEntryDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing entry, absent when creating one.
  entry?: ScheduleEntry
}

const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// Internal form shape: always keeps both recurrence branches' fields around
// so the toggle doesn't lose what the user typed, and only one branch is
// sent to the API on submit (see buildPayload).
interface FormState {
  channel_id: string
  block_id: string
  recurrence_type: RecurrenceType
  day_of_week: number
  start_time_local: string
  // datetime-local input value, e.g. "2026-09-26T14:00".
  start_at_local: string
  active: boolean
}

const emptyForm: FormState = {
  channel_id: '',
  block_id: '',
  recurrence_type: 'weekly',
  day_of_week: 0,
  start_time_local: '',
  start_at_local: '',
  active: true,
}

function epochToDatetimeLocalInput(ms: number): string {
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function entryToForm(entry: ScheduleEntry): FormState {
  return {
    channel_id: entry.channel_id,
    block_id: entry.block_id,
    recurrence_type: entry.recurrence_type,
    day_of_week: entry.day_of_week ?? 0,
    start_time_local: entry.start_time_local ?? '',
    start_at_local: entry.start_at !== null ? epochToDatetimeLocalInput(entry.start_at) : '',
    active: entry.active,
  }
}

// Mirrors validateScheduleEntryRequest in backend/internal/api/schedules.go —
// keep in sync so the client rejects the same invalid combinations before
// a round trip to the API.
function validate(form: FormState): string | undefined {
  if (!form.channel_id) return 'Select a channel.'
  if (!form.block_id) return 'Select a block.'
  if (form.recurrence_type === 'weekly') {
    if (form.day_of_week < 0 || form.day_of_week > 6) return 'Invalid day of the week.'
    if (!/^\d{2}:\d{2}$/.test(form.start_time_local)) return 'Enter a time in HH:MM format.'
  } else {
    if (!form.start_at_local) return 'Enter a date and time.'
  }
  return undefined
}

function buildPayload(form: FormState): ScheduleEntryInput {
  if (form.recurrence_type === 'weekly') {
    return {
      channel_id: form.channel_id,
      block_id: form.block_id,
      recurrence_type: 'weekly',
      day_of_week: form.day_of_week,
      start_time_local: form.start_time_local,
      start_at: null,
      active: form.active,
    }
  }
  return {
    channel_id: form.channel_id,
    block_id: form.block_id,
    recurrence_type: 'once',
    day_of_week: null,
    start_time_local: null,
    start_at: new Date(form.start_at_local).getTime(),
    active: form.active,
  }
}

export function ScheduleEntryDialog({ open, onOpenChange, entry }: ScheduleEntryDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = entry !== undefined

  useEffect(() => {
    if (open) {
      setForm(entry ? entryToForm(entry) : emptyForm)
      setError(undefined)
    }
  }, [open, entry])

  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
    enabled: open,
  })
  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
    enabled: open,
  })

  const mutation = useMutation({
    mutationFn: (payload: ScheduleEntryInput) =>
      isEditing
        ? api.put<ScheduleEntry>(`/schedule-entries/${entry.id}`, payload)
        : api.post<ScheduleEntry>('/schedule-entries', payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedule-entries'] })
      toast.success(isEditing ? 'Schedule entry updated.' : 'Schedule entry created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the schedule entry.' : 'Could not create the schedule entry.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const validationError = validate(form)
    if (validationError) {
      setError(validationError)
      return
    }
    setError(undefined)
    mutation.mutate(buildPayload(form))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit schedule entry' : 'New schedule entry'}</DialogTitle>
            <DialogDescription>
              {isEditing ? 'Update the channel programming.' : 'Link a block to a channel at a given time.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="schedule-channel">Channel</FieldLabel>
              <Select value={form.channel_id} onValueChange={(v) => setForm((f) => ({ ...f, channel_id: v }))}>
                <SelectTrigger id="schedule-channel" className="w-full">
                  <SelectValue placeholder="Select a channel" />
                </SelectTrigger>
                <SelectContent>
                  {channels?.map((channel) => (
                    <SelectItem key={channel.id} value={channel.id}>
                      {channel.number} — {channel.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor="schedule-block">Block</FieldLabel>
              <Select value={form.block_id} onValueChange={(v) => setForm((f) => ({ ...f, block_id: v }))}>
                <SelectTrigger id="schedule-block" className="w-full">
                  <SelectValue placeholder="Select a block" />
                </SelectTrigger>
                <SelectContent>
                  {blocks?.map((block) => (
                    <SelectItem key={block.id} value={block.id}>
                      {block.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field>
              <FieldLabel>Recurrence</FieldLabel>
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant={form.recurrence_type === 'weekly' ? 'default' : 'outline'}
                  size="sm"
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'weekly' }))}
                >
                  Weekly
                </Button>
                <Button
                  type="button"
                  variant={form.recurrence_type === 'once' ? 'default' : 'outline'}
                  size="sm"
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'once' }))}
                >
                  Once
                </Button>
              </div>
            </Field>

            {form.recurrence_type === 'weekly' ? (
              <>
                <Field>
                  <FieldLabel htmlFor="schedule-day">Day of the week</FieldLabel>
                  <Select
                    value={String(form.day_of_week)}
                    onValueChange={(v) => setForm((f) => ({ ...f, day_of_week: Number(v) }))}
                  >
                    <SelectTrigger id="schedule-day" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {DAY_LABELS.map((label, i) => (
                        <SelectItem key={i} value={String(i)}>
                          {label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field>
                  <FieldLabel htmlFor="schedule-time">Time (HH:MM)</FieldLabel>
                  <Input
                    id="schedule-time"
                    type="time"
                    required
                    value={form.start_time_local}
                    onChange={(e) => setForm((f) => ({ ...f, start_time_local: e.target.value }))}
                  />
                </Field>
              </>
            ) : (
              <Field>
                <FieldLabel htmlFor="schedule-start-at">Date and time</FieldLabel>
                <Input
                  id="schedule-start-at"
                  type="datetime-local"
                  required
                  value={form.start_at_local}
                  onChange={(e) => setForm((f) => ({ ...f, start_at_local: e.target.value }))}
                />
              </Field>
            )}

            <Field orientation="horizontal">
              <FieldLabel htmlFor="schedule-active">Active</FieldLabel>
              <Switch
                id="schedule-active"
                checked={form.active}
                onCheckedChange={(checked) => setForm((f) => ({ ...f, active: checked }))}
              />
            </Field>

            {error && <p className="text-sm text-destructive">{error}</p>}
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
