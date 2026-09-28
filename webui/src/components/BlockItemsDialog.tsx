import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { api } from '@/lib/http'
import type { Block, BlockItem, CatalogItem } from '@/lib/types'

interface BlockItemsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  block?: Block
}

function formatDuration(ms: number): string {
  const totalMinutes = Math.round(ms / 60000)
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours === 0) return `${minutes}min`
  if (minutes === 0) return `${hours}h`
  return `${hours}h ${minutes}min`
}

export function BlockItemsDialog({ open, onOpenChange, block }: BlockItemsDialogProps) {
  const queryClient = useQueryClient()
  const blockId = block?.id
  const isDynamic = block?.criteria != null
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')

  // Simple debounce: wait 300ms of no typing before firing the search query.
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedSearch(search.trim()), 300)
    return () => clearTimeout(handle)
  }, [search])

  useEffect(() => {
    if (!open) {
      setSearch('')
      setDebouncedSearch('')
    }
  }, [open])

  const { data: items, isLoading: itemsLoading } = useQuery({
    queryKey: ['block-items', blockId],
    queryFn: () => api.get<BlockItem[]>(`/blocks/${blockId}/items`),
    enabled: open && !!blockId,
  })

  const catalogItemIds = useMemo(() => (items ?? []).map((i) => i.catalog_item_id), [items])

  const catalogItemQueries = useQueries({
    queries: catalogItemIds.map((id) => ({
      queryKey: ['catalog-item', id],
      queryFn: () => api.get<CatalogItem>(`/catalog-items/${id}`),
      staleTime: Infinity,
      enabled: open,
    })),
  })

  const catalogItemsById = useMemo(() => {
    const map = new Map<string, CatalogItem>()
    catalogItemIds.forEach((id, i) => {
      const item = catalogItemQueries[i]?.data
      if (item) map.set(id, item)
    })
    return map
  }, [catalogItemIds, catalogItemQueries])

  const { data: searchResults, isLoading: searchLoading } = useQuery({
    queryKey: ['catalog-items-search', debouncedSearch],
    queryFn: () => api.get<CatalogItem[]>(`/catalog-items?q=${encodeURIComponent(debouncedSearch)}&limit=10`),
    enabled: open && !isDynamic && debouncedSearch.length > 0,
  })

  const addMutation = useMutation({
    mutationFn: (catalogItemId: string) =>
      api.post<BlockItem>(`/blocks/${blockId}/items`, { catalog_item_id: catalogItemId, trim_duration_ms: null }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['block-items', blockId] })
      toast.success('Item added to the block.')
    },
    onError: () => {
      toast.error('Could not add the item.')
    },
  })

  const removeMutation = useMutation({
    mutationFn: (itemId: string) => api.delete(`/blocks/${blockId}/items/${itemId}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['block-items', blockId] })
      toast.success('Item removed from the block.')
    },
    onError: () => {
      toast.error('Could not remove the item.')
    },
  })

  const sortedItems = useMemo(() => [...(items ?? [])].sort((a, b) => a.position - b.position), [items])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Block items{block ? `: ${block.name}` : ''}</DialogTitle>
          <DialogDescription>
            {isDynamic
              ? 'This block is dynamic: its items are managed automatically by the criteria and cannot be added or removed manually.'
              : 'Items play in the order below. Reordering is not supported: to change the order, remove the items and add them again.'}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          {itemsLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

          {items && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12">Pos.</TableHead>
                  <TableHead>Title</TableHead>
                  <TableHead>Duration</TableHead>
                  {!isDynamic && <TableHead className="text-right">Actions</TableHead>}
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedItems.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={isDynamic ? 3 : 4} className="text-center text-muted-foreground">
                      No items in this block yet.
                    </TableCell>
                  </TableRow>
                )}
                {sortedItems.map((item) => {
                  const catalogItem = catalogItemsById.get(item.catalog_item_id)
                  const duration = item.trim_duration_ms ?? catalogItem?.duration_ms
                  return (
                    <TableRow key={item.id}>
                      <TableCell>{item.position}</TableCell>
                      <TableCell className="max-w-64 truncate">{catalogItem?.title ?? '…'}</TableCell>
                      <TableCell>{duration !== undefined ? formatDuration(duration) : '—'}</TableCell>
                      {!isDynamic && (
                        <TableCell className="text-right">
                          <Button
                            variant="destructive"
                            size="sm"
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
          )}

          {!isDynamic && (
            <div className="flex flex-col gap-2 border-t border-border pt-4">
              <Input
                placeholder="Search catalog items by title…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              {debouncedSearch.length > 0 && (
                <div className="flex flex-col gap-1">
                  {searchLoading && <p className="text-sm text-muted-foreground">Searching…</p>}
                  {searchResults && searchResults.length === 0 && (
                    <p className="text-sm text-muted-foreground">No items found.</p>
                  )}
                  {searchResults?.map((result) => (
                    <div
                      key={result.id}
                      className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5"
                    >
                      <div className="flex min-w-0 flex-col">
                        <span className="truncate text-sm">{result.title}</span>
                        <span className="text-xs text-muted-foreground">{formatDuration(result.duration_ms)}</span>
                      </div>
                      <Button
                        size="sm"
                        onClick={() => addMutation.mutate(result.id)}
                        disabled={addMutation.isPending}
                      >
                        Add
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
