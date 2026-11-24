import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { FillerPoolDialog } from '@/components/FillerPoolDialog'
import { FillerPoolItems } from '@/components/FillerPoolItems'
import { Loading, QueryError } from '@/components/QueryError'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  KIND_LABELS,
  MAX_FIT_ATTEMPTS_MAX,
  MAX_FIT_ATTEMPTS_MIN,
  SCOPE_LABELS,
  STRATEGY_LABELS,
  validMaxFitAttempts,
} from '@/lib/filler'
import { api, ApiError } from '@/lib/http'
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

const NEW_ID = 'new'

const emptyDraft: FillerRuleInput = {
  scope_type: 'channel',
  scope_id: '',
  kind: 'midroll',
  filler_pool_id: '',
  selection_strategy: 'sequential',
  position: 0,
  max_fit_attempts: 1,
}

function ruleToInput(rule: FillerRule): FillerRuleInput {
  return {
    scope_type: rule.scope_type,
    scope_id: rule.scope_id,
    kind: rule.kind,
    filler_pool_id: rule.filler_pool_id,
    selection_strategy: rule.selection_strategy,
    position: rule.position,
    max_fit_attempts: rule.max_fit_attempts ?? 1,
  }
}

function sameInput(a: FillerRuleInput, b: FillerRuleInput): boolean {
  return (
    a.scope_type === b.scope_type &&
    a.scope_id === b.scope_id &&
    a.kind === b.kind &&
    a.filler_pool_id === b.filler_pool_id &&
    a.selection_strategy === b.selection_strategy &&
    a.position === b.position &&
    a.max_fit_attempts === b.max_fit_attempts
  )
}

