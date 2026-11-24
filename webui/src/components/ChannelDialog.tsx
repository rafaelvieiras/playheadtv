import { type FormEvent, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, ApiError } from '@/lib/http'
import type { Channel, ChannelInput, Schedule } from '@/lib/types'

interface ChannelDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// NONE_VALUE stands in for schedule_id: null — Radix Select can't use an
// empty string as an item value (reserved to clear/placeholder internally).
const NONE_VALUE = '__none__'

// The number is the only field kept as text while it is typed: a numeric state
// turns a cleared input into "0", and "0" is exactly what the field must never
// claim.
type FormState = Omit<ChannelInput, 'number'>

type FieldKey = 'number' | 'name' | 'timezone'
type Errors = Partial<Record<FieldKey, string>>

// A whole number, 1 or more. The backend refuses anything else too, but the
// form must not rely on that: a round trip to learn "0 is not a channel number"
// is a round trip that leaves the draft open over a message in the wrong place.
function parseNumber(text: string): number | null {
  const trimmed = text.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const value = Number(trimmed)
  return Number.isSafeInteger(value) && value >= 1 ? value : null
}

// The browser's own zone. A new channel used to start at UTC, which is a
// sensible storage default and a bad guess for a channel an operator in another
// zone is about to write weekly times in.
function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

let cachedZones: string[] | undefined

// Every zone this engine knows about. Intl.supportedValuesOf is the standard
// list; an engine without it can only offer its own zone, which is still
// better than the free-text field this replaced.
function timeZoneChoices(): string[] {
  if (cachedZones) return cachedZones
  let values: string[] = []
  try {
    values = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  } catch {
    values = []
  }
  cachedZones = values.length > 0 ? values : [browserTimeZone()]
  return cachedZones
}

function newChannelForm(): FormState {
  return {
    name: '',
    guide_art_url: null,
    timezone: browserTimeZone(),
    schedule_id: null,
    base_schedule_id: null,
  }
}

// Every problem at once, keyed by the field that has it: the form used to
// report one at a time (fixing the name revealed the timezone), and the
// browser's own bubble pointed at a field the operator had not forgotten.
function validate(number: number | null, form: FormState): Errors {
  const errors: Errors = {}
  if (number === null) {
    errors.number = 'Enter a whole number, 1 or more.'
  }
  if (form.name.trim() === '') {
    errors.name = 'Name is required.'
  }
  if (form.timezone.trim() === '') {
    errors.timezone = 'Choose a timezone.'
  }
  return errors
}

// Which field a refusal is about. The backend answers a number it already holds
// with 409 "already exists" and a number below 1 with 400 "number must be at
// least 1"; both belong under the number, not in a banner that replaced them
// with "Could not create the channel.".
function serverErrorField(error: unknown): FieldKey | undefined {
  if (!(error instanceof ApiError)) return undefined
  const message = error.message.toLowerCase()
  if (error.status === 409 && message.includes('already exists')) return 'number'
  if (error.status === 400) {
    if (message.includes('number')) return 'number'
    if (message.includes('timezone')) return 'timezone'
    if (message.includes('name')) return 'name'
  }
  return undefined
}

