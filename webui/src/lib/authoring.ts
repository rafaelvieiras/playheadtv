// Typed API hooks for the authoring pages (issue #28's endpoints): item PUTs,
// criteria preview and the grid-reset dry-run. Mutations invalidate the list
// query of the thing they edit; every request goes through lib/http.ts.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/lib/http'
import type { Query } from '@/lib/query'
import type {
  BlockItem,
  BlockItemUpdateInput,
  CriteriaPreview,
  CriteriaReviewItem,
  EpgItem,
  FillerPoolItem,
  FillerPoolItemUpdateInput,
} from '@/lib/types'

export const authoringKeys = {
  blockItems: (blockId: string) => ['blocks', blockId, 'items'] as const,
  poolItems: (poolId: string) => ['filler-pools', poolId, 'items'] as const,
  epgPreview: (channelId: string, start?: number, end?: number) =>
    ['channels', channelId, 'epg-preview', start ?? null, end ?? null] as const,
}

// PUT /blocks/{id}/items/{item_id} — reorder and/or trim one item of a
// manual block.
export function useUpdateBlockItem(blockId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ itemId, ...body }: BlockItemUpdateInput & { itemId: string }) =>
      api.put<BlockItem>(`/blocks/${blockId}/items/${itemId}`, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: authoringKeys.blockItems(blockId) })
    },
  })
}

// PUT /filler-pools/{id}/items/{item_id} — change one item's weight in a
// manual pool.
export function useUpdateFillerPoolItem(poolId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ itemId, weight }: FillerPoolItemUpdateInput & { itemId: string }) =>
      api.put<FillerPoolItem>(`/filler-pools/${poolId}/items/${itemId}`, { weight }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: authoringKeys.poolItems(poolId) })
    },
  })
}

// POST /catalog-items/preview-criteria — what a criteria object matches right
// now. A mutation (not a query) because it is a POST with a draft body; call
// `mutate` (e.g. debounced) as the user edits the criteria.
export function usePreviewCriteria() {
  return useMutation({
    mutationFn: (criteria: Query) =>
      api.post<CriteriaPreview>('/catalog-items/preview-criteria', criteria),
  })
}

// GET /channels/{id}/epg-preview — dry-run of a grid reset: the entries the
// channel would have if reset now. `start`/`end` are optional epoch millis
// overlap filters, like GET /epg. Never cached as fresh: the preview reflects
// the stored configuration at the moment of the call.
export function useChannelEpgPreview(
  channelId: string | undefined,
  range: { start?: number; end?: number } = {},
  enabled = true,
) {
  return useQuery({
    queryKey: authoringKeys.epgPreview(channelId ?? '', range.start, range.end),
    queryFn: () => {
      const params = new URLSearchParams()
      if (range.start !== undefined) params.set('start', String(range.start))
      if (range.end !== undefined) params.set('end', String(range.end))
      const qs = params.toString()
      return api.get<EpgItem[]>(`/channels/${channelId}/epg-preview?${qs ? `${qs}&` : ''}group_tail=1`)
    },
    enabled: enabled && !!channelId,
    staleTime: 0,
    gcTime: 0,
  })
}

export interface EpgWarning {
  kind: 'criteria-error' | 'series-error'
  block_id: string
  block_item_id?: string
  message: string
}

// GET /channels/{id}/epg-warnings — problems in the channel's stored
// configuration that leave parts of its grid silently empty (a criteria item
// whose query fails to evaluate, an unreadable series spec). Evaluated by the
// server with the materializer's own loader, so it matches what a run skips.
export function useChannelEpgWarnings(channelId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['channels', channelId, 'epg-warnings'],
    queryFn: () => api.get<EpgWarning[]>(`/channels/${channelId}/epg-warnings`),
    enabled: enabled && !!channelId,
    staleTime: 0,
  })
}

// GET /criteria-migration-review — dynamic blocks and pools whose old-format
// criteria could not be converted without changing what they match.
export function useCriteriaReview(enabled = true) {
  return useQuery({
    queryKey: ['criteria-migration-review'],
    queryFn: () => api.get<CriteriaReviewItem[]>('/criteria-migration-review'),
    enabled,
  })
}