export function FillerRuleEditorPage() {
  const { id = '' } = useParams()
  const isNew = id === NEW_ID
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: rule, isLoading, error, refetch } = useQuery({
    queryKey: ['filler-rules', id],
    queryFn: () => api.get<FillerRule>(`/filler-rules/${id}`),
    enabled: !isNew,
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const { data: pools } = useQuery({ queryKey: ['filler-pools'], queryFn: () => api.get<FillerPool[]>('/filler-pools') })

  // Optional prefill for create (e.g. from a channel's Fillers section):
  // /filler-rules/new?scope_type=channel&scope_id=<id>&kind=fallback
  const initial: FillerRuleInput = (() => {
    const scope = searchParams.get('scope_type')
    const kind = searchParams.get('kind')
    return {
      ...emptyDraft,
      scope_type: scope === 'block' ? 'block' : 'channel',
      scope_id: searchParams.get('scope_id') ?? '',
      kind: kind && kind in KIND_LABELS ? (kind as FillerKind) : emptyDraft.kind,
    }
  })()

  const [draft, setDraft] = useState<FillerRuleInput | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [poolDialogOpen, setPoolDialogOpen] = useState(false)

  const save = useMutation({
    mutationFn: (payload: FillerRuleInput) =>
      isNew ? api.post<FillerRule>('/filler-rules', payload) : api.put<FillerRule>(`/filler-rules/${id}`, payload),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['filler-rules'] })
      setDraft(null)
      setFormError(null)
      toast.success(isNew ? 'Filler rule created.' : 'Filler rule saved.')
      queryClient.setQueryData(['filler-rules', saved.id], saved)
      if (isNew) navigate(`/filler-rules/${saved.id}`, { replace: true })
    },
    onError: (e) => setFormError(e instanceof ApiError ? e.message : 'Could not save the filler rule.'),
  })

  const breadcrumb = [{ label: 'Filler rules', to: '/filler-rules' }, { label: isNew ? 'New rule' : 'Filler rule' }]

  if (!isNew && error instanceof ApiError && error.status === 404) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Filler rule not found" breadcrumb={breadcrumb} />
        <p className="text-sm text-muted-foreground">This filler rule does not exist (it may have been deleted).</p>
      </div>
    )
  }
  if (!isNew && (isLoading || !rule)) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Filler rule" breadcrumb={breadcrumb} />
        {error ? (
          <QueryError subject="the filler rule" error={error} onRetry={() => void refetch()} />
        ) : (
          <Loading />
        )}
      </div>
    )
  }

  const loaded = rule ? ruleToInput(rule) : initial
  const current = draft ?? loaded
  const dirty = !isNew && !sameInput(current, loaded)
  const pool = pools?.find((p) => p.id === current.filler_pool_id)

  function patch(next: Partial<FillerRuleInput>) {
    setDraft({ ...current, ...next })
    setFormError(null)
  }

  function handleSave() {
    if (!current.scope_id) {
      setFormError(current.scope_type === 'channel' ? 'Select a channel.' : 'Select a block.')
      return
    }
    if (!current.filler_pool_id) {
      setFormError('Select a filler pool.')
      return
    }
    if (!Number.isInteger(current.position)) {
      setFormError('Position must be a whole number.')
      return
    }
    if (!validMaxFitAttempts(current.max_fit_attempts)) {
      setFormError(`Fit attempts must be a whole number from ${MAX_FIT_ATTEMPTS_MIN} to ${MAX_FIT_ATTEMPTS_MAX}.`)
      return
    }
    save.mutate(current)
  }

  const scopeOptions =
    current.scope_type === 'channel'
      ? (channels ?? []).map((c) => ({ value: c.id, label: `${c.number} — ${c.name}` }))
      : (blocks ?? []).map((b) => ({ value: b.id, label: b.name }))
  const poolOptions = (pools ?? []).map((p) => ({ value: p.id, label: p.name }))

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={isNew ? 'New filler rule' : `${KIND_LABELS[loaded.kind]} filler rule`}
        breadcrumb={breadcrumb}
      />

      <FieldGroup className="max-w-xl">
        <Field>
          <FieldLabel htmlFor="rule-scope-type">Scope</FieldLabel>
          <Select
            value={current.scope_type}
            onValueChange={(v) => patch({ scope_type: v as FillerScope, scope_id: '' })}
          >
            <SelectTrigger id="rule-scope-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(SCOPE_LABELS) as FillerScope[]).map((s) => (
                <SelectItem key={s} value={s}>
                  {SCOPE_LABELS[s]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field>
          <FieldLabel htmlFor="rule-scope-target">{SCOPE_LABELS[current.scope_type]}</FieldLabel>
          <Combobox
            id="rule-scope-target"
            options={scopeOptions}
            value={current.scope_id || null}
            onValueChange={(v) => patch({ scope_id: v })}
            placeholder={current.scope_type === 'channel' ? 'Select a channel' : 'Select a block'}
            searchPlaceholder="Search…"
          />
        </Field>

        <Field>
          <FieldLabel htmlFor="rule-kind">Type</FieldLabel>
          <Select value={current.kind} onValueChange={(v) => patch({ kind: v as FillerKind })}>
            <SelectTrigger id="rule-kind" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(KIND_LABELS) as FillerKind[]).map((k) => (
                <SelectItem key={k} value={k}>
                  {KIND_LABELS[k]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field>
          <FieldLabel htmlFor="rule-pool">Filler pool</FieldLabel>
          <div className="flex gap-2">
            <Combobox
              id="rule-pool"
              className="flex-1"
              options={poolOptions}
              value={current.filler_pool_id || null}
              onValueChange={(v) => patch({ filler_pool_id: v })}
              placeholder="Select a filler pool"
              searchPlaceholder="Search pools…"
            />
            <Button type="button" variant="outline" onClick={() => setPoolDialogOpen(true)}>
              New pool
            </Button>
          </div>
        </Field>

        <Field>
          <FieldLabel htmlFor="rule-strategy">Selection</FieldLabel>
          <Select
            value={current.selection_strategy}
            onValueChange={(v) => patch({ selection_strategy: v as SelectionStrategy })}
          >
            <SelectTrigger id="rule-strategy" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(STRATEGY_LABELS) as SelectionStrategy[]).map((s) => (
                <SelectItem key={s} value={s}>
                  {STRATEGY_LABELS[s]}
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
            step={1}
            value={Number.isNaN(current.position) ? '' : current.position}
            onChange={(e) => patch({ position: e.target.value === '' ? NaN : Number(e.target.value) })}
          />
          <FieldDescription>Order among rules of the same scope and type.</FieldDescription>
        </Field>

        {(current.kind === 'tail' || current.kind === 'fallback') && (
          <Field>
            <FieldLabel htmlFor="rule-fit-attempts">Fit attempts</FieldLabel>
            <Input
              id="rule-fit-attempts"
              data-testid="rule-fit-attempts"
              type="number"
              step={1}
              min={MAX_FIT_ATTEMPTS_MIN}
              max={MAX_FIT_ATTEMPTS_MAX}
              value={Number.isNaN(current.max_fit_attempts) ? '' : current.max_fit_attempts}
              onChange={(e) => patch({ max_fit_attempts: e.target.value === '' ? NaN : Number(e.target.value) })}
            />
            <FieldDescription>
              When an item does not fit the time left, try other items of the pool, up to this many misses in a row
              (1 to {MAX_FIT_ATTEMPTS_MAX}). 1 stops at the first miss. The fill keeps going with whatever fits while
              time is left; skipped items do not lose their turn in a sequential rotation.
            </FieldDescription>
          </Field>
        )}
      </FieldGroup>

      {pool && (
        <section className="flex flex-col gap-3" data-testid="rule-pool-panel">
          <div className="flex items-center gap-2">
            <h2 className="text-base font-medium">Pool: {pool.name}</h2>
            <Badge variant={pool.dynamic ? 'default' : 'secondary'}>{pool.dynamic ? 'Dynamic' : 'Manual'}</Badge>
            <Link to={`/filler-pools/${pool.id}`} target="_blank" className="text-sm underline">
              Edit pool
            </Link>
          </div>
          {pool.dynamic && (
            <p className="text-sm text-muted-foreground">Collection: {pool.collection_name ?? 'held for review (old query)'}</p>
          )}
          <FillerPoolItems poolId={pool.id} editable={false} />
        </section>
      )}

      {formError && isNew && <p className="text-sm text-destructive">{formError}</p>}
      {isNew ? (
        <div>
          <Button onClick={handleSave} disabled={save.isPending}>
            {save.isPending ? 'Creating…' : 'Create rule'}
          </Button>
        </div>
      ) : (
        <SaveBar
          dirty={dirty}
          saving={save.isPending}
          error={formError}
          onSave={handleSave}
          onDiscard={() => {
            setDraft(null)
            setFormError(null)
          }}
        />
      )}

      <FillerPoolDialog
        open={poolDialogOpen}
        onOpenChange={setPoolDialogOpen}
        onCreated={(created) => patch({ filler_pool_id: created.id })}
      />
    </div>
  )
}
