import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
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
import type { BlockPlaybackDraft } from '@/components/BlockPlaybackFields'
import { DynamicCollectionFields, validateCollectionChoice, type CollectionChoice } from '@/components/DynamicCollectionFields'
import { blockPlaybackPayload, DEFAULT_INTERLEAVE, validateInterleave } from '@/lib/interleave'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { DAY_LABELS } from '@/lib/schedule'
import { api, ApiError } from '@/lib/http'
import type { Block, RecurrenceType, ScheduleItem, ScheduleItemInput } from '@/lib/types'

interface ScheduleItemDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  scheduleId: string
  // Present when editing an existing item, absent when creating one.
  item?: ScheduleItem
  // True when the schedule already has a continuous item other than `item`;
  // the form then rejects a second one before the round trip (the backend
  // enforces the same rule with a 409).
  hasOtherContinuous?: boolean
}

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
function validate(form: FormState, hasOtherContinuous: boolean): string | undefined {
  if (form.recurrence_type === 'continuous' && hasOtherContinuous) {
    return 'This schedule already has a continuous item; edit or delete it first.'
  }
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

export function ScheduleItemDialog({
  open,
  onOpenChange,
  scheduleId,
  item,
  hasOtherContinuous = false,
}: ScheduleItemDialogProps) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  // Create-and-attach: a brand-new block made together with the item.
  const [newBlock, setNewBlock] = useState(false)
  const [newBlockName, setNewBlockName] = useState('')
  const [newBlockCollection, setNewBlockCollection] = useState<CollectionChoice>(null)
  const [newBlockPlayback, setNewBlockPlayback] = useState<BlockPlaybackDraft>({ order: 'title', interleave: DEFAULT_INTERLEAVE })
  const isEditing = item !== undefined

  useEffect(() => {
    if (open) {
      setForm(item ? itemToForm(item) : emptyForm)
      setError(undefined)
      setNewBlock(false)
      setNewBlockName('')
      setNewBlockCollection(null)
      setNewBlockPlayback({ order: 'title', interleave: DEFAULT_INTERLEAVE })
    }
  }, [open, item])

  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
    enabled: open,
  })

  const mutation = useMutation({
    mutationFn: async ({ payload }: { payload: ScheduleItemInput; openEditor: boolean }) => {
      let createdBlock: Block | undefined
      if (newBlock) {
        createdBlock = await api.post<Block>('/blocks', {
          name: newBlockName.trim(),
          description: null,
          collection_id: newBlockCollection,
          ...(newBlockCollection ? blockPlaybackPayload(newBlockPlayback.order, newBlockPlayback.interleave) : {}),
        })
        payload = { ...payload, block_id: createdBlock.id }
      }
      try {
        const saved = isEditing
          ? await api.put<ScheduleItem>(`/schedule-items/${item.id}`, payload)
          : await api.post<ScheduleItem>(`/schedules/${scheduleId}/items`, payload)
        return { saved, createdBlock }
      } catch (err) {
        // Don't leave an orphan block behind when the item itself is rejected.
        if (createdBlock) await api.delete(`/blocks/${createdBlock.id}`).catch(() => undefined)
        throw err
      }
    },
    onSuccess: ({ createdBlock }, { openEditor }) => {
      queryClient.invalidateQueries({ queryKey: ['schedule-items', scheduleId] })
      queryClient.invalidateQueries({ queryKey: ['schedules', scheduleId, 'items'] })
      if (createdBlock) queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success(isEditing ? 'Schedule item updated.' : 'Schedule item created.')
      onOpenChange(false)
      if (createdBlock && openEditor) navigate(`/blocks/${createdBlock.id}`)
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

  function submit(openEditor: boolean) {
    let validationError: string | undefined
    if (newBlock) {
      if (newBlockName.trim() === '') validationError = 'Enter a name for the new block.'
      else
        validationError =
          validateCollectionChoice(newBlockCollection) ??
          (newBlockCollection && newBlockPlayback.order === 'interleaved' ? validateInterleave(newBlockPlayback.interleave) : undefined)
    } else if (!form.block_id) {
      validationError = 'Select a block.'
    }
    validationError = validationError ?? validate(form, hasOtherContinuous)
    if (validationError) {
      setError(validationError)
      return
    }
    setError(undefined)
    mutation.mutate({ payload: buildPayload(form), openEditor })
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    submit(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-md">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit schedule item' : 'New schedule item'}</DialogTitle>
            <DialogDescription>
              {isEditing ? 'Update this recurrence rule.' : 'Add a block airing at a given time to this schedule.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            {!newBlock ? (
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
                {!isEditing && (
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    className="self-start px-0"
                    data-testid="new-block-toggle"
                    onClick={() => setNewBlock(true)}
                  >
                    Create a new block instead
                  </Button>
                )}
              </Field>
            ) : (
              <div className="flex flex-col gap-3 rounded-md border p-3" data-testid="new-block-fields">
                <Field>
                  <FieldLabel htmlFor="schedule-item-new-block-name">New block name</FieldLabel>
                  <Input
                    id="schedule-item-new-block-name"
                    value={newBlockName}
                    onChange={(e) => setNewBlockName(e.target.value)}
                  />
                </Field>
                <DynamicCollectionFields
                  value={newBlockCollection}
                  onChange={setNewBlockCollection}
                  idPrefix="schedule-item-new-block-collection"
                  playback={{ value: newBlockPlayback, onChange: setNewBlockPlayback }}
                />
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  className="self-start px-0"
                  onClick={() => setNewBlock(false)}
                >
                  Use an existing block instead
                </Button>
              </div>
            )}

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
            {newBlock && (
              <Button
                type="button"
                variant="secondary"
                data-testid="save-and-edit-block"
                disabled={mutation.isPending}
                onClick={() => submit(true)}
              >
                Save and edit block
              </Button>
            )}
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
