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
import type { Channel, ChannelInput } from '@/lib/types'

interface ChannelDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing channel, absent when creating one.
  channel?: Channel
}

const emptyForm: ChannelInput = {
  number: 1,
  name: '',
  guide_art_url: null,
  timezone: 'UTC',
}

export function ChannelDialog({ open, onOpenChange, channel }: ChannelDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<ChannelInput>(emptyForm)
  const isEditing = channel !== undefined

  useEffect(() => {
    if (open) {
      setForm(
        channel
          ? {
              number: channel.number,
              name: channel.name,
              guide_art_url: channel.guide_art_url,
              timezone: channel.timezone,
            }
          : emptyForm
      )
    }
  }, [open, channel])

  const mutation = useMutation({
    mutationFn: () =>
      isEditing
        ? api.put<Channel>(`/channels/${channel.id}`, form)
        : api.post<Channel>('/channels', form),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success(isEditing ? 'Channel updated.' : 'Channel created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the channel.' : 'Could not create the channel.')
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
            <DialogTitle>{isEditing ? 'Edit channel' : 'New channel'}</DialogTitle>
            <DialogDescription>
              {isEditing ? 'Update the channel details.' : 'Fill in the details of the new channel.'}
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
