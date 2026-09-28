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
import { DynamicCriteriaFields } from '@/components/DynamicCriteriaFields'
import { api } from '@/lib/http'
import type { Block, BlockInput, DynamicCriteria } from '@/lib/types'

interface BlockDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing block, absent when creating one.
  block?: Block
}

const emptyForm: BlockInput = {
  name: '',
  description: null,
  criteria: null,
}

function validate(form: BlockInput): string | undefined {
  if (!form.criteria) return undefined
  const hasGenres = form.criteria.genres.length > 0
  const hasYearRange = form.criteria.year_from !== null || form.criteria.year_to !== null
  if (!hasGenres && !hasYearRange) {
    return 'Enter at least one genre or a year range for the dynamic criteria.'
  }
  return undefined
}

export function BlockDialog({ open, onOpenChange, block }: BlockDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<BlockInput>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = block !== undefined

  useEffect(() => {
    if (open) {
      setForm(
        block
          ? {
              name: block.name,
              description: block.description,
              criteria: block.criteria ? (JSON.parse(block.criteria) as DynamicCriteria) : null,
            }
          : emptyForm
      )
      setError(undefined)
    }
  }, [open, block])

  const mutation = useMutation({
    mutationFn: () =>
      isEditing
        ? api.put<Block>(`/blocks/${block.id}`, form)
        : api.post<Block>('/blocks', form),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success(isEditing ? 'Block updated.' : 'Block created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the block.' : 'Could not create the block.')
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
    mutation.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit block' : 'New block'}</DialogTitle>
            <DialogDescription>
              {isEditing ? 'Update the block details.' : 'Fill in the details of the new block.'}
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

            <DynamicCriteriaFields
              value={form.criteria}
              onChange={(criteria) => setForm((f) => ({ ...f, criteria }))}
              idPrefix="block-criteria"
            />

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
