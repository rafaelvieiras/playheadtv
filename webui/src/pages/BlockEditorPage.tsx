import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp } from 'lucide-react'
import { useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { CriteriaAmountControl, CriteriaOrderSelect } from '@/components/CriteriaItemControls'
import { CriteriaReviewNotice } from '@/components/CriteriaReviewNotice'
import { type BlockPlaybackDraft } from '@/components/BlockPlaybackFields'
import { BlockIncludeSpecialsField } from '@/components/BlockIncludeSpecialsField'
import { BlockListModeField } from '@/components/BlockListModeField'
import { ListModeReviewNotice } from '@/components/ListModeReviewNotice'
import { CollectionPicker } from '@/components/CollectionPicker'
import { DynamicCollectionFields, validateCollectionChoice, type CollectionChoice } from '@/components/DynamicCollectionFields'
import { InterleaveControls } from '@/components/InterleaveControls'
import { InlineWarning } from '@/components/InlineWarning'
import { Loading, QueryError } from '@/components/QueryError'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { WatermarkSection } from '@/components/WatermarkSection'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { authoringKeys, usePreviewCriteria, useUpdateBlockItem } from '@/lib/authoring'
import { formatCatalogTitle } from '@/lib/catalog'
import { formatDuration } from '@/lib/criteria'
import {
  blockPlaybackPayload,
  DEFAULT_INTERLEAVE,
  toDraft,
  validateInterleave,
  type InterleaveDraft,
} from '@/lib/interleave'
import { useCollections } from '@/lib/collections'
import { summarizeQuery, validateQuery, type Query } from '@/lib/query'
import { airsUntilSlotEnd } from '@/lib/guide'
import { ApiError, api } from '@/lib/http'
import { SeriesAmountControl } from '@/components/SeriesAmountControl'
import { SeriesSliceControl } from '@/components/SeriesSliceControl'
import { sliceKey, sliceSummary } from '@/lib/seriesSlice'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  PLAYBACK_ORDER_LABELS,
  type Block,
  type BlockInput,
  type BlockItem,
  type BlockItemInput,
  type CatalogItem,
  type CriteriaAmount,
  type CriteriaOrder,
  type Interleave,
  type ListMode,
  type PlaybackOrder,
  type SeriesAmount,
  type SeriesSlice,
  type SeriesSummary,
} from '@/lib/types'

// seriesKey identifies a GET /series row: media source plus external id or title.
function seriesKey(s: SeriesSummary): string {
  return `${s.media_source_id}\u0000${s.series_external_id ?? ''}\u0000${s.series_title}`
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? `${fallback} (${err.message})` : fallback
}

// Full-page editor for one block: details (name/description, and the collection
// for a dynamic block) are saved through the SaveBar; the item list of a
// manual block is edited live, each action being its own request.
export function BlockEditorPage() {
  const { id = '' } = useParams()
  const { data: block, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['blocks', id],
    queryFn: () => api.get<Block>(`/blocks/${id}`),
    enabled: !!id,
  })

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={block?.name ?? 'Block'}
        breadcrumb={[{ label: 'Blocks', to: '/blocks' }, { label: block?.name ?? 'Block' }]}
        actions={
          block && (
            <Badge variant={block.dynamic ? 'default' : 'secondary'} data-testid="block-kind">
              {block.dynamic ? 'Dynamic' : 'Manual'}
            </Badge>
          )
        }
      />
      {isLoading && <Loading />}
      {isError && <QueryError subject="the block" error={error} onRetry={() => void refetch()} />}
      {block && (
        <>
          {/* Remounted after a save (updated_at changes) so the draft resets. */}
          <BlockDetailsForm key={`${block.id}-${block.updated_at}-${block.collection_id}`} block={block} />
          {!block.dynamic && <BlockItemsSection block={block} />}
        </>
      )}
    </div>
  )
}

