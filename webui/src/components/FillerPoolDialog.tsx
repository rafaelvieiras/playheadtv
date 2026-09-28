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
import type { DynamicCriteria, FillerPool, FillerPoolInput } from '@/lib/types'

interface FillerPoolDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing pool, absent when creating one.
  pool?: FillerPool
}

interface FormState {
  name: string
  criteria: DynamicCriteria | null
}

const emptyForm: FormState = {
  name: '',
  criteria: null,
}

function poolToForm(pool: FillerPool): FormState {
  return {
    name: pool.name,
    criteria: pool.criteria ? (JSON.parse(pool.criteria) as DynamicCriteria) : null,
  }
}

function validate(form: FormState): string | undefined {
  if (!form.criteria) return undefined
  const hasGenres = form.criteria.genres.length > 0
  const hasYearRange = form.criteria.year_from !== null || form.criteria.year_to !== null
  if (!hasGenres && !hasYearRange) {
    return 'Enter at least one genre or a year range for the dynamic criteria.'
  }
  return undefined
}

export function FillerPoolDialog({ open, onOpenChange, pool }: FillerPoolDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = pool !== undefined

  useEffect(() => {
    if (open) {
      setForm(pool ? poolToForm(pool) : emptyForm)
      setError(undefined)
    }
  }, [open, pool])

  const mutation = useMutation({
    mutationFn: (payload: FillerPoolInput) =>
      isEditing
        ? api.put<FillerPool>(`/filler-pools/${pool.id}`, payload)
        : api.post<FillerPool>('/filler-pools', payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['filler-pools'] })
      toast.success(isEditing ? 'Filler pool updated.' : 'Filler pool created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the filler pool.' : 'Could not create the filler pool.')
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
    mutation.mutate({ name: form.name, criteria: form.criteria })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit filler pool' : 'New filler pool'}</DialogTitle>
            <DialogDescription>
              {isEditing
                ? 'Update the filler pool details.'
                : 'A manual pool has items added one by one; a dynamic pool is filled automatically from criteria.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="pool-name">Name</FieldLabel>
              <Input
                id="pool-name"
                required
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </Field>

            <DynamicCriteriaFields
              value={form.criteria}
              onChange={(criteria) => setForm((f) => ({ ...f, criteria }))}
              idPrefix="pool-criteria"
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
