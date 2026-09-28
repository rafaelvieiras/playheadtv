import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
import type {
  Block,
  Channel,
  FillerKind,
  FillerPool,
  FillerRule,
  FillerRuleInput,
  FillerScope,
  SelectionStrategy,
} from '@/lib/types'

interface FillerRuleDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing rule, absent when creating one.
  rule?: FillerRule
}

export const KIND_LABELS: Record<FillerKind, string> = {
  preroll: 'PreRoll',
  midroll: 'MidRoll',
  postroll: 'PostRoll',
  tail: 'Tail',
  fallback: 'Fallback',
}

export const SCOPE_LABELS: Record<FillerScope, string> = {
  channel: 'Channel',
  block: 'Block',
}

export const STRATEGY_LABELS: Record<SelectionStrategy, string> = {
  sequential: 'Sequential',
  random: 'Random',
  weighted: 'Weighted',
}

interface FormState {
  scope_type: FillerScope
  scope_id: string
  kind: FillerKind
  filler_pool_id: string
  selection_strategy: SelectionStrategy
  position: number
}

const emptyForm: FormState = {
  scope_type: 'channel',
  scope_id: '',
  kind: 'midroll',
  filler_pool_id: '',
  selection_strategy: 'sequential',
  position: 0,
}

function ruleToForm(rule: FillerRule): FormState {
  return {
    scope_type: rule.scope_type,
    scope_id: rule.scope_id,
    kind: rule.kind,
    filler_pool_id: rule.filler_pool_id,
    selection_strategy: rule.selection_strategy,
    position: rule.position,
  }
}

export function FillerRuleDialog({ open, onOpenChange, rule }: FillerRuleDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = rule !== undefined

  useEffect(() => {
    if (open) {
      setForm(rule ? ruleToForm(rule) : emptyForm)
      setError(undefined)
    }
  }, [open, rule])

  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
    enabled: open && form.scope_type === 'channel',
  })
  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
    enabled: open && form.scope_type === 'block',
  })
  const { data: pools } = useQuery({
    queryKey: ['filler-pools'],
    queryFn: () => api.get<FillerPool[]>('/filler-pools'),
    enabled: open,
  })

  const mutation = useMutation({
    mutationFn: (payload: FillerRuleInput) =>
      isEditing
        ? api.put<FillerRule>(`/filler-rules/${rule.id}`, payload)
        : api.post<FillerRule>('/filler-rules', payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['filler-rules'] })
      toast.success(isEditing ? 'Filler rule updated.' : 'Filler rule created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(isEditing ? 'Could not update the filler rule.' : 'Could not create the filler rule.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!form.scope_id) {
      setError(form.scope_type === 'channel' ? 'Select a channel.' : 'Select a block.')
      return
    }
    if (!form.filler_pool_id) {
      setError('Select a filler pool.')
      return
    }
    setError(undefined)
    mutation.mutate({
      scope_type: form.scope_type,
      scope_id: form.scope_id,
      kind: form.kind,
      filler_pool_id: form.filler_pool_id,
      selection_strategy: form.selection_strategy,
      position: form.position,
    })
  }

  function handleScopeTypeChange(scope_type: FillerScope) {
    setForm((f) => ({ ...f, scope_type, scope_id: '' }))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit filler rule' : 'New filler rule'}</DialogTitle>
            <DialogDescription>
              {isEditing
                ? 'Update the filler rule.'
                : 'Links a filler pool to a channel or block, at a point in the programming.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="rule-scope-type">Scope</FieldLabel>
              <Select value={form.scope_type} onValueChange={(v) => handleScopeTypeChange(v as FillerScope)}>
                <SelectTrigger id="rule-scope-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(SCOPE_LABELS) as FillerScope[]).map((scope) => (
                    <SelectItem key={scope} value={scope}>
                      {SCOPE_LABELS[scope]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            {form.scope_type === 'channel' ? (
              <Field>
                <FieldLabel htmlFor="rule-channel">Channel</FieldLabel>
                <Select value={form.scope_id} onValueChange={(v) => setForm((f) => ({ ...f, scope_id: v }))}>
                  <SelectTrigger id="rule-channel" className="w-full">
                    <SelectValue placeholder="Select a channel" />
                  </SelectTrigger>
                  <SelectContent>
                    {channels?.map((channel) => (
                      <SelectItem key={channel.id} value={channel.id}>
                        {channel.number} — {channel.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            ) : (
              <Field>
                <FieldLabel htmlFor="rule-block">Block</FieldLabel>
                <Select value={form.scope_id} onValueChange={(v) => setForm((f) => ({ ...f, scope_id: v }))}>
                  <SelectTrigger id="rule-block" className="w-full">
                    <SelectValue placeholder="Select a block" />
                  </SelectTrigger>
                  <SelectContent>
                    {blocks?.map((block) => (
                      <SelectItem key={block.id} value={block.id}>
                        {block.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}

            <Field>
              <FieldLabel htmlFor="rule-kind">Type</FieldLabel>
              <Select value={form.kind} onValueChange={(v) => setForm((f) => ({ ...f, kind: v as FillerKind }))}>
                <SelectTrigger id="rule-kind" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(KIND_LABELS) as FillerKind[]).map((kind) => (
                    <SelectItem key={kind} value={kind}>
                      {KIND_LABELS[kind]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field>
              <FieldLabel htmlFor="rule-pool">Filler pool</FieldLabel>
              <Select
                value={form.filler_pool_id}
                onValueChange={(v) => setForm((f) => ({ ...f, filler_pool_id: v }))}
              >
                <SelectTrigger id="rule-pool" className="w-full">
                  <SelectValue placeholder="Select a filler pool" />
                </SelectTrigger>
                <SelectContent>
                  {pools?.map((pool) => (
                    <SelectItem key={pool.id} value={pool.id}>
                      {pool.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field>
              <FieldLabel htmlFor="rule-strategy">Selection</FieldLabel>
              <Select
                value={form.selection_strategy}
                onValueChange={(v) => setForm((f) => ({ ...f, selection_strategy: v as SelectionStrategy }))}
              >
                <SelectTrigger id="rule-strategy" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(STRATEGY_LABELS) as SelectionStrategy[]).map((strategy) => (
                    <SelectItem key={strategy} value={strategy}>
                      {STRATEGY_LABELS[strategy]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field>
              <FieldLabel htmlFor="rule-position">Position</FieldLabel>
              <Input
                id="rule-position"
                type="number"
                required
                value={form.position}
                onChange={(e) => setForm((f) => ({ ...f, position: Number(e.target.value) }))}
              />
            </Field>

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
