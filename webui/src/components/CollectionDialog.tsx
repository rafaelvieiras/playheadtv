import { type FormEvent, useEffect, useState } from 'react'
import { toast } from 'sonner'

import { CollectionFields, type CollectionDraft } from '@/components/CollectionFields'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { ApiError } from '@/lib/http'
import { useCreateCollection } from '@/lib/collections'
import { emptyQuery, validateQuery } from '@/lib/query'
import type { Collection } from '@/lib/types'

const emptyDraft = (): CollectionDraft => ({ name: '', description: '', query: emptyQuery() })

// Create a collection. Editing happens on its own page (/collections/:id),
// where the users of the collection are shown before a change is saved.
export function CollectionDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (collection: Collection) => void
}) {
  const [draft, setDraft] = useState<CollectionDraft>(emptyDraft)
  const [error, setError] = useState<string | undefined>(undefined)
  const create = useCreateCollection()

  useEffect(() => {
    if (open) {
      setDraft(emptyDraft())
      setError(undefined)
    }
  }, [open])

  function handleSubmit(e: FormEvent) {
    // Nested inside another form's React tree (a block dialog, say)? Never
    // submit the outer one.
    e.preventDefault()
    e.stopPropagation()
    const invalid = (draft.name.trim() === '' ? 'Name is required.' : undefined) ?? validateQuery(draft.query)
    if (invalid) {
      setError(invalid)
      return
    }
    setError(undefined)
    create.mutate(
      { name: draft.name.trim(), description: draft.description.trim() || null, query: draft.query },
      {
        onSuccess: (collection) => {
          toast.success('Collection created.')
          onOpenChange(false)
          onCreated(collection)
        },
        onError: (err) => setError(err instanceof ApiError ? err.message : 'Could not create the collection.'),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={handleSubmit} data-testid="collection-dialog">
          <DialogHeader>
            <DialogTitle>New collection</DialogTitle>
            <DialogDescription>
              A named query over the catalog that blocks, filler pools and criteria items can play.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <CollectionFields value={draft} onChange={setDraft} idPrefix="new-collection" />
            {error && (
              <p className="text-sm text-destructive" data-testid="collection-dialog-error">
                {error}
              </p>
            )}
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending} data-testid="collection-dialog-create">
              {create.isPending ? 'Creating…' : 'Create'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
