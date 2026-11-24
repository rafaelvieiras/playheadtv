import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { Loading, QueryError } from '@/components/QueryError'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  BREAK_KIND_LABELS,
  MAX_BREAK_NAME,
  MAX_BREAK_STEPS,
  MAX_BREAK_TARGET_MINUTES,
  STEP_TYPE_LABELS,
  draftProblems,
  mapTemplateMessage,
  draftToInput,
  emptyDraft,
  emptyStep,
  moveStep,
  problemsById,
  sameDraft,
  summarizeDraft,
  templateToDraft,
  type StepDraft,
  type TemplateDraft,
  type TemplateProblem,
} from '@/lib/breakTemplate'
import { MAX_FIT_ATTEMPTS_MAX, MAX_FIT_ATTEMPTS_MIN, SCOPE_LABELS, STRATEGY_LABELS } from '@/lib/filler'
import { api, ApiError } from '@/lib/http'
import type {
  Block,
  BreakKind,
  BreakStepType,
  BreakTemplate,
  Channel,
  FillerPool,
  FillerScope,
  SelectionStrategy,
} from '@/lib/types'

const NEW_ID = 'new'

export function BreakTemplateEditorPage() {
  const { id = '' } = useParams()
  const isNew = id === NEW_ID
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: template, isLoading, error, refetch } = useQuery({
    queryKey: ['break-templates', id],
    queryFn: () => api.get<BreakTemplate>(`/break-templates/${id}`),
    enabled: !isNew,
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const { data: pools } = useQuery({ queryKey: ['filler-pools'], queryFn: () => api.get<FillerPool[]>('/filler-pools') })

  // Optional prefill for create: /break-templates/new?scope_type=channel&scope_id=<id>&kind=midroll
  const [initial] = useState<TemplateDraft>(() => {
    const kind = searchParams.get('kind')
    return {
      ...emptyDraft(),
      scope_type: searchParams.get('scope_type') === 'block' ? 'block' : 'channel',
      scope_id: searchParams.get('scope_id') ?? '',
      kind: kind && kind in BREAK_KIND_LABELS ? (kind as BreakKind) : 'midroll',
    }
  })

  const [draft, setDraft] = useState<TemplateDraft | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  // Field problems are reported from the first save attempt on, and recomputed
  // on every edit, so a message disappears as soon as its value changes.
  const [submitted, setSubmitted] = useState(false)
  // A refusal from the server, bound to the field it names.
  const [serverProblems, setServerProblems] = useState<TemplateProblem[]>([])

  const save = useMutation({
    mutationFn: (payload: ReturnType<typeof draftToInput>) =>
      isNew
        ? api.post<BreakTemplate>('/break-templates', payload)
        : api.put<BreakTemplate>(`/break-templates/${id}`, payload),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['break-templates'] })
      setDraft(null)
      setFormError(null)
      setSubmitted(false)
      setServerProblems([])
      toast.success(isNew ? 'Break template created.' : 'Break template saved.')
      queryClient.setQueryData(['break-templates', saved.id], saved)
      if (isNew) navigate(`/break-templates/${saved.id}`, { replace: true })
    },
    onError: (e) => {
      // The server names the step and the field in milliseconds: put the
      // message under that field in minutes instead of only in the save bar.
      const problem = e instanceof ApiError && e.status === 400 ? mapTemplateMessage(e.message) : null
      setServerProblems(problem ? [problem] : [])
      setFormError(problem ? null : e instanceof ApiError ? e.message : 'Could not save the break template.')
    },
  })

  const breadcrumb = [{ label: 'Break templates', to: '/break-templates' }, { label: isNew ? 'New template' : 'Break template' }]

  if (!isNew && error instanceof ApiError && error.status === 404) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Break template not found" breadcrumb={breadcrumb} />
        <p className="text-sm text-muted-foreground">This break template does not exist (it may have been deleted).</p>
      </div>
    )
  }
  if (!isNew && (isLoading || !template)) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Break template" breadcrumb={breadcrumb} />
        {error ? (
          <QueryError subject="the break template" error={error} onRetry={() => void refetch()} />
        ) : (
          <Loading />
        )}
      </div>
    )
  }

  const loaded = template ? templateToDraft(template) : initial
  const current = draft ?? loaded
  const dirty = !isNew && draft !== null && !sameDraft(current, loaded)
  // Recomputed on every edit (the lists are short), so a message disappears
  // the moment its value changes.
  const found = submitted ? draftProblems(current) : []
  const foundIds = new Set(found.map((p) => p.id))
  const problems = [...found, ...serverProblems.filter((p) => !foundIds.has(p.id))]
  const errors = problemsById(problems)

  // field carries the aria wiring of one control and the id of its message.
  function field(id: string) {
    const message = errors[id]
    return {
      invalid: !!message,
      errorId: `${id}-error`,
      props: {
        'aria-invalid': !!message,
        'aria-describedby': message ? `${id}-error` : undefined,
      },
    }
  }

  function patch(next: Partial<TemplateDraft>) {
    setDraft({ ...current, ...next })
    setFormError(null)
    setServerProblems([])
  }
  function patchStep(index: number, next: Partial<StepDraft>) {
    patch({ steps: current.steps.map((s, i) => (i === index ? { ...s, ...next } : s)) })
  }

  function handleSave() {
    // Every problem in one submit, each under its own field.
    setSubmitted(true)
    setServerProblems([])
    setFormError(null)
    if (draftProblems(current).length > 0) return
    save.mutate(draftToInput(current))
  }

  const scopeOptions =
    current.scope_type === 'channel'
      ? (channels ?? []).map((c) => ({ value: c.id, label: `${c.number} — ${c.name}` }))
      : (blocks ?? []).map((b) => ({ value: b.id, label: b.name }))
  const poolOptions = (pools ?? []).map((p) => ({ value: p.id, label: p.name }))

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={isNew ? 'New break template' : 'Break template'} breadcrumb={breadcrumb} />

      <FieldGroup className="max-w-xl">
        <Field>
          <FieldLabel htmlFor="bt-scope-type">Scope</FieldLabel>
          <Select value={current.scope_type} onValueChange={(v) => patch({ scope_type: v as FillerScope, scope_id: '' })}>
            <SelectTrigger id="bt-scope-type" className="w-full">
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

        <Field data-invalid={field('bt-scope-target').invalid}>
          <FieldLabel htmlFor="bt-scope-target">{SCOPE_LABELS[current.scope_type]}</FieldLabel>
          <Combobox
            id="bt-scope-target"
            options={scopeOptions}
            value={current.scope_id || null}
            onValueChange={(v) => patch({ scope_id: v })}
            placeholder={current.scope_type === 'channel' ? 'Select a channel' : 'Select a block'}
            searchPlaceholder="Search…"
          />
          <FieldError id={field('bt-scope-target').errorId}>{errors['bt-scope-target']}</FieldError>
        </Field>

        <Field>
          <FieldLabel htmlFor="bt-kind">Boundary</FieldLabel>
          <Select value={current.kind} onValueChange={(v) => patch({ kind: v as BreakKind })}>
            <SelectTrigger id="bt-kind" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(BREAK_KIND_LABELS) as BreakKind[]).map((k) => (
                <SelectItem key={k} value={k}>
                  {BREAK_KIND_LABELS[k]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <FieldDescription>
            Replaces the single item a filler rule of this type inserts. A block template wins over a channel one, and
            a template over a plain rule at the same level.
          </FieldDescription>
        </Field>

        <Field data-invalid={field('bt-name').invalid}>
          <FieldLabel htmlFor="bt-name">Name</FieldLabel>
          <Input
            id="bt-name"
            value={current.name}
            maxLength={MAX_BREAK_NAME}
            onChange={(e) => patch({ name: e.target.value })}
            placeholder="Optional"
            {...field('bt-name').props}
          />
          <FieldError id={field('bt-name').errorId}>{errors['bt-name']}</FieldError>
        </Field>

        <Field data-invalid={field('bt-position').invalid}>
          <FieldLabel htmlFor="bt-position">Position</FieldLabel>
          <Input
            id="bt-position"
            type="number"
            step={1}
            value={Number.isNaN(current.position) ? '' : current.position}
            onChange={(e) => patch({ position: e.target.value === '' ? NaN : Number(e.target.value) })}
            {...field('bt-position').props}
          />
          <FieldError id={field('bt-position').errorId}>{errors['bt-position']}</FieldError>
          <FieldDescription>Order among templates of the same scope and type; the lowest wins.</FieldDescription>
        </Field>
      </FieldGroup>

      <section className="flex max-w-xl flex-col gap-3" data-testid="bt-steps">
        <h2 className="text-base font-medium">Steps</h2>
        <div className="rounded-lg border bg-muted/40 p-3" data-testid="bt-summary">
          <p className="text-sm" data-testid="bt-summary-text">
            {summarizeDraft(current)}
          </p>
          <p className="text-xs text-muted-foreground">
            The fill lengths are exact; each item's own length is whatever its pool hands over, so the total is at
            least that much.
          </p>
        </div>
        <FieldError id="bt-steps-error">{errors['bt-steps']}</FieldError>
        <p className="text-sm text-muted-foreground">
          Played in this order at every boundary. A fill step airs whole items of its pool until its length is filled
          (it can end a little short); the block's tail still lands the slot on the next scheduled item.
        </p>
        {current.steps.map((step, i) => (
          <div key={step.key} className="flex flex-col gap-3 rounded-lg border p-3" data-testid={`bt-step-${i}`}>
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">Step {i + 1}</span>
              <div className="flex gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Move step ${i + 1} up`}
                  data-testid={`bt-step-${i}-up`}
                  disabled={i === 0}
                  onClick={() => patch({ steps: moveStep(current.steps, i, -1) })}
                >
                  <ArrowUp />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Move step ${i + 1} down`}
                  data-testid={`bt-step-${i}-down`}
                  disabled={i === current.steps.length - 1}
                  onClick={() => patch({ steps: moveStep(current.steps, i, 1) })}
                >
                  <ArrowDown />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove step ${i + 1}`}
                  data-testid={`bt-step-${i}-remove`}
                  disabled={current.steps.length === 1}
                  onClick={() => patch({ steps: current.steps.filter((_, j) => j !== i) })}
                >
                  <Trash2 />
                </Button>
              </div>
            </div>

            <Field>
              <FieldLabel htmlFor={`bt-step-${i}-type`}>Type</FieldLabel>
              <Select value={step.step_type} onValueChange={(v) => patchStep(i, { step_type: v as BreakStepType })}>
                <SelectTrigger id={`bt-step-${i}-type`} data-testid={`bt-step-${i}-type`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(STEP_TYPE_LABELS) as BreakStepType[]).map((t) => (
                    <SelectItem key={t} value={t}>
                      {STEP_TYPE_LABELS[t]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field data-invalid={field(`bt-step-${i}-pool`).invalid}>
              <FieldLabel htmlFor={`bt-step-${i}-pool`}>Filler pool</FieldLabel>
              <Combobox
                id={`bt-step-${i}-pool`}
                options={poolOptions}
                value={step.filler_pool_id || null}
                onValueChange={(v) => patchStep(i, { filler_pool_id: v })}
                placeholder="Select a filler pool"
                searchPlaceholder="Search pools…"
              />
              <FieldError id={field(`bt-step-${i}-pool`).errorId}>{errors[`bt-step-${i}-pool`]}</FieldError>
            </Field>

            <Field>
              <FieldLabel htmlFor={`bt-step-${i}-selection`}>Selection</FieldLabel>
              <Select
                value={step.selection_strategy}
                onValueChange={(v) => patchStep(i, { selection_strategy: v as SelectionStrategy })}
              >
                <SelectTrigger id={`bt-step-${i}-selection`} className="w-full">
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

            {step.step_type === 'fill' && (
              <>
                <Field data-invalid={field(`bt-step-${i}-target`).invalid}>
                  <FieldLabel htmlFor={`bt-step-${i}-target`}>Length to fill (minutes)</FieldLabel>
                  <Input
                    id={`bt-step-${i}-target`}
                    data-testid={`bt-step-${i}-target`}
                    type="number"
                    step={0.5}
                    min={0.5}
                    max={MAX_BREAK_TARGET_MINUTES}
                    value={Number.isNaN(step.target_minutes) ? '' : step.target_minutes}
                    onChange={(e) => patchStep(i, { target_minutes: e.target.value === '' ? NaN : Number(e.target.value) })}
                    {...field(`bt-step-${i}-target`).props}
                  />
                  <FieldError id={field(`bt-step-${i}-target`).errorId}>{errors[`bt-step-${i}-target`]}</FieldError>
                  <FieldDescription>
                    More than 0 and up to {MAX_BREAK_TARGET_MINUTES} minutes. Whole items only, never cut short.
                  </FieldDescription>
                </Field>
                <Field data-invalid={field(`bt-step-${i}-attempts`).invalid}>
                  <FieldLabel htmlFor={`bt-step-${i}-attempts`}>Fit attempts</FieldLabel>
                  <Input
                    id={`bt-step-${i}-attempts`}
                    data-testid={`bt-step-${i}-attempts`}
                    type="number"
                    step={1}
                    min={MAX_FIT_ATTEMPTS_MIN}
                    max={MAX_FIT_ATTEMPTS_MAX}
                    value={Number.isNaN(step.max_fit_attempts) ? '' : step.max_fit_attempts}
                    onChange={(e) => patchStep(i, { max_fit_attempts: e.target.value === '' ? NaN : Number(e.target.value) })}
                    {...field(`bt-step-${i}-attempts`).props}
                  />
                  <FieldError id={field(`bt-step-${i}-attempts`).errorId}>{errors[`bt-step-${i}-attempts`]}</FieldError>
                  <FieldDescription>
                    How many candidates in a row the fill may reject for not being the right length before it gives up
                    ({MAX_FIT_ATTEMPTS_MIN} to {MAX_FIT_ATTEMPTS_MAX}).
                  </FieldDescription>
                </Field>
              </>
            )}
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            data-testid="bt-add-step"
            disabled={current.steps.length >= MAX_BREAK_STEPS}
            onClick={() => patch({ steps: [...current.steps, emptyStep()] })}
          >
            <Plus /> Add step
          </Button>
          {current.steps.length >= MAX_BREAK_STEPS && (
            <span className="ml-2 text-sm text-muted-foreground">At most {MAX_BREAK_STEPS} steps.</span>
          )}
        </div>
      </section>

      {/* The save bar carries the server's refusal on an existing template. */}
      {formError && isNew && (
        <p className="text-sm text-destructive" role="alert" data-testid="bt-form-error">
          {formError}
        </p>
      )}
      {isNew ? (
        <div>
          <Button onClick={handleSave} disabled={save.isPending} data-testid="bt-create">
            {save.isPending ? 'Creating…' : 'Create template'}
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
            setSubmitted(false)
            setServerProblems([])
          }}
        />
      )}
    </div>
  )
}
