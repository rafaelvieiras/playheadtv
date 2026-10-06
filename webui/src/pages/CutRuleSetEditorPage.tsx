import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { CutPreviewPanel } from '@/components/CutPreviewPanel'
import { CutStrategyFields } from '@/components/CutStrategyFields'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  CUT_KIND_LABELS,
  MAX_CUT_NAME,
  MAX_CUT_RULES,
  draftToConfig,
  draftToInput,
  emptyDraft,
  emptyRule,
  moveItem,
  sameDraft,
  setToDraft,
  validateDraft,
  type GuardsDraft,
  type HasChaptersChoice,
  type RuleDraft,
  type RuleSetDraft,
} from '@/lib/cutRules'
import { SCOPE_LABELS } from '@/lib/filler'
import { api, ApiError } from '@/lib/http'
import type { Block, Channel, CutItemKind, CutRuleSet, FillerScope } from '@/lib/types'

const NEW_ID = 'new'

export function CutRuleSetEditorPage() {
  const { id = '' } = useParams()
  const isNew = id === NEW_ID
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: ruleSet, isLoading, error } = useQuery({
    queryKey: ['cut-rule-sets', id],
    queryFn: () => api.get<CutRuleSet>(`/cut-rule-sets/${id}`),
    enabled: !isNew,
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })

  // Optional prefill for create: /cut-rule-sets/new?scope_type=channel&scope_id=<id>
  const [initial] = useState<RuleSetDraft>(() => ({
    ...emptyDraft(),
    scope_type: searchParams.get('scope_type') === 'block' ? 'block' : 'channel',
    scope_id: searchParams.get('scope_id') ?? '',
  }))

  const [draft, setDraft] = useState<RuleSetDraft | null>(null)
  const [formError, setFormError] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (payload: ReturnType<typeof draftToInput>) =>
      isNew
        ? api.post<CutRuleSet>('/cut-rule-sets', payload)
        : api.put<CutRuleSet>(`/cut-rule-sets/${id}`, payload),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['cut-rule-sets'] })
      setDraft(null)
      setFormError(null)
      toast.success(isNew ? 'Cut rule set created.' : 'Cut rule set saved.')
      queryClient.setQueryData(['cut-rule-sets', saved.id], saved)
      if (isNew) navigate(`/cut-rule-sets/${saved.id}`, { replace: true })
    },
    onError: (e) => setFormError(e instanceof ApiError ? e.message : 'Could not save the cut rule set.'),
  })

  const breadcrumb = [{ label: 'Cut rules', to: '/cut-rule-sets' }, { label: isNew ? 'New rule set' : 'Cut rule set' }]

  const loaded = useMemo(() => (ruleSet ? setToDraft(ruleSet) : initial), [ruleSet, initial])
  const current = draft ?? loaded
  const dirty = !isNew && draft !== null && !sameDraft(current, loaded)
  const config = useMemo(() => draftToConfig(current), [current])

  if (!isNew && error instanceof ApiError && error.status === 404) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Cut rule set not found" breadcrumb={breadcrumb} />
        <p className="text-sm text-muted-foreground">This rule set does not exist (it may have been deleted).</p>
      </div>
    )
  }
  if (!isNew && (isLoading || !ruleSet)) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Cut rule set" breadcrumb={breadcrumb} />
        {error ? (
          <p className="text-sm text-destructive">Could not load the cut rule set.</p>
        ) : (
          <p className="text-sm text-muted-foreground">Loading…</p>
        )}
      </div>
    )
  }

  function patch(next: Partial<RuleSetDraft>) {
    setDraft({ ...current, ...next })
    setFormError(null)
  }
  function patchRule(index: number, next: Partial<RuleDraft>) {
    patch({ rules: current.rules.map((r, i) => (i === index ? { ...r, ...next } : r)) })
  }
  function patchGuards(next: Partial<GuardsDraft>) {
    patch({ guards: { ...current.guards, ...next } })
  }

  function handleSave() {
    const problem = validateDraft(current)
    if (problem) {
      setFormError(problem)
      return
    }
    save.mutate(draftToInput(current))
  }

  const scopeOptions =
    current.scope_type === 'channel'
      ? (channels ?? []).map((c) => ({ value: c.id, label: `${c.number} — ${c.name}` }))
      : (blocks ?? []).map((b) => ({ value: b.id, label: b.name }))

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={isNew ? 'New cut rule set' : 'Cut rule set'} breadcrumb={breadcrumb} />
      <p className="max-w-2xl text-sm text-muted-foreground">
        Rules decide where inside a movie or episode a break goes. The first rule that matches an item decides its
        cuts. Rule sets are saved and validated now; the guide does not apply them yet.
      </p>

      <div className="grid gap-8 lg:grid-cols-[minmax(0,36rem)_minmax(0,1fr)]">
        <div className="flex flex-col gap-6">
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="cr-scope-type">Scope</FieldLabel>
              <Select
                value={current.scope_type}
                onValueChange={(v) => patch({ scope_type: v as FillerScope, scope_id: '' })}
              >
                <SelectTrigger id="cr-scope-type" className="w-full">
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
              <FieldLabel htmlFor="cr-scope-target">{SCOPE_LABELS[current.scope_type]}</FieldLabel>
              <Combobox
                id="cr-scope-target"
                options={scopeOptions}
                value={current.scope_id || null}
                onValueChange={(v) => patch({ scope_id: v })}
                placeholder={current.scope_type === 'channel' ? 'Select a channel' : 'Select a block'}
                searchPlaceholder="Search…"
              />
              <FieldDescription>A block's own rule set wins over its channel's.</FieldDescription>
            </Field>

            <Field>
              <FieldLabel htmlFor="cr-name">Name</FieldLabel>
              <Input
                id="cr-name"
                value={current.name}
                maxLength={MAX_CUT_NAME}
                onChange={(e) => patch({ name: e.target.value })}
                placeholder="Optional"
              />
            </Field>

            <Field>
              <FieldLabel htmlFor="cr-position">Position</FieldLabel>
              <Input
                id="cr-position"
                type="number"
                step={1}
                value={Number.isNaN(current.position) ? '' : current.position}
                onChange={(e) => patch({ position: e.target.value === '' ? NaN : Number(e.target.value) })}
              />
              <FieldDescription>Order among rule sets of the same scope; the lowest wins.</FieldDescription>
            </Field>
          </FieldGroup>

          <section className="flex flex-col gap-3" data-testid="cr-rules">
            <h2 className="text-base font-medium">Rules</h2>
            {current.rules.map((rule, i) => (
              <div key={rule.key} className="flex flex-col gap-3 rounded-lg border p-3" data-testid={`cr-rule-${i}`}>
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">Rule {i + 1}</span>
                  <div className="flex gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Move rule ${i + 1} up`}
                      data-testid={`cr-rule-${i}-up`}
                      disabled={i === 0}
                      onClick={() => patch({ rules: moveItem(current.rules, i, -1) })}
                    >
                      <ArrowUp />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Move rule ${i + 1} down`}
                      data-testid={`cr-rule-${i}-down`}
                      disabled={i === current.rules.length - 1}
                      onClick={() => patch({ rules: moveItem(current.rules, i, 1) })}
                    >
                      <ArrowDown />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Remove rule ${i + 1}`}
                      data-testid={`cr-rule-${i}-remove`}
                      onClick={() => patch({ rules: current.rules.filter((_, j) => j !== i) })}
                    >
                      <Trash2 />
                    </Button>
                  </div>
                </div>

                <Field>
                  <FieldLabel htmlFor={`cr-rule-${i}-name`}>Name</FieldLabel>
                  <Input
                    id={`cr-rule-${i}-name`}
                    value={rule.name}
                    placeholder="Optional"
                    onChange={(e) => patchRule(i, { name: e.target.value })}
                  />
                </Field>

                <fieldset className="flex flex-col gap-2">
                  <legend className="mb-1 text-sm font-medium">Applies to</legend>
                  <div className="flex flex-wrap gap-4">
                    {(Object.keys(CUT_KIND_LABELS) as CutItemKind[]).map((k) => (
                      <label key={k} className="flex items-center gap-2 text-sm">
                        <Checkbox
                          data-testid={`cr-rule-${i}-kind-${k}`}
                          checked={rule.kinds.includes(k)}
                          onCheckedChange={(on) =>
                            patchRule(i, { kinds: on === true ? [...rule.kinds, k] : rule.kinds.filter((x) => x !== k) })
                          }
                        />
                        {CUT_KIND_LABELS[k]}
                      </label>
                    ))}
                  </div>
                  <FieldDescription>Leave all unchecked to match any kind.</FieldDescription>
                  <div className="flex flex-wrap gap-3">
                    <Field className="w-40">
                      <FieldLabel htmlFor={`cr-rule-${i}-min`}>Longer than (min)</FieldLabel>
                      <Input
                        id={`cr-rule-${i}-min`}
                        data-testid={`cr-rule-${i}-min`}
                        inputMode="decimal"
                        value={rule.min_minutes}
                        placeholder="any"
                        onChange={(e) => patchRule(i, { min_minutes: e.target.value })}
                      />
                    </Field>
                    <Field className="w-40">
                      <FieldLabel htmlFor={`cr-rule-${i}-max`}>Shorter than (min)</FieldLabel>
                      <Input
                        id={`cr-rule-${i}-max`}
                        data-testid={`cr-rule-${i}-max`}
                        inputMode="decimal"
                        value={rule.max_minutes}
                        placeholder="any"
                        onChange={(e) => patchRule(i, { max_minutes: e.target.value })}
                      />
                    </Field>
                    <Field className="w-44">
                      <FieldLabel htmlFor={`cr-rule-${i}-chapters`}>Chapters</FieldLabel>
                      <Select
                        value={rule.has_chapters}
                        onValueChange={(v) => patchRule(i, { has_chapters: v as HasChaptersChoice })}
                      >
                        <SelectTrigger id={`cr-rule-${i}-chapters`} data-testid={`cr-rule-${i}-chapters`} className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="any">Either</SelectItem>
                          <SelectItem value="yes">Has chapters</SelectItem>
                          <SelectItem value="no">No chapters</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                </fieldset>

                <CutStrategyFields
                  id={`cr-rule-${i}`}
                  value={rule.strategy}
                  onChange={(strategy) => patchRule(i, { strategy })}
                />

                <Field>
                  <FieldLabel htmlFor={`cr-rule-${i}-snap`}>Snap to chapters within (min)</FieldLabel>
                  <Input
                    id={`cr-rule-${i}-snap`}
                    data-testid={`cr-rule-${i}-snap`}
                    inputMode="decimal"
                    className="w-40"
                    value={rule.snap_minutes}
                    placeholder="off"
                    onChange={(e) => patchRule(i, { snap_minutes: e.target.value })}
                  />
                  <FieldDescription>
                    When the item has chapters, a cut within this distance of a chapter boundary moves onto it.
                  </FieldDescription>
                </Field>
              </div>
            ))}
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                data-testid="cr-add-rule"
                disabled={current.rules.length >= MAX_CUT_RULES}
                onClick={() => patch({ rules: [...current.rules, emptyRule()] })}
              >
                <Plus /> Add rule
              </Button>
              {current.rules.length >= MAX_CUT_RULES && (
                <span className="ml-2 text-sm text-muted-foreground">At most {MAX_CUT_RULES} rules.</span>
              )}
            </div>
          </section>

          <section className="flex flex-col gap-3" data-testid="cr-default">
            <h2 className="text-base font-medium">Default</h2>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                data-testid="cr-default-enabled"
                checked={current.default_enabled}
                onCheckedChange={(on) => patch({ default_enabled: on === true })}
              />
              Cut the items no rule matches
            </label>
            {current.default_enabled ? (
              <div className="rounded-lg border p-3">
                <CutStrategyFields id="cr-default" value={current.default} onChange={(d) => patch({ default: d })} />
              </div>
            ) : (
              <FieldDescription>Without a default, items no rule matches are not cut.</FieldDescription>
            )}
          </section>

          <section className="flex flex-col gap-3" data-testid="cr-guards">
            <h2 className="text-base font-medium">Guard rails</h2>
            <p className="text-sm text-muted-foreground">
              Applied to every rule. Leave a field empty for its default (3 minutes, and at most 10 breaks per item).
            </p>
            <div className="grid grid-cols-2 gap-3">
              <Field>
                <FieldLabel htmlFor="cr-guard-segment">Shortest segment (min)</FieldLabel>
                <Input
                  id="cr-guard-segment"
                  data-testid="cr-guard-segment"
                  inputMode="decimal"
                  value={current.guards.min_segment_minutes}
                  placeholder="3"
                  onChange={(e) => patchGuards({ min_segment_minutes: e.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="cr-guard-max">Most breaks per item</FieldLabel>
                <Input
                  id="cr-guard-max"
                  data-testid="cr-guard-max"
                  inputMode="numeric"
                  value={current.guards.max_breaks}
                  placeholder="10"
                  onChange={(e) => patchGuards({ max_breaks: e.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="cr-guard-start">Away from the start (min)</FieldLabel>
                <Input
                  id="cr-guard-start"
                  data-testid="cr-guard-start"
                  inputMode="decimal"
                  value={current.guards.min_from_start_minutes}
                  placeholder="3"
                  onChange={(e) => patchGuards({ min_from_start_minutes: e.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="cr-guard-end">Away from the end (min)</FieldLabel>
                <Input
                  id="cr-guard-end"
                  data-testid="cr-guard-end"
                  inputMode="decimal"
                  value={current.guards.min_from_end_minutes}
                  placeholder="3"
                  onChange={(e) => patchGuards({ min_from_end_minutes: e.target.value })}
                />
              </Field>
            </div>
          </section>

          {formError && isNew && (
            <p className="text-sm text-destructive" data-testid="cr-form-error">
              {formError}
            </p>
          )}
          {isNew ? (
            <div>
              <Button onClick={handleSave} disabled={save.isPending} data-testid="cr-create">
                {save.isPending ? 'Creating…' : 'Create rule set'}
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
        </div>

        <div className="lg:sticky lg:top-4 lg:self-start">
          <CutPreviewPanel config={config.ok ? config.value : null} configError={config.ok ? null : config.error} />
        </div>
      </div>
    </div>
  )
}
