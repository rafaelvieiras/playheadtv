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

const emptyForm: ChannelInput = {
  number: 1,
  name: '',
  guide_art_url: null,
  timezone: 'UTC',
  schedule_id: null,
  base_schedule_id: null,
}

type Errors = Partial<Record<'number' | 'name' | 'timezone', string>>

// Every problem at once, keyed by the field that has it: the form used to
// report one at a time (fixing the name revealed the timezone), and the
// browser's own bubble pointed at a field the operator had not forgotten.
function validate(form: ChannelInput): Errors {
  const errors: Errors = {}
  if (!Number.isInteger(form.number) || form.number < 1) {
    errors.number = 'Enter a whole number, 1 or more.'
  }
  if (form.name.trim() === '') {
    errors.name = 'Name is required.'
  }
  if (form.timezone.trim() === '') {
    errors.timezone = 'Timezone is required.'
  }
  return errors
}

export function ChannelDialog({ open, onOpenChange }: ChannelDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<ChannelInput>(emptyForm)
  const [errors, setErrors] = useState<Errors>({})
  const navigate = useNavigate()

  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
    enabled: open,
  })

  useEffect(() => {
    if (open) {
      setForm(emptyForm)
      setErrors({})
    }
  }, [open])

  const mutation = useMutation({
    mutationFn: () => api.post<Channel>('/channels', form),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Channel created.')
      onOpenChange(false)
      navigate(`/channels/${created.id}`)
    },
  })

  function set<K extends keyof ChannelInput>(key: K, value: ChannelInput[K]) {
    setForm((current) => ({ ...current, [key]: value }))
    // A message stops being true the moment the value behind it changes.
    if (key in errors) setErrors((current) => ({ ...current, [key]: undefined }))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const found = validate(form)
    setErrors(found)
    if (Object.values(found).some(Boolean)) return
    mutation.mutate()
  }

  const submitError =
    mutation.isError &&
    (mutation.error instanceof ApiError ? mutation.error.message : 'Could not create the channel.')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeOnOutsideClick={false}>
        {/* noValidate: the app reports the problems, under the field they
            belong to, instead of the browser's one bubble at a time. */}
        <form onSubmit={handleSubmit} noValidate>
          <DialogHeader>
            <DialogTitle>New channel</DialogTitle>
            <DialogDescription>
              Create the channel, then set up its programming on its page.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field data-invalid={!!errors.number}>
              <FieldLabel htmlFor="channel-number">
                Number <span className="text-destructive">*</span>
              </FieldLabel>
              <Input
                id="channel-number"
                type="number"
                inputMode="numeric"
                min={1}
                aria-invalid={!!errors.number}
                aria-describedby={errors.number ? 'channel-number-error' : undefined}
                value={form.number}
                onChange={(e) => set('number', Number(e.target.value))}
              />
              <FieldError id="channel-number-error">{errors.number}</FieldError>
            </Field>
            <Field data-invalid={!!errors.name}>
              <FieldLabel htmlFor="channel-name">
                Name <span className="text-destructive">*</span>
              </FieldLabel>
              <Input
                id="channel-name"
                aria-invalid={!!errors.name}
                aria-describedby={errors.name ? 'channel-name-error' : undefined}
                value={form.name}
                onChange={(e) => set('name', e.target.value)}
              />
              <FieldError id="channel-name-error">{errors.name}</FieldError>
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
            <Field data-invalid={!!errors.timezone}>
              <FieldLabel htmlFor="channel-timezone">
                Timezone <span className="text-destructive">*</span>
              </FieldLabel>
              <Input
                id="channel-timezone"
                aria-invalid={!!errors.timezone}
                aria-describedby={errors.timezone ? 'channel-timezone-error' : undefined}
                value={form.timezone}
                onChange={(e) => set('timezone', e.target.value)}
              />
              <FieldError id="channel-timezone-error">{errors.timezone}</FieldError>
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
