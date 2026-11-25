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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import type { BlockPlaybackDraft } from '@/components/BlockPlaybackFields'
import { DynamicCollectionFields, validateCollectionChoice, type CollectionChoice } from '@/components/DynamicCollectionFields'
import { CollectionPicker } from '@/components/CollectionPicker'
import { blockPlaybackPayload, DEFAULT_INTERLEAVE, validateInterleave } from '@/lib/interleave'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { tzLabel } from '@/lib/format'
import {
  DAY_LABELS,
  DAY_SHORT_LABELS,
  fromZonedInputValue,
  toZonedInputValue,
  WEEK_ORDER,
  WEEKDAY_SHORTCUTS,
} from '@/lib/schedule'
import { api, ApiError } from '@/lib/http'
import type { Block, CriteriaOrder, RecurrenceType, ScheduleItem, ScheduleItemInput } from '@/lib/types'
import { CRITERIA_ORDER_LABELS } from '@/lib/types'

// The orders a collection item may air in (issue #100). Interleaved is left out
// on purpose: it needs an interleave spec the item row has no room for, so a
// schedule item offers the same four orders the 24/7 loop shortcut does.
const COLLECTION_ITEM_ORDERS: CriteriaOrder[] = ['shuffle', 'random', 'title', 'release_date']

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
  // The channel's IANA zone. A one-off item's date and time are read and
  // written in it, so the dialog shows the same clock time as the row behind
  // it (and the same one the grid draws), whatever the browser's own zone is.
  timeZone?: string
}

// Internal form shape: always keeps both recurrence branches' fields around
// so the toggle doesn't lose what the user typed, and only one branch is
// sent to the API on submit (see buildPayload).
interface FormState {
  // Which of the two targets the item has: a block (an authored list) or a
  // collection aired as the rule it is. Exactly one is ever sent.
  target: 'block' | 'collection'
  block_id: string
  collection_id: string
  playback_order: CriteriaOrder
  recurrence_type: RecurrenceType
  days_of_week: number[]
  start_time_local: string
  // datetime-local input value, e.g. "2026-09-26T14:00".
  start_at_local: string
  active: boolean
}

const emptyForm: FormState = {
  target: 'block',
  block_id: '',
  collection_id: '',
  playback_order: 'shuffle',
  recurrence_type: 'weekly',
  days_of_week: [],
  start_time_local: '',
  start_at_local: '',
  active: true,
}

function itemToForm(item: ScheduleItem, timeZone?: string): FormState {
  return {
    // An item created by the 24/7 loop shortcut (or by MCP) carries no block at
    // all: it airs a collection. Reading it as "a block the list does not have"
    // is what left this dialog with an empty select and no way to save.
    target: item.collection_id ? 'collection' : 'block',
    block_id: item.block_id ?? '',
    collection_id: item.collection_id ?? '',
    playback_order: item.playback_order ?? 'shuffle',
    recurrence_type: item.recurrence_type,
    days_of_week: item.days_of_week,
    start_time_local: item.start_time_local ?? '',
    start_at_local: item.start_at !== null ? toZonedInputValue(item.start_at, timeZone) : '',
    active: item.active,
  }
}

// Mirrors validateScheduleItemRequest in backend/internal/api/schedules.go —
// keep in sync so the client rejects the same invalid combinations before a
// round trip to the API.
type Errors = Partial<Record<'block' | 'collection' | 'newBlockName' | 'recurrence' | 'days' | 'start_time' | 'start_at', string>>

// Every problem at once, keyed by the field it belongs to. The old version
// answered one at a time and only in the footer, while the browser's own
// `required` bubble pointed at a field the operator had not forgotten.
function validate(form: FormState, hasOtherContinuous: boolean): Errors {
  const errors: Errors = {}
  if (form.recurrence_type === 'continuous') {
    if (hasOtherContinuous) {
      errors.recurrence = 'This schedule already has a continuous item; edit or delete it first.'
    }
    return errors
  }
  if (form.recurrence_type === 'weekly') {
    if (form.days_of_week.length === 0) errors.days = 'Select at least one day of the week.'
    if (!/^\d{2}:\d{2}$/.test(form.start_time_local)) errors.start_time = 'Enter a time in HH:MM format.'
  } else if (!form.start_at_local) {
    errors.start_at = 'Enter a date and time.'
  }
  return errors
}

function buildPayload(form: FormState, timeZone?: string): ScheduleItemInput {
  // Exactly one target travels: the API refuses a body carrying both.
  const target: Pick<ScheduleItemInput, 'block_id' | 'collection_id' | 'playback_order'> =
    form.target === 'collection'
      ? { collection_id: form.collection_id, playback_order: form.playback_order }
      : { block_id: form.block_id }
  if (form.recurrence_type === 'continuous') {
    return {
      ...target,
      recurrence_type: 'continuous',
      days_of_week: [],
      start_time_local: null,
      start_at: null,
      active: form.active,
    }
  }
  if (form.recurrence_type === 'weekly') {
    return {
      ...target,
      recurrence_type: 'weekly',
      days_of_week: [...form.days_of_week].sort((a, b) => a - b),
      start_time_local: form.start_time_local,
      start_at: null,
      active: form.active,
    }
  }
  return {
    ...target,
    recurrence_type: 'once',
    days_of_week: [],
    start_time_local: null,
    start_at: fromZonedInputValue(form.start_at_local, timeZone),
    active: form.active,
  }
}

