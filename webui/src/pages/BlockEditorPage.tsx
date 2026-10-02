import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp } from 'lucide-react'
import { useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { DynamicCriteriaFields } from '@/components/DynamicCriteriaFields'
import { InlineWarning } from '@/components/InlineWarning'
import { PageHeader } from '@/components/PageHeader'
import { SaveBar } from '@/components/SaveBar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { authoringKeys, usePreviewCriteria, useUpdateBlockItem } from '@/lib/authoring'
import { formatCatalogTitle } from '@/lib/catalog'
import { formatDuration, parseCriteriaJson, validateCriteria } from '@/lib/criteria'
import { ApiError, api } from '@/lib/http'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  PLAYBACK_ORDER_LABELS,
  type Block,
  type BlockInput,
  type BlockItem,
  type BlockItemInput,
  type CatalogItem,
  type DynamicCriteria,
  type PlaybackOrder,
  type SeriesSummary,
} from '@/lib/types'

function parseCriteria(raw: string | null): DynamicCriteria | null {
  return parseCriteriaJson(raw)
}

// seriesKey identifies a GET /series row: media source plus external id or title.
function seriesKey(s: SeriesSummary): string {
  return `${s.media_source_id}\u0000${s.series_external_id ?? ''}\u0000${s.series_title}`
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? `${fallback} (${err.message})` : fallback
}

// Full-page editor for one block: details (name/description, and the criteria
// for a dynamic block) are saved through the SaveBar; the item list of a
// manual block is edited live, each action being its own request.
export function BlockEditorPage() {
  const { id = '' } = useParams()
  const { data: block, isLoading, isError } = useQuery({
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
            <Badge variant={block.criteria ? 'default' : 'secondary'} data-testid="block-kind">
              {block.criteria ? 'Dynamic' : 'Manual'}
            </Badge>
          )
        }
      />
      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load the block.</p>}
      {block && (
        <>
          {/* Remounted after a save (updated_at changes) so the draft resets. */}
          <BlockDetailsForm key={`${block.id}-${block.updated_at}`} block={block} />
          {block.criteria === null && <BlockItemsSection block={block} />}
        </>
      )}
    </div>
  )
}

function BlockDetailsForm({ block }: { block: Block }) {
  const queryClient = useQueryClient()
  const initial = useMemo<BlockInput>(
    () => ({
      name: block.name,
      description: block.description,
      criteria: parseCriteria(block.criteria),
    }),
    [block]
  )
  const [draft, setDraft] = useState<BlockInput>(initial)
  const [error, setError] = useState<string | null>(null)
  const isDynamic = block.criteria !== null

  const dirty = JSON.stringify(draft) !== JSON.stringify(initial)

  const saveMutation = useMutation({
    mutationFn: () => api.put<Block>(`/blocks/${block.id}`, draft),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
      toast.success('Block saved.')
    },
    onError: (err) => setError(errorMessage(err, 'Could not save the block.')),
  })

  function save() {
    if (draft.name.trim() === '') {
      setError('Name is required.')
      return
    }
    const criteriaError = validateCriteria(draft.criteria)
    if (criteriaError) {
      setError(criteriaError)
      return
    }
    setError(null)
    saveMutation.mutate()
  }

  function discard() {
    setDraft(initial)
    setError(null)
  }

  return (
    <section className="flex flex-col gap-4">
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="block-editor-name">Name</FieldLabel>
          <Input
            id="block-editor-name"
            value={draft.name}
            onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="block-editor-description">Description (optional)</FieldLabel>
          <Textarea
            id="block-editor-description"
            value={draft.description ?? ''}
            onChange={(e) => setDraft((d) => ({ ...d, description: e.target.value || null }))}
          />
        </Field>
        {isDynamic && (
          <DynamicCriteriaFields
            value={draft.criteria}
            onChange={(criteria) => setDraft((d) => ({ ...d, criteria }))}
            idPrefix="block-editor-criteria"
          />
        )}
      </FieldGroup>

      {isDynamic && draft.criteria && <CriteriaPreviewPanel criteria={draft.criteria} />}

      <SaveBar dirty={dirty} saving={saveMutation.isPending} onSave={save} onDiscard={discard} error={error} />
    </section>
  )
}

// Live, debounced preview of what the draft criteria match right now.
function CriteriaPreviewPanel({ criteria }: { criteria: DynamicCriteria }) {
  const preview = usePreviewCriteria()
  const { mutate } = preview
  const invalid = validateCriteria(criteria)
  const key = JSON.stringify(criteria)

  useEffect(() => {
    if (invalid) return
    const handle = setTimeout(() => mutate(JSON.parse(key) as DynamicCriteria), 500)
    return () => clearTimeout(handle)
  }, [key, invalid, mutate])

  const data = invalid ? undefined : preview.data

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4" data-testid="criteria-preview">
      <h2 className="text-sm font-medium">Preview</h2>
      {invalid && <p className="text-sm text-muted-foreground">{invalid}</p>}
      {!invalid && preview.isError && (
        <p className="text-sm text-destructive">{errorMessage(preview.error, 'Could not load the preview.')}</p>
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
              These criteria match nothing in the catalog right now, so the block would be empty.
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
    () => [...new Set(sorted.flatMap((i) => (i.item_type === 'series' || !i.catalog_item_id ? [] : [i.catalog_item_id])))],
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
  // selected next to it.
  const [seriesSearch, setSeriesSearch] = useState('')
  const [debouncedSeriesSearch, setDebouncedSeriesSearch] = useState('')
  const [newSeriesOrder, setNewSeriesOrder] = useState<PlaybackOrder>('original')
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

  function setOrder(item: BlockItem, order: PlaybackOrder) {
    updateMutation.mutate(
      { itemId: item.id, playback_order: order },
      {
        onSuccess: () => toast.success('Playback order changed, the series restarts.'),
        onError: (err) => toast.error(errorMessage(err, 'Could not change the playback order.')),
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

  const catalogItems = sorted.filter((i) => i.item_type !== 'series')
  const seriesCount = sorted.length - catalogItems.length
  const totalMs = catalogItems.reduce((sum, item) => {
    const full = catalogById.get(item.catalog_item_id ?? '')?.duration_ms ?? 0
    return sum + (item.trim_duration_ms ?? full)
  }, 0)
  const allLoaded = catalogItems.every((i) => catalogById.has(i.catalog_item_id ?? ''))
  // A series airs until the next scheduled item, so anything after it in the
  // block never gets on the air.
  const lastSeriesIndex = sorted.map((i) => i.item_type).lastIndexOf('series')
  const shadowed = lastSeriesIndex >= 0 && lastSeriesIndex < sorted.length - 1

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
          A series item airs its episodes until the next scheduled item or the end of the guide, so the items below it
          in this block are never reached. Move the series to the end.
        </InlineWarning>
      )}

      {sorted.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-12">#</TableHead>
              <TableHead>Title</TableHead>
              <TableHead>Full length</TableHead>
              <TableHead>Trim (seconds) / order</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((item, index) => {
              const isSeries = item.item_type === 'series'
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
                    ) : catalog ? (
                      formatCatalogTitle(catalog)
                    ) : (
                      '…'
                    )}
                  </TableCell>
                  <TableCell data-testid="item-length">
                    {isSeries
                      ? item.series
                        ? `${item.series.episode_count} episode${item.series.episode_count === 1 ? '' : 's'}, ${formatDuration(item.series.total_duration_ms)}`
                        : '—'
                      : catalog
                        ? formatDuration(catalog.duration_ms)
                        : '—'}
                  </TableCell>
                  <TableCell>
                    {isSeries ? (
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
