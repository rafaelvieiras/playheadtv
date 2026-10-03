import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { DynamicCollectionFields, validateCollectionChoice } from '@/components/DynamicCollectionFields'
import type { BlockPlaybackDraft } from '@/components/BlockPlaybackFields'
import { blockPlaybackPayload, DEFAULT_INTERLEAVE, validateInterleave } from '@/lib/interleave'
import { api } from '@/lib/http'
import { BlockListModeField } from '@/components/BlockListModeField'
import type { Block, BlockInput, ListMode } from '@/lib/types'

interface BlockDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const emptyForm: BlockInput = {
  name: '',
  description: null,
  collection_id: null,
}

const emptyPlayback: BlockPlaybackDraft = { order: 'title', interleave: DEFAULT_INTERLEAVE }

// Quick create only: editing a block happens on its own page (/blocks/:id),
// which this dialog navigates to after the block is created.
export function BlockDialog({ open, onOpenChange }: BlockDialogProps) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [form, setForm] = useState<BlockInput>(emptyForm)
  const [playback, setPlayback] = useState<BlockPlaybackDraft>(emptyPlayback)
  const [listMode, setListMode] = useState<ListMode>('repeat')
  const [error, setError] = useState<string | undefined>(undefined)

  useEffect(() => {
    if (open) {
      setListMode('repeat')
      setForm(emptyForm)
      setPlayback(emptyPlayback)
      setError(undefined)
    }
  }, [open])

  const mutation = useMutation({
    mutationFn: () => api.post<Block>('/blocks', {
        ...form,
        ...(form.collection_id ? blockPlaybackPayload(playback.order, playback.interleave) : { list_mode: listMode }),
      }),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('Block created.')
      onOpenChange(false)
      navigate(`/blocks/${created.id}`)
    },
    onError: () => {
      toast.error('Could not create the block.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const validationError =
      validateCollectionChoice(form.collection_id) ?? (form.collection_id && playback.order === 'interleaved' ? validateInterleave(playback.interleave) : undefined)
    if (validationError) {
      setError(validationError)
      return
    }
    setError(undefined)
    mutation.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>New block</DialogTitle>
            <DialogDescription>
              Create the block, then fill its items in the editor. A dynamic block plays a collection.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="block-name">Name</FieldLabel>
              <Input
                id="block-name"
                required
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="block-description">Description (optional)</FieldLabel>
              <Input
                id="block-description"
                value={form.description ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value || null }))}
              />
            </Field>

            <DynamicCollectionFields
              value={form.collection_id}
              onChange={(collection_id) => setForm((f) => ({ ...f, collection_id }))}
              idPrefix="block-collection"
              playback={{ value: playback, onChange: setPlayback }}
            />

            {!form.collection_id && <BlockListModeField value={listMode} onChange={setListMode} idPrefix="block" />}

            {error && <p className="text-sm text-destructive">{error}</p>}
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? 'Saving…' : 'Create'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
