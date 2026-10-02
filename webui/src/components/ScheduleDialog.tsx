import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { api } from '@/lib/http'
import type { Schedule, ScheduleInput } from '@/lib/types'

interface ScheduleDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing schedule, absent when creating one.
  schedule?: Schedule
  // Called with the saved schedule (e.g. to select a freshly created one).
  onSaved?: (schedule: Schedule) => void
}

const emptyForm: ScheduleInput = {
  name: '',
  description: null,
}

export function ScheduleDialog({ open, onOpenChange, schedule, onSaved }: ScheduleDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<ScheduleInput>(emptyForm)
  const isEditing = schedule !== undefined

  useEffect(() => {
    if (open) {
      setForm(schedule ? { name: schedule.name, description: schedule.description } : emptyForm)
    }
  }, [open, schedule])

  const mutation = useMutation({
    mutationFn: () =>
      isEditing
        ? api.put<Schedule>(`/schedules/${schedule.id}`, form)
        : api.post<Schedule>('/schedules', form),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      toast.success(isEditing ? 'Schedule updated.' : 'Schedule created.')
      onOpenChange(false)
      onSaved?.(saved)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the schedule.' : 'Could not create the schedule.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    mutation.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit schedule' : 'New schedule'}</DialogTitle>
            <DialogDescription>
              {isEditing
                ? 'Rename this schedule or change its description.'
                : 'A reusable, named group of recurrence rules. It is selected for the channel when created; save the channel settings to assign it.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="schedule-name">Name</FieldLabel>
              <Input
                id="schedule-name"
                required
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="schedule-description">Description (optional)</FieldLabel>
              <Input
                id="schedule-description"
                value={form.description ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value || null }))}
              />
            </Field>
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