function BlockDetailsForm({ block }: { block: Block }) {
  const queryClient = useQueryClient()
  // A block held for review (a legacy row with no collection yet) is dynamic
  // but has no collection to edit until someone applies the proposal or picks
  // one (see CriteriaReviewNotice).
  const initial = useMemo<{ name: string; description: string | null; collection: CollectionChoice; keepLegacy: boolean }>(
    () => ({
      name: block.name,
      description: block.description,
      collection: block.collection_id,
      keepLegacy: block.dynamic && block.collection_id === null,
    }),
    [block]
  )
  const initialPlayback = useMemo<BlockPlaybackDraft>(
    () => ({ order: block.playback_order ?? 'title', interleave: toDraft(block.interleave) }),
    [block]
  )
  const [draft, setDraft] = useState(initial)
  const [playback, setPlayback] = useState(initialPlayback)
  const initialListMode: ListMode = block.list_mode ?? 'repeat'
  const [listMode, setListMode] = useState<ListMode>(initialListMode)
  const initialIncludeSpecials = block.include_specials ?? false
  const [includeSpecials, setIncludeSpecials] = useState(initialIncludeSpecials)
  const [error, setError] = useState<string | null>(null)
  const [nameError, setNameError] = useState<string | undefined>(undefined)
  const isDynamic = block.dynamic
  const unreviewed = draft.keepLegacy && draft.collection === null

  const dirty =
    draft.name !== initial.name ||
    draft.description !== initial.description ||
    draft.collection !== initial.collection ||
    draft.keepLegacy !== initial.keepLegacy ||
    (isDynamic && JSON.stringify(playback) !== JSON.stringify(initialPlayback)) ||
    (!isDynamic && listMode !== initialListMode) ||
    includeSpecials !== initialIncludeSpecials

  const saveMutation = useMutation({
    mutationFn: () =>
      api.put<Block>(`/blocks/${block.id}`, {
        name: draft.name,
        description: draft.description,
        collection_id: draft.collection,
        ...(draft.collection ? blockPlaybackPayload(playback.order, playback.interleave) : {}),
        // Only a changed mode is sent: writing it settles the migration review
        // of a block, which a mere rename must not do.
        ...(!isDynamic && listMode !== initialListMode ? { list_mode: listMode } : {}),
        ...(includeSpecials !== initialIncludeSpecials ? { include_specials: includeSpecials } : {}),
      } satisfies BlockInput),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('Block saved.')
    },
    onError: (err) => setError(errorMessage(err, 'Could not save the block.')),
  })

  function save() {
    // The name's problem belongs under the name, not in the floating bar at
    // the bottom of the page — where it also stayed after a valid name was
    // typed.
    const missingName = draft.name.trim() === '' ? 'Name is required.' : undefined
    setNameError(missingName)
    if (missingName) return
    if (unreviewed) {
      setError('Review the old query first: apply the proposal or choose a collection.')
      return
    }
    const criteriaError =
      validateCollectionChoice(draft.collection) ??
      (draft.collection && playback.order === 'interleaved' ? validateInterleave(playback.interleave) : undefined)
    if (criteriaError) {
      setError(criteriaError)
      return
    }
    setError(null)
    saveMutation.mutate()
  }

  function discard() {
    setDraft(initial)
    setPlayback(initialPlayback)
    setListMode(initialListMode)
    setIncludeSpecials(initialIncludeSpecials)
    setNameError(undefined)
    setError(null)
  }

  return (
    <section className="flex flex-col gap-4">
      <FieldGroup>
        <Field data-invalid={!!nameError}>
          <FieldLabel htmlFor="block-editor-name">
            Name <span className="text-destructive">*</span>
          </FieldLabel>
          <Input
            id="block-editor-name"
            aria-invalid={!!nameError}
            aria-describedby={nameError ? 'block-editor-name-error' : undefined}
            value={draft.name}
            onChange={(e) => {
              setDraft((d) => ({ ...d, name: e.target.value }))
              setNameError(undefined)
            }}
          />
          <FieldError id="block-editor-name-error">{nameError}</FieldError>
        </Field>
        <Field>
          <FieldLabel htmlFor="block-editor-description">Description (optional)</FieldLabel>
          <Textarea
            id="block-editor-description"
            value={draft.description ?? ''}
            onChange={(e) => setDraft((d) => ({ ...d, description: e.target.value || null }))}
          />
        </Field>
        {!isDynamic && <BlockListModeField value={listMode} onChange={setListMode} idPrefix="block-editor" />}
        <BlockIncludeSpecialsField value={includeSpecials} onChange={setIncludeSpecials} idPrefix="block-editor" />
        {isDynamic && !unreviewed && (
          <DynamicCollectionFields
            value={draft.collection}
            onChange={(collection) => setDraft((d) => ({ ...d, collection }))}
            idPrefix="block-editor-collection"
            playback={{ value: playback, onChange: setPlayback }}
            preview
          />
        )}
      </FieldGroup>

      {!isDynamic && <ListModeReviewNotice block={block} />}

      {isDynamic && unreviewed && (
        <CriteriaReviewNotice
          ownerType="block"
          ownerId={block.id}
          onChooseCollection={() => setDraft((d) => ({ ...d, collection: '', keepLegacy: false }))}
        />
      )}

      <SaveBar dirty={dirty} saving={saveMutation.isPending} onSave={save} onDiscard={discard} error={error} />

      <WatermarkSection ownerType="block" ownerId={block.id} />
    </section>
  )
}

