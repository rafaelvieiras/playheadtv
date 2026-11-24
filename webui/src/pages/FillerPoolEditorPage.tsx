import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { CriteriaReviewNotice } from '@/components/CriteriaReviewNotice'
import { DynamicCollectionFields, validateCollectionChoice, type CollectionChoice } from '@/components/DynamicCollectionFields'
import { FillerPoolItems } from '@/components/FillerPoolItems'
import { Loading, QueryError } from '@/components/QueryError'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Badge } from '@/components/ui/badge'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api, ApiError } from '@/lib/http'
import type { FillerPool, FillerPoolInput, FillerRule } from '@/lib/types'

interface Draft {
  name: string
  collection: CollectionChoice
  // The pool is held for review (a legacy row with no collection yet): dynamic,
  // but with no collection to edit. Saving needs a collection first.
  keepLegacy: boolean
}

export function FillerPoolEditorPage() {
  const { id = '' } = useParams()
  const queryClient = useQueryClient()
  const { data: pool, isLoading, error, refetch } = useQuery({
    queryKey: ['filler-pools', id],
    queryFn: () => api.get<FillerPool>(`/filler-pools/${id}`),
  })
  const { data: rules } = useQuery({
    queryKey: ['filler-rules'],
    queryFn: () => api.get<FillerRule[]>('/filler-rules'),
  })
  const [draft, setDraft] = useState<Draft | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [nameError, setNameError] = useState<string | undefined>(undefined)

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
          <QueryError subject="the filler pool" error={error} onRetry={() => void refetch()} />
        ) : (
          <Loading />
        )}
      </div>
    )
  }

  const loaded: Draft = {
    name: pool.name,
    collection: pool.collection_id,
    keepLegacy: pool.dynamic && pool.collection_id === null,
  }
  const current = draft ?? loaded
  const dynamic = current.collection !== null || current.keepLegacy
  const unreviewed = current.keepLegacy && current.collection === null
  const collectionError = validateCollectionChoice(current.collection)
  const nameMissing = current.name.trim() === ''
  const dirty =
    current.name !== loaded.name ||
    current.collection !== loaded.collection ||
    current.keepLegacy !== loaded.keepLegacy
  const usedBy = (rules ?? []).filter((r) => r.filler_pool_id === pool.id)

  function selectTab(toDynamic: boolean) {
    if (!toDynamic) patch({ collection: null, keepLegacy: false })
    else if (loaded.keepLegacy) patch({ collection: null, keepLegacy: true })
    else patch({ collection: '', keepLegacy: false })
  }

  function patch(next: Partial<Draft>) {
    setDraft({ ...current, ...next })
    setNameError(undefined)
    setSaveError(null)
  }

  function handleSave() {
    if (nameMissing) {
      // Under the name field, so it disappears as soon as the name is typed.
      setNameError('Name is required.')
      return
    }
    setNameError(undefined)
    if (unreviewed) {
      setSaveError('Review the old query first: apply the proposal or choose a collection.')
      return
    }
    if (collectionError) {
      setSaveError(collectionError)
      return
    }
    save.mutate({ name: current.name.trim(), collection_id: current.collection })
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {pool.name}
            <Badge variant={pool.dynamic ? 'default' : 'secondary'}>{pool.dynamic ? 'Dynamic' : 'Manual'}</Badge>
          </span>
        }
        breadcrumb={breadcrumb}
      />

      <FieldGroup>
        <Field data-invalid={!!nameError}>
          <FieldLabel htmlFor="pool-name">
            Name <span className="text-destructive">*</span>
          </FieldLabel>
          <Input
            id="pool-name"
            aria-invalid={!!nameError}
            aria-describedby={nameError ? 'pool-name-error' : undefined}
            value={current.name}
            onChange={(e) => patch({ name: e.target.value })}
            className="max-w-md"
          />
          <FieldError id="pool-name-error">{nameError}</FieldError>
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

      <Tabs value={dynamic ? 'dynamic' : 'manual'} onValueChange={(v) => selectTab(v === 'dynamic')}>
        <TabsList>
          <TabsTrigger value="manual">Manual items</TabsTrigger>
          <TabsTrigger value="dynamic">Dynamic (collection)</TabsTrigger>
        </TabsList>
        <TabsContent value="manual" className="pt-4">
          {pool.dynamic ? (
            <p className="text-sm text-muted-foreground" data-testid="pool-manual-pending">
              Save to turn this dynamic pool back into a manual one, then add items here.
            </p>
          ) : (
            <FillerPoolItems poolId={pool.id} editable />
          )}
        </TabsContent>
        <TabsContent value="dynamic" className="flex flex-col gap-4 pt-4">
          <p className="text-sm text-muted-foreground">
            A dynamic pool is filled automatically from the catalog by a collection; its items cannot be edited by hand.
          </p>
          {unreviewed ? (
            <CriteriaReviewNotice
              ownerType="filler_pool"
              ownerId={pool.id}
              onChooseCollection={() => patch({ collection: '', keepLegacy: false })}
            />
          ) : (
            <FieldGroup>
              <DynamicCollectionFields value={current.collection} onChange={(collection) => patch({ collection })} idPrefix="pool-collection" preview />
            </FieldGroup>
          )}
          {collectionError && current.collection !== null && (
            <p className="text-sm text-destructive" data-testid="criteria-error">
              {collectionError}
            </p>
          )}
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
