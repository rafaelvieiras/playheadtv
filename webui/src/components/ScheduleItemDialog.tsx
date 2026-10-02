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
import { api, ApiError } from '@/lib/http'
import type { Block, RecurrenceType, ScheduleItem, ScheduleItemInput } from '@/lib/types'

interface ScheduleItemDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  scheduleId: string
  // Present when editing an existing item, absent when creating one.
  item?: ScheduleItem
}

const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// Internal form shape: always keeps both recurrence branches' fields around
// so the toggle doesn't lose what the user typed, and only one branch is
// sent to the API on submit (see buildPayload).
interface FormState {
  block_id: string
  recurrence_type: RecurrenceType
  day_of_week: number
  start_time_local: string
  // datetime-local input value, e.g. "2026-09-26T14:00".
  start_at_local: string
  active: boolean
}

const emptyForm: FormState = {
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

function itemToForm(item: ScheduleItem): FormState {
  return {
    block_id: item.block_id,
    recurrence_type: item.recurrence_type,
    day_of_week: item.day_of_week ?? 0,
    start_time_local: item.start_time_local ?? '',
    start_at_local: item.start_at !== null ? epochToDatetimeLocalInput(item.start_at) : '',
    active: item.active,
  }
}

// Mirrors validateScheduleItemRequest in backend/internal/api/schedules.go —
// keep in sync so the client rejects the same invalid combinations before a
// round trip to the API.
function validate(form: FormState): string | undefined {
  if (!form.block_id) return 'Select a block.'
  if (form.recurrence_type === 'continuous') return undefined
  if (form.recurrence_type === 'weekly') {
    if (form.day_of_week < 0 || form.day_of_week > 6) return 'Invalid day of the week.'
    if (!/^\d{2}:\d{2}$/.test(form.start_time_local)) return 'Enter a time in HH:MM format.'
  } else {
    if (!form.start_at_local) return 'Enter a date and time.'
  }
  return undefined
}

function buildPayload(form: FormState): ScheduleItemInput {
  if (form.recurrence_type === 'continuous') {
    return {
      block_id: form.block_id,
      recurrence_type: 'continuous',
      day_of_week: null,
      start_time_local: null,
      start_at: null,
      active: form.active,
    }
  }
  if (form.recurrence_type === 'weekly') {
    return {
      block_id: form.block_id,
      recurrence_type: 'weekly',
      day_of_week: form.day_of_week,
      start_time_local: form.start_time_local,
      start_at: null,
      active: form.active,
    }
  }
  return {
    block_id: form.block_id,
    recurrence_type: 'once',
    day_of_week: null,
    start_time_local: null,
    start_at: new Date(form.start_at_local).getTime(),
    active: form.active,
  }
}

export function ScheduleItemDialog({ open, onOpenChange, scheduleId, item }: ScheduleItemDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = item !== undefined

  useEffect(() => {
    if (open) {
      setForm(item ? itemToForm(item) : emptyForm)
      setError(undefined)
    }
  }, [open, item])

  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
    enabled: open,
  })

  const mutation = useMutation({
    mutationFn: (payload: ScheduleItemInput) =>
      isEditing
        ? api.put<ScheduleItem>(`/schedule-items/${item.id}`, payload)
        : api.post<ScheduleItem>(`/schedules/${scheduleId}/items`, payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedule-items', scheduleId] })
      toast.success(isEditing ? 'Schedule item updated.' : 'Schedule item created.')
      onOpenChange(false)
    },
    onError: (err) => {
      // 409: the schedule already has its one continuous item; the server's
      // message says so.
      if (err instanceof ApiError && err.status === 409) {
        toast.error(err.message)
        return
      }
      toast.error(isEditing ? 'Could not update the schedule item.' : 'Could not create the schedule item.')
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
            <DialogTitle>{isEditing ? 'Edit schedule item' : 'New schedule item'}</DialogTitle>
            <DialogDescription>
              {isEditing ? 'Update this recurrence rule.' : 'Add a block airing at a given time to this schedule.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="schedule-item-block">Block</FieldLabel>
              <Select value={form.block_id} onValueChange={(v) => setForm((f) => ({ ...f, block_id: v }))}>
                <SelectTrigger id="schedule-item-block" className="w-full">
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
                <Button
                  type="button"
                  variant={form.recurrence_type === 'continuous' ? 'default' : 'outline'}
                  size="sm"
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'continuous' }))}
                >
                  Continuous
                </Button>
              </div>
              {form.recurrence_type === 'continuous' && (
                <p className="text-sm text-muted-foreground">
                  No fixed time: airs whenever no timed item does, resuming its rotation where it left off. A schedule
                  can have only one.
                </p>
              )}
            </Field>

            {form.recurrence_type === 'continuous' ? null : form.recurrence_type === 'weekly' ? (
              <>
                <Field>
                  <FieldLabel htmlFor="schedule-item-day">Day of the week</FieldLabel>
                  <Select
                    value={String(form.day_of_week)}
                    onValueChange={(v) => setForm((f) => ({ ...f, day_of_week: Number(v) }))}
                  >
                    <SelectTrigger id="schedule-item-day" className="w-full">
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
                  <FieldLabel htmlFor="schedule-item-time">Time (HH:MM)</FieldLabel>
                  <Input
                    id="schedule-item-time"
                    type="time"
                    required
                    value={form.start_time_local}
                    onChange={(e) => setForm((f) => ({ ...f, start_time_local: e.target.value }))}
                  />
                </Field>
              </>
            ) : (
              <Field>
                <FieldLabel htmlFor="schedule-item-start-at">Date and time</FieldLabel>
                <Input
                  id="schedule-item-start-at"
                  type="datetime-local"
                  required
                  value={form.start_at_local}
                  onChange={(e) => setForm((f) => ({ ...f, start_at_local: e.target.value }))}
                />
              </Field>
            )}

            <Field orientation="horizontal">
              <FieldLabel htmlFor="schedule-item-active">Active</FieldLabel>
              <Switch
                id="schedule-item-active"
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
