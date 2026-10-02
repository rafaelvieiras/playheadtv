import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { CriteriaPreviewPanel } from '@/components/CriteriaPreviewPanel'
import { DynamicCriteriaFields } from '@/components/DynamicCriteriaFields'
import { FillerPoolItems } from '@/components/FillerPoolItems'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Badge } from '@/components/ui/badge'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { parsePoolCriteria, validateCriteria } from '@/lib/filler'
import { api, ApiError } from '@/lib/http'
import type { DynamicCriteria, FillerPool, FillerPoolInput, FillerRule } from '@/lib/types'

interface Draft {
  name: string
  criteria: DynamicCriteria | null
}

function normalize(criteria: DynamicCriteria | null): string {
  if (!criteria) return ''
  return JSON.stringify([criteria.kind, criteria.genres, criteria.year_from, criteria.year_to])
}

export function FillerPoolEditorPage() {
  const { id = '' } = useParams()
  const queryClient = useQueryClient()
  const { data: pool, isLoading, error } = useQuery({
    queryKey: ['filler-pools', id],
    queryFn: () => api.get<FillerPool>(`/filler-pools/${id}`),
  })
  const { data: rules } = useQuery({
    queryKey: ['filler-rules'],
    queryFn: () => api.get<FillerRule[]>('/filler-rules'),
  })
  const [draft, setDraft] = useState<Draft | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (payload: FillerPoolInput) => api.put<FillerPool>(`/filler-pools/${id}`, payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['filler-pools'] })
      setDraft(null)
      setSaveError(null)
      toast.success('Filler pool saved.')
    },
    onError: (e) => setSaveError(e instanceof ApiError ? e.message : 'Could not save the filler pool.'),
  })

  const breadcrumb = [{ label: 'Filler pools', to: '/filler-pools' }, { label: pool?.name ?? 'Filler pool' }]

  if (error instanceof ApiError && error.status === 404) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Filler pool not found" breadcrumb={breadcrumb} />
        <p className="text-sm text-muted-foreground">This filler pool does not exist (it may have been deleted).</p>
      </div>
    )
  }
  if (isLoading || !pool) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Filler pool" breadcrumb={breadcrumb} />
        {error ? (
          <p className="text-sm text-destructive">Could not load the filler pool.</p>
        ) : (
          <p className="text-sm text-muted-foreground">Loading…</p>
        )}
      </div>
    )
  }

  const loaded: Draft = { name: pool.name, criteria: parsePoolCriteria(pool) }
  const current = draft ?? loaded
  const dynamic = current.criteria !== null
  const criteriaError = validateCriteria(current.criteria)
  const nameMissing = current.name.trim() === ''
  const dirty = current.name !== loaded.name || normalize(current.criteria) !== normalize(loaded.criteria)
  const usedBy = (rules ?? []).filter((r) => r.filler_pool_id === pool.id)

  function patch(next: Partial<Draft>) {
    setDraft({ ...current, ...next })
    setSaveError(null)
  }

  function handleSave() {
    if (nameMissing) {
      setSaveError('Name is required.')
      return
    }
    if (criteriaError) {
      setSaveError(criteriaError)
      return
    }
    save.mutate({ name: current.name.trim(), criteria: current.criteria })
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {pool.name}
            <Badge variant={loaded.criteria ? 'default' : 'secondary'}>{loaded.criteria ? 'Dynamic' : 'Manual'}</Badge>
          </span>
        }
        breadcrumb={breadcrumb}
      />

      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="pool-name">Name</FieldLabel>
          <Input
            id="pool-name"
            value={current.name}
            onChange={(e) => patch({ name: e.target.value })}
            className="max-w-md"
          />
        </Field>
        {usedBy.length > 0 && (
          <p className="text-sm text-muted-foreground" data-testid="pool-used-by">
            Used by {usedBy.length} filler {usedBy.length === 1 ? 'rule' : 'rules'}:{' '}
            {usedBy.map((r, i) => (
              <span key={r.id}>
                {i > 0 && ', '}
                <Link to={`/filler-rules/${r.id}`} className="underline">
                  {r.kind} ({r.scope_type})
                </Link>
              </span>
            ))}
          </p>
        )}
      </FieldGroup>

      <Tabs value={dynamic ? 'dynamic' : 'manual'} onValueChange={(v) => patch({ criteria: v === 'dynamic' ? emptyCriteria() : null })}>
        <TabsList>
          <TabsTrigger value="manual">Manual items</TabsTrigger>
          <TabsTrigger value="dynamic">Dynamic criteria</TabsTrigger>
        </TabsList>
        <TabsContent value="manual" className="pt-4">
          {loaded.criteria ? (
            <p className="text-sm text-muted-foreground" data-testid="pool-manual-pending">
              Save to turn this dynamic pool back into a manual one, then add items here.
            </p>
          ) : (
            <FillerPoolItems poolId={pool.id} editable />
          )}
        </TabsContent>
        <TabsContent value="dynamic" className="flex flex-col gap-4 pt-4">
          <p className="text-sm text-muted-foreground">
            A dynamic pool is filled automatically from the catalog; its items cannot be edited by hand.
          </p>
          <FieldGroup>
            <DynamicCriteriaFields value={current.criteria} onChange={(criteria) => patch({ criteria })} idPrefix="pool-criteria" />
          </FieldGroup>
          {criteriaError && (
            <p className="text-sm text-destructive" data-testid="criteria-error">
              {criteriaError}
            </p>
          )}
          <CriteriaPreviewPanel criteria={criteriaError ? null : current.criteria} />
        </TabsContent>
      </Tabs>

      <SaveBar
        dirty={dirty}
        saving={save.isPending}
        error={saveError}
        onSave={handleSave}
        onDiscard={() => {
          setDraft(null)
          setSaveError(null)
        }}
      />
    </div>
  )
}

function emptyCriteria(): DynamicCriteria {
  return { kind: 'movie', genres: [], year_from: null, year_to: null }
}
