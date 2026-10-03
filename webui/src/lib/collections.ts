// Typed API hooks for collections (issue #5, stage 2): named, reusable catalog
// queries that dynamic blocks, filler pools and criteria items reference by id.
// Every mutation that can change what a block, pool or item plays invalidates
// those lists too, since the collection's name and query show through them.
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'

import { api } from '@/lib/http'
import type { ApplyReviewResponse, Collection, CollectionInput, CollectionUsage } from '@/lib/types'

export const collectionKeys = {
  all: ['collections'] as const,
  one: (id: string) => ['collections', id] as const,
  usage: (id: string) => ['collections', id, 'usage'] as const,
}

export function useCollections() {
  return useQuery({
    queryKey: collectionKeys.all,
    queryFn: () => api.get<Collection[]>('/collections'),
  })
}

export function useCollection(id: string | undefined) {
  return useQuery({
    queryKey: collectionKeys.one(id ?? ''),
    queryFn: () => api.get<Collection>(`/collections/${id}`),
    enabled: !!id,
  })
}

// Who uses the collection: always read fresh, since it decides whether an edit
// is shared and whether a delete is refused.
export function useCollectionUsage(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: collectionKeys.usage(id ?? ''),
    queryFn: () => api.get<CollectionUsage>(`/collections/${id}/usage`),
    enabled: enabled && !!id,
    staleTime: 0,
    gcTime: 0,
  })
}

export function usageCount(u: CollectionUsage | undefined): number {
  return u ? u.blocks.length + u.filler_pools.length + u.block_items.length : 0
}

// What reads a collection shows its name/query, and its membership follows it:
// refresh all of them after the collection (or a reference to it) changes.
export function invalidateCollectionUsers(queryClient: QueryClient) {
  for (const key of ['collections', 'blocks', 'filler-pools', 'criteria-migration-review', 'criteria-preview']) {
    queryClient.invalidateQueries({ queryKey: [key] })
  }
}

export function useCreateCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: CollectionInput) => api.post<Collection>('/collections', input),
    onSuccess: () => invalidateCollectionUsers(queryClient),
  })
}

export function useUpdateCollection(id: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: CollectionInput) => api.put<Collection>(`/collections/${id}`, input),
    onSuccess: () => invalidateCollectionUsers(queryClient),
  })
}

export function useDeleteCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/collections/${id}`),
    onSuccess: () => invalidateCollectionUsers(queryClient),
  })
}

// POST /criteria-migration-review/apply — resolve one held row: creates (or
// reuses) a collection with the proposed query, or `query` when given.
export function useApplyReviewProposal() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: { owner_type: 'block' | 'filler_pool' | 'block_item'; owner_id: string; query?: unknown }) =>
      api.post<ApplyReviewResponse>('/criteria-migration-review/apply', body),
    onSuccess: () => {
      invalidateCollectionUsers(queryClient)
      queryClient.invalidateQueries({ queryKey: ['blocks'] })
    },
  })
}
