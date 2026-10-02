import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { InlineWarning } from '@/components/InlineWarning'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { authoringKeys, useUpdateFillerPoolItem } from '@/lib/authoring'
import { formatDurationMs } from '@/lib/filler'
import { api } from '@/lib/http'
import type { CatalogItem, FillerPoolItem } from '@/lib/types'

// Items of a pool with their catalog titles and durations. `editable` (manual
// pools on the pool page) adds search-and-add, remove and weight editing;
// each of those is saved immediately, there is no draft. Read-only otherwise
// (dynamic pools, the pool shown inside the rule editor).
export function FillerPoolItems({ poolId, editable }: { poolId: string; editable: boolean }) {
  const queryClient = useQueryClient()
  const { data: items, isLoading } = useQuery({
    queryKey: authoringKeys.poolItems(poolId),
    queryFn: () => api.get<FillerPoolItem[]>(`/filler-pools/${poolId}/items`),
  })

  const catalogQueries = useQueries({
    queries: (items ?? []).map((item) => ({
      queryKey: ['catalog-item', item.catalog_item_id],
      queryFn: () => api.get<CatalogItem>(`/catalog-items/${item.catalog_item_id}`),
      staleTime: Infinity,
    })),
  })
  const catalogById = new Map<string, CatalogItem>()
  ;(items ?? []).forEach((item, i) => {
    const data = catalogQueries[i]?.data
    if (data) catalogById.set(item.catalog_item_id, data)
  })

  const totalMs = (items ?? []).reduce((sum, i) => sum + (catalogById.get(i.catalog_item_id)?.duration_ms ?? 0), 0)

  const removeMutation = useMutation({
    mutationFn: (itemId: string) => api.delete(`/filler-pools/${poolId}/items/${itemId}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: authoringKeys.poolItems(poolId) })
      toast.success('Item removed from the pool.')
    },
    onError: () => toast.error('Could not remove the item.'),
  })

  return (
    <div className="flex flex-col gap-4" data-testid="pool-items">
      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

      {items && items.length === 0 && (
        <InlineWarning title="This pool has no items">
          {editable ? 'Search the catalog below to add some.' : 'Nothing will air from it until it has items.'}
        </InlineWarning>
      )}

      {items && items.length > 0 && (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Title</TableHead>
                <TableHead>Duration</TableHead>
                <TableHead>Weight</TableHead>
                {editable && <TableHead className="text-right">Actions</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => {
                const catalogItem = catalogById.get(item.catalog_item_id)
                return (
                  <TableRow key={item.id} data-testid="pool-item-row">
                    <TableCell className="max-w-64 truncate">{catalogItem?.title ?? '…'}</TableCell>
                    <TableCell>{catalogItem ? formatDurationMs(catalogItem.duration_ms) : '—'}</TableCell>
                    <TableCell>
                      {editable ? (
                        <WeightInput poolId={poolId} item={item} title={catalogItem?.title} />
                      ) : (
                        item.weight
                      )}
                    </TableCell>
                    {editable && (
                      <TableCell className="text-right">
                        <Button
                          variant="destructive"
                          size="sm"
                          aria-label={`Remove ${catalogItem?.title ?? 'item'}`}
                          onClick={() => removeMutation.mutate(item.id)}
                          disabled={removeMutation.isPending}
                        >
                          Remove
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
          <p className="text-sm text-muted-foreground" data-testid="pool-total-duration">
            {items.length} {items.length === 1 ? 'item' : 'items'}, {formatDurationMs(totalMs)} in total
          </p>
        </>
      )}

      {editable && <CatalogSearchAdd poolId={poolId} existingIds={(items ?? []).map((i) => i.catalog_item_id)} />}
    </div>
  )
}

// Weight cell: edits locally, saves on blur or Enter when the value is a
// positive integer that differs from the stored one; otherwise reverts.
function WeightInput({ poolId, item, title }: { poolId: string; item: FillerPoolItem; title?: string }) {
  const update = useUpdateFillerPoolItem(poolId)
  const [value, setValue] = useState(String(item.weight))

  useEffect(() => {
    setValue(String(item.weight))
  }, [item.weight])

  function commit() {
    const weight = Number(value)
    if (!Number.isInteger(weight) || weight < 1) {
      setValue(String(item.weight))
      toast.error('Weight must be a positive whole number.')
      return
    }
    if (weight === item.weight) return
    update.mutate(
      { itemId: item.id, weight },
      {
        onSuccess: () => toast.success('Weight updated.'),
        onError: () => {
          setValue(String(item.weight))
          toast.error('Could not update the weight.')
        },
      },
    )
  }

  return (
    <Input
      type="number"
      min={1}
      step={1}
      className="w-20"
      aria-label={`Weight of ${title ?? 'item'}`}
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') e.currentTarget.blur()
      }}
    />
  )
}

function CatalogSearchAdd({ poolId, existingIds }: { poolId: string; existingIds: string[] }) {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const handle = setTimeout(() => setDebounced(search.trim()), 300)
    return () => clearTimeout(handle)
  }, [search])

  const { data: results, isLoading } = useQuery({
    queryKey: ['catalog-items-search', debounced],
    queryFn: () => api.get<CatalogItem[]>(`/catalog-items?q=${encodeURIComponent(debounced)}&limit=10`),
    enabled: debounced.length > 0,
  })

  const addMutation = useMutation({
    mutationFn: (catalogItemId: string) =>
      api.post<FillerPoolItem>(`/filler-pools/${poolId}/items`, { catalog_item_id: catalogItemId, weight: 1 }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: authoringKeys.poolItems(poolId) })
      toast.success('Item added to the pool.')
    },
    onError: () => toast.error('Could not add the item.'),
  })

  const existing = useMemo(() => new Set(existingIds), [existingIds])

  return (
    <div className="flex flex-col gap-2 border-t border-border pt-4">
      <Input
        placeholder="Search catalog items by title…"
        aria-label="Search catalog items"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      {debounced.length > 0 && (
        <div className="flex flex-col gap-1" data-testid="catalog-search-results">
          {isLoading && <p className="text-sm text-muted-foreground">Searching…</p>}
          {results && results.length === 0 && <p className="text-sm text-muted-foreground">No items found.</p>}
          {results?.map((result) => (
            <div
              key={result.id}
              className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5"
            >
              <div className="flex min-w-0 flex-col">
                <span className="truncate text-sm">{result.title}</span>
                <span className="text-xs text-muted-foreground">{formatDurationMs(result.duration_ms)}</span>
              </div>
              <Button
                size="sm"
                aria-label={`Add ${result.title}`}
                onClick={() => addMutation.mutate(result.id)}
                disabled={addMutation.isPending || existing.has(result.id)}
              >
                {existing.has(result.id) ? 'Added' : 'Add'}
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