export function ChannelDialog({ open, onOpenChange }: ChannelDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeOnOutsideClick={false}>
        {/* The form is mounted only while the dialog is open, so the draft, its
            messages and the server's last refusal die with it: the next "New
            channel" never greets the operator with the one that failed. */}
        <ChannelForm onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function ChannelForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [numberText, setNumberText] = useState('1')
  const [form, setForm] = useState<FormState>(newChannelForm)
  const [errors, setErrors] = useState<Errors>({})

  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
  })

  const mutation = useMutation({
    mutationFn: (input: ChannelInput) => api.post<Channel>('/channels', input),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Channel created.')
      onClose()
      navigate(`/channels/${created.id}`)
    },
  })

  // The list is exactly what the engine supports, plus the value the form
  // already holds: a stored zone the engine no longer lists must still be shown
  // (and kept) instead of silently reading as empty.
  const timeZoneOptions = useMemo(() => {
    const known = timeZoneChoices()
    const stored = form.timezone
    const extra = stored !== '' && !known.includes(stored) ? [stored] : []
    return [...extra, ...known].map((zone) => ({ value: zone, label: zone }))
  }, [form.timezone])

  function clearServerError() {
    // A refusal stops being true the moment the value behind it changes, and
    // that includes the server's ("already exists").
    if (mutation.isError) mutation.reset()
  }

  function touchField(key: FieldKey) {
    clearServerError()
    setErrors((current) => (key in current ? { ...current, [key]: undefined } : current))
  }

  function set<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((current) => ({ ...current, [key]: value }))
    if (key === 'name' || key === 'timezone') touchField(key)
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const number = parseNumber(numberText)
    const found = validate(number, form)
    setErrors(found)
    if (Object.values(found).some(Boolean) || number === null) return
    mutation.mutate({ ...form, number, name: form.name.trim() })
  }

  const failedField = serverErrorField(mutation.error)
  const serverMessage = mutation.error instanceof ApiError ? mutation.error.message : undefined
  // One place per problem: a refusal the form can place under a field is shown
  // there (the field's own message wins while it is still being fixed).
  const shown: Errors = { ...errors }
  if (failedField && serverMessage && !shown[failedField]) shown[failedField] = serverMessage
  const submitError =
    mutation.isError && !failedField ? (serverMessage ?? 'Could not create the channel.') : undefined

  return (
    // noValidate: the app reports the problems, under the field they belong to,
    // instead of the browser's one bubble at a time.
    <form onSubmit={handleSubmit} noValidate>
      <DialogHeader>
        <DialogTitle>New channel</DialogTitle>
        <DialogDescription>
          Create the channel, then set up its programming on its page.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="py-4">
        <Field data-invalid={!!shown.number}>
          <FieldLabel htmlFor="channel-number">
            Number <span className="text-destructive">*</span>
          </FieldLabel>
          <Input
            id="channel-number"
            type="number"
            inputMode="numeric"
            min={1}
            step={1}
            aria-invalid={!!shown.number}
            aria-describedby={shown.number ? 'channel-number-error' : undefined}
            value={numberText}
            onChange={(e) => {
              setNumberText(e.target.value)
              touchField('number')
            }}
          />
          <FieldError id="channel-number-error">{shown.number}</FieldError>
        </Field>
        <Field data-invalid={!!shown.name}>
          <FieldLabel htmlFor="channel-name">
            Name <span className="text-destructive">*</span>
          </FieldLabel>
          <Input
            id="channel-name"
            aria-invalid={!!shown.name}
            aria-describedby={shown.name ? 'channel-name-error' : undefined}
            value={form.name}
            onChange={(e) => set('name', e.target.value)}
          />
          <FieldError id="channel-name-error">{shown.name}</FieldError>
        </Field>
        <Field>
          <FieldLabel htmlFor="channel-guide-art">Guide art URL (optional)</FieldLabel>
          <Input
            id="channel-guide-art"
            type="url"
            value={form.guide_art_url ?? ''}
            onChange={(e) => set('guide_art_url', e.target.value || null)}
          />
        </Field>
        <Field data-invalid={!!shown.timezone}>
          <FieldLabel htmlFor="channel-timezone">
            Timezone <span className="text-destructive">*</span>
          </FieldLabel>
          {/* A searchable list, not free text: a typo here shifts every
              time this channel ever airs. */}
          <Combobox
            id="channel-timezone"
            options={timeZoneOptions}
            value={form.timezone}
            onValueChange={(zone) => set('timezone', zone)}
            placeholder="Choose a timezone"
            searchPlaceholder="Search a zone, for example Europe/Lisbon"
            emptyMessage="No zone matches."
            className={shown.timezone ? 'border-destructive' : undefined}
          />
          <FieldError id="channel-timezone-error">{shown.timezone}</FieldError>
        </Field>
        <Field>
          <FieldLabel htmlFor="channel-schedule">Schedule (optional)</FieldLabel>
          <Select
            value={form.schedule_id ?? NONE_VALUE}
            onValueChange={(v) => set('schedule_id', v === NONE_VALUE ? null : v)}
          >
            <SelectTrigger id="channel-schedule" className="w-full">
              <SelectValue placeholder="No schedule" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
              {schedules?.map((schedule) => (
                <SelectItem key={schedule.id} value={schedule.id}>
                  {schedule.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </FieldGroup>
      {submitError && (
        <p role="alert" className="mb-2 text-sm text-destructive">
          {submitError}
        </p>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? 'Saving…' : 'Save'}
        </Button>
      </DialogFooter>
    </form>
  )
}
