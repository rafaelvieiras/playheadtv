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
import { validateCriteria } from '@/lib/filler'
import { api } from '@/lib/http'
import type { DynamicCriteria, FillerPool, FillerPoolInput } from '@/lib/types'

interface FillerPoolDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Called with the created pool; the pool editor page is where its items
  // (or the criteria preview) are worked on afterwards.
  onCreated: (pool: FillerPool) => void
}

// Quick create: a name and, optionally, dynamic criteria. Create only —
// editing a pool happens on its own page (/filler-pools/:id).
export function FillerPoolDialog({ open, onOpenChange, onCreated }: FillerPoolDialogProps) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [criteria, setCriteria] = useState<DynamicCriteria | null>(null)
  const [error, setError] = useState<string | undefined>(undefined)

  useEffect(() => {
    if (open) {
      setName('')
      setCriteria(null)
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
    const validationError = validateCriteria(criteria)
    if (validationError) {
      setError(validationError)
      return
    }
    setError(undefined)
    mutation.mutate({ name: name.trim(), criteria })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>New filler pool</DialogTitle>
            <DialogDescription>
              A manual pool has items added one by one on its page; a dynamic pool is filled automatically from
              criteria.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="new-pool-name">Name</FieldLabel>
              <Input id="new-pool-name" required value={name} onChange={(e) => setName(e.target.value)} />
            </Field>

            <DynamicCriteriaFields value={criteria} onChange={setCriteria} idPrefix="new-pool-criteria" />

            {error && <p className="text-sm text-destructive">{error}</p>}
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