export function ScheduleItemDialog({
  open,
  onOpenChange,
  scheduleId,
  item,
  hasOtherContinuous = false,
  timeZone,
}: ScheduleItemDialogProps) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [errors, setErrors] = useState<Errors>({})
  const [error, setError] = useState<string | undefined>(undefined)
  // Create-and-attach: a brand-new block made together with the item.
  const [newBlock, setNewBlock] = useState(false)
  const [newBlockName, setNewBlockName] = useState('')
  const [newBlockCollection, setNewBlockCollection] = useState<CollectionChoice>(null)
  const [newBlockPlayback, setNewBlockPlayback] = useState<BlockPlaybackDraft>({ order: 'title', interleave: DEFAULT_INTERLEAVE })
  const isEditing = item !== undefined

  useEffect(() => {
    if (open) {
      setForm(item ? itemToForm(item, timeZone) : emptyForm)
      setErrors({})
      setError(undefined)
      setNewBlock(false)
      setNewBlockName('')
      setNewBlockCollection(null)
      setNewBlockPlayback({ order: 'title', interleave: DEFAULT_INTERLEAVE })
    }
  }, [open, item, timeZone])

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
    const found: Errors = validate(form, hasOtherContinuous)
    // The target is the field this dialog most often loses, and the one the
    // browser's bubble used to hide.
    const blockProblem = newBlock
      ? newBlockName.trim() === ''
        ? 'Enter a name for the new block.'
        : undefined
      : form.target === 'collection'
        ? form.collection_id
          ? undefined
          : 'Choose a collection.'
        : form.block_id
          ? undefined
          : 'Select a block.'
    if (blockProblem) {
      if (newBlock) found.newBlockName = blockProblem
      else if (form.target === 'collection') found.collection = blockProblem
      else found.block = blockProblem
    }
    const collectionProblem = newBlock
      ? (validateCollectionChoice(newBlockCollection) ??
        (newBlockCollection && newBlockPlayback.order === 'interleaved'
          ? validateInterleave(newBlockPlayback.interleave)
          : undefined))
      : undefined
    setErrors(found)
    setError(collectionProblem)
    if (collectionProblem || Object.keys(found).length > 0) return
    mutation.mutate({ payload: buildPayload(form, timeZone), openEditor })
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    submit(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-md" closeOnOutsideClick={false}>
        <form onSubmit={handleSubmit} noValidate>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit schedule item' : 'New schedule item'}</DialogTitle>
            <DialogDescription>
              {isEditing
                ? 'Update this recurrence rule. '
                : 'Add a block, or a collection played as it is, airing at a given time to this schedule. '}
              {tzLabel(timeZone)}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            {form.target === 'collection' ? (
              <>
                <Field data-invalid={!!errors.collection}>
                  <FieldLabel>
                    Collection <span className="text-destructive">*</span>
                  </FieldLabel>
                  <CollectionPicker
                    value={form.collection_id}
                    onChange={(id) => {
                      setForm((f) => ({ ...f, collection_id: id }))
                      setErrors((current) => ({ ...current, collection: undefined }))
                    }}
                    idPrefix="schedule-item-collection"
                  />
                  <FieldError id="schedule-item-collection-error">{errors.collection}</FieldError>
                  <FieldDescription>
                    No block in between: the catalog items the collection matches air here directly, and follow the
                    catalog as it syncs.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="schedule-item-order">Order</FieldLabel>
                  <Select
                    value={form.playback_order}
                    onValueChange={(v) => setForm((f) => ({ ...f, playback_order: v as CriteriaOrder }))}
                  >
                    <SelectTrigger id="schedule-item-order" className="w-full" data-testid="schedule-item-order">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {COLLECTION_ITEM_ORDERS.map((order) => (
                        <SelectItem key={order} value={order}>
                          {CRITERIA_ORDER_LABELS[order]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  className="self-start px-0"
                  data-testid="target-block-toggle"
                  onClick={() => setForm((f) => ({ ...f, target: 'block' }))}
                >
                  Use a block instead
                </Button>
              </>
            ) : !newBlock ? (
              <Field data-invalid={!!errors.block}>
                <FieldLabel htmlFor="schedule-item-block">
                  Block <span className="text-destructive">*</span>
                </FieldLabel>
                <Select
                  value={form.block_id}
                  onValueChange={(v) => {
                    setForm((f) => ({ ...f, block_id: v }))
                    setErrors((current) => ({ ...current, block: undefined }))
                  }}
                >
                  <SelectTrigger
                    id="schedule-item-block"
                    className="w-full"
                    aria-invalid={!!errors.block}
                    aria-describedby={errors.block ? 'schedule-item-block-error' : undefined}
                  >
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
                <FieldError id="schedule-item-block-error">{errors.block}</FieldError>
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  className="self-start px-0"
                  data-testid="target-collection-toggle"
                  onClick={() => setForm((f) => ({ ...f, target: 'collection' }))}
                >
                  Fill it from a collection instead
                </Button>
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
                <Field data-invalid={!!errors.newBlockName}>
                  <FieldLabel htmlFor="schedule-item-new-block-name">
                    New block name <span className="text-destructive">*</span>
                  </FieldLabel>
                  <Input
                    id="schedule-item-new-block-name"
                    aria-invalid={!!errors.newBlockName}
                    aria-describedby={errors.newBlockName ? 'schedule-item-new-block-name-error' : undefined}
                    value={newBlockName}
                    onChange={(e) => {
                      setNewBlockName(e.target.value)
                      setErrors((current) => ({ ...current, newBlockName: undefined }))
                    }}
                  />
                  <FieldError id="schedule-item-new-block-name-error">{errors.newBlockName}</FieldError>
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
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  className="self-start px-0"
                  data-testid="target-collection-toggle"
                  onClick={() => {
                    setNewBlock(false)
                    setForm((f) => ({ ...f, target: 'collection' }))
                  }}
                >
                  Fill it from a collection instead
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
                  aria-pressed={form.recurrence_type === 'weekly'}
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'weekly' }))}
                >
                  Weekly
                </Button>
                <Button
                  type="button"
                  variant={form.recurrence_type === 'once' ? 'default' : 'outline'}
                  size="sm"
                  aria-pressed={form.recurrence_type === 'once'}
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'once' }))}
                >
                  Once
                </Button>
                <Button
                  type="button"
                  variant={form.recurrence_type === 'continuous' ? 'default' : 'outline'}
                  size="sm"
                  aria-pressed={form.recurrence_type === 'continuous'}
                  className="flex-1"
                  onClick={() => setForm((f) => ({ ...f, recurrence_type: 'continuous' }))}
                >
                  Continuous
                </Button>
              </div>
              <FieldError id="schedule-item-recurrence-error">{errors.recurrence}</FieldError>
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
                  <FieldLabel>Days of the week</FieldLabel>
                  <div className="flex gap-1" role="group" aria-label="Days of the week" data-testid="weekday-picker">
                    {WEEK_ORDER.map((day) => {
                      const selected = form.days_of_week.includes(day)
                      return (
                        <Button
                          key={day}
                          type="button"
                          size="sm"
                          variant={selected ? 'default' : 'outline'}
                          className="flex-1 px-0"
                          aria-pressed={selected}
                          aria-label={DAY_LABELS[day]}
                          title={DAY_LABELS[day]}
                          data-testid={`weekday-${day}`}
                          onClick={() =>
                            setForm((f) => ({
                              ...f,
                              days_of_week: selected ? f.days_of_week.filter((d) => d !== day) : [...f.days_of_week, day],
                            }))
                          }
                        >
                          {DAY_SHORT_LABELS[day]}
                        </Button>
                      )
                    })}
                  </div>
                  <FieldError id="schedule-item-days-error">{errors.days}</FieldError>
                  <div className="flex gap-2">
                    {(
                      [
                        ['every-day', 'Every day', WEEKDAY_SHORTCUTS.everyDay],
                        ['weekdays', 'Weekdays', WEEKDAY_SHORTCUTS.weekdays],
                        ['weekend', 'Weekend', WEEKDAY_SHORTCUTS.weekend],
                      ] as const
                    ).map(([key, label, days]) => (
                      <Button
                        key={key}
                        type="button"
                        variant="ghost"
                        size="sm"
                        data-testid={`weekday-shortcut-${key}`}
                        onClick={() => setForm((f) => ({ ...f, days_of_week: [...days] }))}
                      >
                        {label}
                      </Button>
                    ))}
                  </div>
                </Field>
                <Field data-invalid={!!errors.start_time}>
                  <FieldLabel htmlFor="schedule-item-time">Time (HH:MM)</FieldLabel>
                  <Input
                    id="schedule-item-time"
                    type="time"
                    aria-invalid={!!errors.start_time}
                    aria-describedby={errors.start_time ? 'schedule-item-time-error' : undefined}
                    value={form.start_time_local}
                    onChange={(e) => {
                      setForm((f) => ({ ...f, start_time_local: e.target.value }))
                      setErrors((current) => ({ ...current, start_time: undefined }))
                    }}
                  />
                  <FieldError id="schedule-item-time-error">{errors.start_time}</FieldError>
                </Field>
              </>
            ) : (
              <Field data-invalid={!!errors.start_at}>
                <FieldLabel htmlFor="schedule-item-start-at">Date and time</FieldLabel>
                <Input
                  id="schedule-item-start-at"
                  type="datetime-local"
                  aria-invalid={!!errors.start_at}
                  aria-describedby={errors.start_at ? 'schedule-item-start-at-error' : undefined}
                  value={form.start_at_local}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, start_at_local: e.target.value }))
                    setErrors((current) => ({ ...current, start_at: undefined }))
                  }}
                />
                <FieldError id="schedule-item-start-at-error">{errors.start_at}</FieldError>
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

            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
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
