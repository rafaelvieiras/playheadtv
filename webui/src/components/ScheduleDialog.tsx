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
}

const emptyForm: ScheduleInput = {
  name: '',
  description: null,
}

export function ScheduleDialog({ open, onOpenChange, schedule }: ScheduleDialogProps) {
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['schedules'] })
      toast.success(isEditing ? 'Schedule updated.' : 'Schedule created.')
      onOpenChange(false)
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
                ? 'Update this schedule. Manage its recurrence rules from "Items".'
                : 'A reusable, named group of recurrence rules — assign it to a channel from "Channels" once created.'}
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