// Live, debounced preview of what the draft criteria match right now.
function CriteriaPreviewPanel({ criteria, empty }: { criteria: Query; empty?: string }) {
  const preview = usePreviewCriteria()
  const { mutate } = preview
  const invalid = validateQuery(criteria)
  const key = JSON.stringify(criteria)

  useEffect(() => {
    if (invalid) return
    const handle = setTimeout(() => mutate(JSON.parse(key) as Query), 500)
    return () => clearTimeout(handle)
  }, [key, invalid, mutate])

  const data = invalid ? undefined : preview.data

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4" data-testid="criteria-preview">
      <h2 className="text-sm font-medium">Preview</h2>
      {invalid && <p className="text-sm text-muted-foreground">{invalid}</p>}
      {!invalid && preview.isError && (
        <QueryError
          subject="the preview"
          error={preview.error}
          onRetry={() => mutate(JSON.parse(key) as Query)}
          stale={data !== undefined}
          testId="criteria-preview-error"
        />
      )}
      {!invalid && !data && !preview.isError && <p className="text-sm text-muted-foreground">Loading…</p>}
      {data && (
        <>
          <p className="text-sm" data-testid="preview-summary">
            <span data-testid="preview-count">{data.match_count}</span> matching item
            {data.match_count === 1 ? '' : 's'}, total{' '}
            <span data-testid="preview-duration">{formatDuration(data.total_duration_ms)}</span>
          </p>
          {data.match_count === 0 && (
            <InlineWarning title="No matches">
              {empty ?? 'These criteria match nothing in the catalog right now, so the block would be empty.'}
            </InlineWarning>
          )}
          {data.sample.length > 0 && (
            <div className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">
                Sample (first {data.sample.length} of {data.match_count}, by title)
              </span>
              <ul className="flex flex-col divide-y divide-border rounded-md border border-border text-sm">
                {data.sample.map((item) => (
                  <li key={item.id} className="flex items-center justify-between gap-2 px-2 py-1">
                    <span className="truncate">{formatCatalogTitle(item)}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {formatDuration(item.duration_ms)}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function BlockItemsSection({ block }: { block: Block }) {
  const queryClient = useQueryClient()
  const blockId = block.id
  const itemsKey = authoringKeys.blockItems(blockId)

  const { data: items, isLoading } = useQuery({
    queryKey: itemsKey,
    queryFn: () => api.get<BlockItem[]>(`/blocks/${blockId}/items`),
  })
  const sorted = useMemo(() => [...(items ?? [])].sort((a, b) => a.position - b.position), [items])

  const catalogIds = useMemo(
    () => [...new Set(sorted.flatMap((i) => (i.item_type !== 'catalog' || !i.catalog_item_id ? [] : [i.catalog_item_id])))],
    [sorted]
  )
  const catalogQueries = useQueries({
    queries: catalogIds.map((cid) => ({
      queryKey: ['catalog-item', cid],
      queryFn: () => api.get<CatalogItem>(`/catalog-items/${cid}`),
      staleTime: Infinity,
    })),
  })
  const catalogById = useMemo(() => {
    const map = new Map<string, CatalogItem>()
    catalogIds.forEach((cid, i) => {
      const data = catalogQueries[i]?.data
      if (data) map.set(cid, data)
    })
    return map
  }, [catalogIds, catalogQueries])

  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedSearch(search.trim()), 300)
    return () => clearTimeout(handle)
  }, [search])

  const { data: results } = useQuery({
    queryKey: ['catalog-items-search', debouncedSearch],
    queryFn: () => api.get<CatalogItem[]>(`/catalog-items?q=${encodeURIComponent(debouncedSearch)}&limit=10`),
    enabled: debouncedSearch.length > 0,
  })
  const options = useMemo(
    () =>
      (results ?? []).map((r) => ({
        value: r.id,
        label: `${formatCatalogTitle(r)} (${formatDuration(r.duration_ms)})`,
      })),
    [results]
  )

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: itemsKey })
  }

  // Series picker: GET /series?q=, the chosen series is added with the order
  // and the amount per visit selected next to it.
  const [seriesSearch, setSeriesSearch] = useState('')
  const [debouncedSeriesSearch, setDebouncedSeriesSearch] = useState('')
  const [newSeriesOrder, setNewSeriesOrder] = useState<PlaybackOrder>('original')
  const [newSeriesAmount, setNewSeriesAmount] = useState<SeriesAmount>({ mode: 'count', count: 1 })
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedSeriesSearch(seriesSearch.trim()), 300)
    return () => clearTimeout(handle)
  }, [seriesSearch])
  const { data: seriesResults } = useQuery({
    queryKey: ['series-search', debouncedSeriesSearch],
    queryFn: () => api.get<SeriesSummary[]>(`/series?q=${encodeURIComponent(debouncedSeriesSearch)}&limit=10`),
  })
  const seriesByKey = useMemo(() => {
    const map = new Map<string, SeriesSummary>()
    for (const s of seriesResults ?? []) map.set(seriesKey(s), s)
    return map
  }, [seriesResults])
  const seriesOptions = useMemo(
    () =>
      (seriesResults ?? []).map((s) => ({
        value: seriesKey(s),
        label: `${s.series_title} (${s.episode_count} episode${s.episode_count === 1 ? '' : 's'}, ${formatDuration(s.total_duration_ms)})`,
      })),
    [seriesResults]
  )

  const addMutation = useMutation({
    mutationFn: (body: BlockItemInput) => api.post<BlockItem>(`/blocks/${blockId}/items`, body),
    onSuccess: () => {
      invalidate()
      toast.success('Item added.')
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not add the item.')),
  })

  function addSeries(key: string) {
    const s = seriesByKey.get(key)
    if (!s) return
    addMutation.mutate({
      series: {
        media_source_id: s.media_source_id,
        ...(s.series_external_id ? { series_external_id: s.series_external_id } : { series_title: s.series_title }),
        playback_order: newSeriesOrder,
        amount: newSeriesAmount,
      },
    })
  }

  const removeMutation = useMutation({
    mutationFn: (itemId: string) => api.delete(`/blocks/${blockId}/items/${itemId}`),
    onSuccess: () => {
      invalidate()
      toast.success('Item removed.')
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not remove the item.')),
  })

  const updateMutation = useUpdateBlockItem(blockId)
  const busy = addMutation.isPending || removeMutation.isPending || updateMutation.isPending

  function move(item: BlockItem, index: number, delta: -1 | 1) {
    updateMutation.mutate(
      { itemId: item.id, position: index + delta },
      { onError: (err) => toast.error(errorMessage(err, 'Could not reorder the item.')) }
    )
  }

  function setCriteriaOrder(item: BlockItem, order: CriteriaOrder) {
    updateMutation.mutate(
      { itemId: item.id, playback_order: order },
      {
        onSuccess: () => toast.success('Playback order changed, the item restarts.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not change the playback order.')),
      }
    )
  }

  function setCriteriaInterleave(item: BlockItem, interleave: InterleaveDraft) {
    if (interleave.per_round === null) return
    updateMutation.mutate(
      { itemId: item.id, interleave: { ...interleave, per_round: interleave.per_round } satisfies Interleave },
      {
        onSuccess: () => toast.success('Interleave settings saved.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not save the interleave settings.')),
      }
    )
  }

  function setCriteriaAmount(item: BlockItem, amount: CriteriaAmount | null) {
    if (!amount) return
    updateMutation.mutate(
      { itemId: item.id, amount },
      {
        onSuccess: () => toast.success('Amount saved.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not save the amount.')),
      }
    )
  }

  const { data: collections } = useCollections()
  // The criteria item whose collection is being changed, and the draft of it.
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editCollection, setEditCollection] = useState('')
  const editQuery = collections?.find((c) => c.id === editCollection)?.query ?? null

  function saveCollection(item: BlockItem) {
    if (!editCollection) {
      toast.error('Choose a collection.')
      return
    }
    updateMutation.mutate(
      { itemId: item.id, collection_id: editCollection },
      {
        onSuccess: () => {
          toast.success('Collection saved.')
          setEditingId(null)
          setEditCollection('')
        },
        onError: (err) => toast.error(errorMessage(err, 'Could not save the collection.')),
      }
    )
  }

  // New criteria item: a collection, an order and an amount, all chosen before it is added.
  const [newCollection, setNewCollection] = useState('')
  const [newCriteriaOrder, setNewCriteriaOrder] = useState<CriteriaOrder>('title')
  const [newCriteriaAmount, setNewCriteriaAmount] = useState<CriteriaAmount | null>(null)
  const [newInterleave, setNewInterleave] = useState<InterleaveDraft>(DEFAULT_INTERLEAVE)
  const [newCriteriaKey, setNewCriteriaKey] = useState(0)
  const newQuery = collections?.find((c) => c.id === newCollection)?.query ?? null
  const newQueryError = newCollection ? undefined : 'Choose a collection.'
  const newInterleaveError = newCriteriaOrder === 'interleaved' ? validateInterleave(newInterleave) : undefined

  function addCriteria() {
    if (newQueryError || newInterleaveError || !newCriteriaAmount) return
    const interleave = blockPlaybackPayload(newCriteriaOrder === 'interleaved' ? 'interleaved' : 'title', newInterleave).interleave
    addMutation.mutate(
      { criteria: { collection_id: newCollection, playback_order: newCriteriaOrder, amount: newCriteriaAmount, ...(interleave ? { interleave } : {}) } },
      {
        onSuccess: () => {
          setNewCollection('')
          setNewCriteriaOrder('title')
          setNewInterleave(DEFAULT_INTERLEAVE)
          setNewCriteriaAmount(null)
          setNewCriteriaKey((k) => k + 1)
        },
      }
    )
  }

  function setOrder(item: BlockItem, order: PlaybackOrder) {
    updateMutation.mutate(
      { itemId: item.id, playback_order: order },
      {
        onSuccess: () => toast.success('Playback order changed, the series restarts.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not change the playback order.')),
      }
    )
  }

  function setAmount(item: BlockItem, amount: SeriesAmount) {
    updateMutation.mutate(
      { itemId: item.id, amount },
      {
        onSuccess: () => toast.success('Amount saved.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not save the amount.')),
      }
    )
  }

  const [sliceOpenId, setSliceOpenId] = useState<string | null>(null)
  function setSlice(item: BlockItem, slice: SeriesSlice) {
    updateMutation.mutate(
      { itemId: item.id, slice },
      {
        onSuccess: () => toast.success(Object.keys(slice).length ? 'Slice saved, the series restarts.' : 'Slice cleared, the series restarts.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not save the slice.')),
      }
    )
  }

  function setTrim(item: BlockItem, ms: number) {
    updateMutation.mutate(
      { itemId: item.id, trim_duration_ms: ms },
      {
        onSuccess: () => toast.success('Trim saved.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not save the trim.')),
      }
    )
  }

  const catalogItems = sorted.filter((i) => i.item_type === 'catalog')
  const seriesCount = sorted.length - catalogItems.length
  const totalMs = catalogItems.reduce((sum, item) => {
    const full = catalogById.get(item.catalog_item_id ?? '')?.duration_ms ?? 0
    return sum + (item.trim_duration_ms ?? full)
  }, 0)
  const allLoaded = catalogItems.every((i) => catalogById.has(i.catalog_item_id ?? ''))
  // A series that airs until the end of the slot never hands over to the next
  // item, so anything after it in the block never gets on the air.
  const lastOpenEndedIndex = sorted.map((i) => airsUntilSlotEnd(i)).lastIndexOf(true)
  const shadowed = lastOpenEndedIndex >= 0 && lastOpenEndedIndex < sorted.length - 1

  return (
    <section className="flex flex-col gap-4" data-testid="block-items">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-medium">Items</h2>
        <span className="text-sm text-muted-foreground">
          Total duration{seriesCount > 0 ? ' (single items)' : ''}:{' '}
          <span data-testid="items-total">{allLoaded ? formatDuration(totalMs) : '…'}</span>
        </span>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {items && sorted.length === 0 && (
        <InlineWarning title="This block is empty">Add at least one item below so it has something to play.</InlineWarning>
      )}

      {shadowed && (
        <InlineWarning title="Items after a series never play">
          A series set to "Until the end of the slot" airs episodes until the next scheduled item or the end of the
          guide, so the items below it in this block are never reached. Move the series to the end, or change its amount
          to a number of episodes or "Until the series ends".
        </InlineWarning>
      )}

      {sorted.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-12">#</TableHead>
              <TableHead>Title</TableHead>
              <TableHead>Full length</TableHead>
              <TableHead>Trim (seconds) / order and amount</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((item, index) => {
              const isSeries = item.item_type === 'series'
              const isCriteria = item.item_type === 'criteria'
              const catalog = catalogById.get(item.catalog_item_id ?? '')
              return (
                <TableRow key={item.id} data-testid="block-item-row">
                  <TableCell>{index + 1}</TableCell>
                  <TableCell className="max-w-64 truncate" data-testid="item-title">
                    {isSeries ? (
                      <span className="flex items-center gap-2">
                        <Badge variant="secondary">Series</Badge>
                        <span className="truncate">{item.series?.series_title || 'Series'}</span>
                      </span>
                    ) : isCriteria ? (
                      <span className="flex items-center gap-2" title={item.criteria?.query ? summarizeQuery(item.criteria.query) : undefined}>
                        <Badge variant="secondary">Collection</Badge>
                        <span className="truncate" data-testid="criteria-item-summary">
                          {item.criteria?.collection_name ?? (item.criteria?.query ? summarizeQuery(item.criteria.query) : 'Collection')}
                        </span>
                      </span>
                    ) : catalog ? (
                      formatCatalogTitle(catalog)
                    ) : (
                      '…'
                    )}
                  </TableCell>
                  <TableCell data-testid="item-length">
                    {isCriteria
                      ? item.criteria
                        ? `${item.criteria.match_count} match${item.criteria.match_count === 1 ? '' : 'es'}, ${formatDuration(item.criteria.total_duration_ms)}`
                        : '—'
                      : isSeries
                      ? item.series
                        ? `${item.series.episode_count} episode${item.series.episode_count === 1 ? '' : 's'}, ${formatDuration(item.series.total_duration_ms)}`
                        : '—'
                      : catalog
                        ? formatDuration(catalog.duration_ms)
                        : '—'}
                  </TableCell>
                  <TableCell>
                    {isCriteria ? (
                      <div className="flex flex-col gap-2">
                        <CriteriaOrderSelect
                          value={item.criteria?.playback_order ?? 'title'}
                          onChange={(o) => setCriteriaOrder(item, o)}
                          disabled={busy}
                          testId="criteria-order"
                        />
                        {item.criteria?.playback_order === 'interleaved' && (
                          <InterleaveControls
                            key={`${item.id}-${JSON.stringify(item.criteria.interleave)}`}
                            value={toDraft(item.criteria.interleave)}
                            onChange={(v) => setCriteriaInterleave(item, v)}
                            disabled={busy}
                            testIdPrefix="criteria"
                          />
                        )}
                        <CriteriaAmountControl
                          key={`${item.id}-${item.criteria?.amount?.mode}-${item.criteria?.amount?.count}`}
                          value={item.criteria?.amount ?? null}
                          onChange={(a) => setCriteriaAmount(item, a)}
                          disabled={busy}
                          testIdPrefix="criteria"
                        />
                      </div>
                    ) : isSeries ? (
                      <div className="flex flex-col gap-2">
                        <Select
                          value={item.series?.playback_order ?? 'original'}
                          onValueChange={(v) => setOrder(item, v as PlaybackOrder)}
                          disabled={busy}
                        >
                          <SelectTrigger className="w-48" aria-label="Playback order" data-testid="series-order">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {(Object.keys(PLAYBACK_ORDER_LABELS) as PlaybackOrder[]).map((o) => (
                              <SelectItem key={o} value={o}>
                                {PLAYBACK_ORDER_LABELS[o]}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <SeriesAmountControl
                          key={`${item.id}-${item.series?.amount?.mode}-${item.series?.amount?.count}`}
                          value={item.series?.amount ?? { mode: 'slot_end' }}
                          onChange={(a) => setAmount(item, a)}
                          disabled={busy}
                          testIdPrefix="series"
                        />
                        <div className="flex flex-wrap items-center gap-2">
                          <Button
                            variant="outline"
                            size="sm"
                            data-testid="series-slice-toggle"
                            aria-expanded={sliceOpenId === item.id}
                            onClick={() => setSliceOpenId(sliceOpenId === item.id ? null : item.id)}
                          >
                            {item.series?.slice ? 'Edit slice' : 'Slice…'}
                          </Button>
                          {item.series?.slice && (
                            <span className="text-xs text-muted-foreground" data-testid="series-slice-summary">
                              {sliceSummary(item.series.slice)}
                            </span>
                          )}
                        </div>
                        {item.series && sliceOpenId === item.id && (
                          <SeriesSliceControl
                            key={`${item.id}-${sliceKey(item.series.slice)}`}
                            series={item.series}
                            includeSpecials={block.include_specials ?? false}
                            disabled={busy}
                            onSave={(slice) => setSlice(item, slice)}
                          />
                        )}
                      </div>
                    ) : (
                      <TrimInput
                        key={`${item.id}-${item.trim_duration_ms}`}
                        item={item}
                        fullMs={catalog?.duration_ms}
                        disabled={busy}
                        onCommit={(ms) => setTrim(item, ms)}
                      />
                    )}
                  </TableCell>
                  <TableCell className="flex justify-end gap-1 text-right">
                    {isCriteria && item.criteria && (
                      <Button
                        variant="outline"
                        size="sm"
                        data-testid="criteria-edit-query"
                        disabled={busy}
                        onClick={() => {
                          setEditingId(item.id)
                          setEditCollection(item.criteria?.collection_id ?? '')
                        }}
                      >
                        Change collection
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="icon-sm"
                      aria-label="Move up"
                      disabled={busy || index === 0}
                      onClick={() => move(item, index, -1)}
                    >
                      <ArrowUp />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon-sm"
                      aria-label="Move down"
                      disabled={busy || index === sorted.length - 1}
                      onClick={() => move(item, index, 1)}
                    >
                      <ArrowDown />
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={busy}
                      onClick={() => removeMutation.mutate(item.id)}
                    >
                      Remove
                    </Button>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      {editingId && (
        <div className="flex flex-col gap-3 rounded-lg border border-border p-4" data-testid="criteria-edit-panel">
          <h3 className="text-sm font-medium">Change the collection of item {sorted.findIndex((i) => i.id === editingId) + 1}</h3>
          <CollectionPicker value={editCollection} onChange={setEditCollection} idPrefix="edit-criteria" />
          {editQuery && <CriteriaPreviewPanel criteria={editQuery} empty="This query matches nothing right now, so the item would be skipped." />}
          <div className="flex gap-2">
            <Button
              size="sm"
              data-testid="criteria-edit-save"
              disabled={busy || !editCollection}
              onClick={() => {
                const item = sorted.find((i) => i.id === editingId)
                if (item) saveCollection(item)
              }}
            >
              Save collection
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setEditingId(null)
                setEditCollection('')
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      <Field>
        <FieldLabel htmlFor="block-add-item">Add an item</FieldLabel>
        <Combobox
          id="block-add-item"
          options={options}
          value={null}
          onValueChange={(v) => addMutation.mutate({ catalog_item_id: v, trim_duration_ms: null })}
          placeholder="Search the catalog by title…"
          searchPlaceholder="Type a title…"
          emptyMessage={debouncedSearch ? 'No items found.' : 'Type to search the catalog.'}
          onSearchChange={setSearch}
          disabled={addMutation.isPending}
        />
      </Field>

      <Field>
        <FieldLabel htmlFor="block-add-series">Add a series</FieldLabel>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Select value={newSeriesOrder} onValueChange={(v) => setNewSeriesOrder(v as PlaybackOrder)}>
            <SelectTrigger className="w-full sm:w-52" aria-label="Order for the new series" data-testid="new-series-order">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(PLAYBACK_ORDER_LABELS) as PlaybackOrder[]).map((o) => (
                <SelectItem key={o} value={o}>
                  {PLAYBACK_ORDER_LABELS[o]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <SeriesAmountControl value={newSeriesAmount} onChange={setNewSeriesAmount} testIdPrefix="new-series" />
          <Combobox
            id="block-add-series"
            options={seriesOptions}
            value={null}
            onValueChange={addSeries}
            placeholder="Search series by title…"
            searchPlaceholder="Type a series title…"
            emptyMessage={debouncedSeriesSearch ? 'No series found.' : 'No series in the catalog.'}
            onSearchChange={setSeriesSearch}
            disabled={addMutation.isPending}
          />
        </div>
      </Field>

      <div className="flex flex-col gap-3 rounded-lg border border-border p-4" data-testid="add-criteria">
        <div>
          <h3 className="text-sm font-medium">Add a collection item</h3>
          <p className="text-sm text-muted-foreground">
            Plays the number of matches you choose from a collection (a live query of the catalog), then moves on to the next item.
          </p>
        </div>
        <CollectionPicker value={newCollection} onChange={setNewCollection} idPrefix="new-criteria" />
        {newQuery && <CriteriaPreviewPanel criteria={newQuery} empty="This query matches nothing right now, so the item would be skipped." />}
        <div className="flex flex-col gap-2 sm:flex-row">
          <CriteriaOrderSelect value={newCriteriaOrder} onChange={setNewCriteriaOrder} testId="new-criteria-order" />
          <CriteriaAmountControl
            key={newCriteriaKey}
            value={newCriteriaAmount}
            onChange={setNewCriteriaAmount}
            eager
            testIdPrefix="new-criteria"
          />
        </div>
        {newCriteriaOrder === 'interleaved' && (
          <InterleaveControls value={newInterleave} onChange={setNewInterleave} eager testIdPrefix="new-criteria" />
        )}
        {newInterleaveError && (
          <p className="text-sm text-destructive" data-testid="new-criteria-error">{newInterleaveError}</p>
        )}
        {!newCriteriaAmount && (
          <p className="text-xs text-muted-foreground">
            Enter how many matches to play per visit, or choose all of them.
          </p>
        )}
        <div>
          <Button
            size="sm"
            data-testid="add-criteria-button"
            disabled={addMutation.isPending || !!newQueryError || !!newInterleaveError || !newCriteriaAmount}
            onClick={addCriteria}
          >
            Add collection item
          </Button>
        </div>
      </div>
    </section>
  )
}

// Seconds input committed on blur/Enter. The backend can only set a positive
// trim (an absent value means "unchanged"), so there is no way to clear it;
// typing the full length is the way back to untrimmed.
function TrimInput({
  item,
  fullMs,
  disabled,
  onCommit,
}: {
  item: BlockItem
  fullMs: number | undefined
  disabled: boolean
  onCommit: (ms: number) => void
}) {
  const current = item.trim_duration_ms !== null ? String(item.trim_duration_ms / 1000) : ''
  const [value, setValue] = useState(current)
  const [error, setError] = useState<string | null>(null)

  function commit() {
    if (value === current) return
    const seconds = Number(value)
    const ms = Math.round(seconds * 1000)
    if (value.trim() === '' || !Number.isFinite(seconds) || ms <= 0) {
      setError('Enter a positive number of seconds.')
      return
    }
    if (fullMs !== undefined && ms > fullMs) {
      setError(`At most ${fullMs / 1000}s (the full length).`)
      return
    }
    setError(null)
    onCommit(ms)
  }

  return (
    <div className="flex flex-col gap-1">
      <Input
        type="number"
        min={0}
        step="any"
        className="w-28"
        aria-label="Trim in seconds"
        placeholder="full length"
        value={value}
        disabled={disabled}
        onChange={(e) => setValue(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit()
        }}
      />
      {error && <span className="text-xs text-destructive">{error}</span>}
    </div>
  )
}
