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
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { DynamicCollectionFields, validateCollectionChoice, type CollectionChoice } from '@/components/DynamicCollectionFields'
import { api } from '@/lib/http'
import type { FillerPool, FillerPoolInput } from '@/lib/types'

interface FillerPoolDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Called with the created pool; the pool editor page is where its items
  // (or its collection) are worked on afterwards.
  onCreated: (pool: FillerPool) => void
}

// Quick create: a name and, optionally, a collection to play. Create only —
// editing a pool happens on its own page (/filler-pools/:id).
export function FillerPoolDialog({ open, onOpenChange, onCreated }: FillerPoolDialogProps) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [collection, setCollection] = useState<CollectionChoice>(null)
  const [nameError, setNameError] = useState<string | undefined>(undefined)
  const [error, setError] = useState<string | undefined>(undefined)

  useEffect(() => {
    if (open) {
      setName('')
      setCollection(null)
      setNameError(undefined)
      setError(undefined)
    }
  }, [open])

  const mutation = useMutation({
    mutationFn: (payload: FillerPoolInput) => api.post<FillerPool>('/filler-pools', payload),
    onSuccess: (pool) => {
      queryClient.invalidateQueries({ queryKey: ['filler-pools'] })
      toast.success('Filler pool created.')
      onOpenChange(false)
      onCreated(pool)
    },
    onError: () => toast.error('Could not create the filler pool.'),
  })

  function handleSubmit(e: FormEvent) {
    // Nested inside another page's form tree? Never submit the outer one.
    e.preventDefault()
    e.stopPropagation()
    const missingName = name.trim() === '' ? 'Name is required.' : undefined
    const validationError = validateCollectionChoice(collection)
    setNameError(missingName)
    setError(validationError)
    if (missingName || validationError) return
    mutation.mutate({ name: name.trim(), collection_id: collection })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeOnOutsideClick={false}>
        <form onSubmit={handleSubmit} noValidate>
          <DialogHeader>
            <DialogTitle>New filler pool</DialogTitle>
            <DialogDescription>
              A manual pool has items added one by one on its page; a dynamic pool is filled automatically from
              a collection.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field data-invalid={!!nameError}>
              <FieldLabel htmlFor="new-pool-name">
                Name <span className="text-destructive">*</span>
              </FieldLabel>
              <Input
                id="new-pool-name"
                aria-invalid={!!nameError}
                aria-describedby={nameError ? 'new-pool-name-error' : undefined}
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setNameError(undefined)
                }}
              />
              <FieldError id="new-pool-name-error">{nameError}</FieldError>
            </Field>

            <DynamicCollectionFields value={collection} onChange={setCollection} idPrefix="new-pool-collection" />

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
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? 'Creating…' : 'Create'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
