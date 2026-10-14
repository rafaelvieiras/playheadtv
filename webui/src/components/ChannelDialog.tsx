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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/lib/http'
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

export function ChannelDialog({ open, onOpenChange }: ChannelDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<ChannelInput>(emptyForm)
  const navigate = useNavigate()

  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
    enabled: open,
  })

  useEffect(() => {
    if (open) {
      setForm(emptyForm)
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
    onError: () => {
      toast.error('Could not create the channel.')
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
            <DialogTitle>New channel</DialogTitle>
            <DialogDescription>
              Create the channel, then set up its programming on its page.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="channel-number">Number</FieldLabel>
              <Input
                id="channel-number"
                type="number"
                required
                value={form.number}
                onChange={(e) => setForm((f) => ({ ...f, number: Number(e.target.value) }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="channel-name">Name</FieldLabel>
              <Input
                id="channel-name"
                required
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="channel-guide-art">Guide art URL (optional)</FieldLabel>
              <Input
                id="channel-guide-art"
                type="url"
                value={form.guide_art_url ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, guide_art_url: e.target.value || null }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="channel-timezone">Timezone</FieldLabel>
              <Input
                id="channel-timezone"
                required
                value={form.timezone}
                onChange={(e) => setForm((f) => ({ ...f, timezone: e.target.value }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="channel-schedule">Schedule (optional)</FieldLabel>
              <Select
                value={form.schedule_id ?? NONE_VALUE}
                onValueChange={(v) => setForm((f) => ({ ...f, schedule_id: v === NONE_VALUE ? null : v }))}
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
