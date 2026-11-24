import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { Link } from 'react-router-dom'

import { GuideGrid } from '@/components/GuideGrid'
import { InlineWarning } from '@/components/InlineWarning'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Loading, QueryError } from '@/components/QueryError'
import { useChannelEpgPreview, useChannelEpgWarnings } from '@/lib/authoring'
import { formatDurationMs } from '@/lib/filler'
import { formatTime, formatWeekday } from '@/lib/format'
import {
  airsUntilSlotEnd,
  computeSlots,
  epgItemTitle,
  findGaps,
  tooLongWarnings,
  type BlockWarning,
} from '@/lib/guide'
import { allEntriesAsCells, buildGuideCells } from '@/lib/guideDisplay'
import { api } from '@/lib/http'
import { isTailGroup } from '@/lib/types'
import type {
  Block,
  BlockItem,
  CatalogItem,
  Channel,
  CriteriaPreview,
  EpgEntry,
  EpgItem,
  ScheduleItem,
} from '@/lib/types'

const HALF_HOUR_MS = 30 * 60 * 1000
const LOOKBACK_MS = 30 * 60 * 1000
const WINDOW_MS = 24 * 60 * 60 * 1000
// Title lookups are one GET per distinct catalog item: cap how many a grid
// triggers and run them a few at a time instead of all at once.
const MAX_LOOKUPS = 150
const LOOKUP_CONCURRENCY = 6

function validTimeZone(tz: string): string | undefined {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return tz
  } catch {
    return undefined
  }
}

// Fetches the given catalog items a few at a time into the shared
// ['catalog-item', id] cache (the one the multi-channel EPG page uses) and
// returns what resolved. At most MAX_LOOKUPS distinct ids are looked up.
function useCatalogItems(ids: string[]) {
  const queryClient = useQueryClient()
  const unique = useMemo(() => [...new Set(ids)].slice(0, MAX_LOOKUPS), [ids])
  const truncated = new Set(ids).size > MAX_LOOKUPS
  const key = useMemo(() => [...unique].sort().join(','), [unique])
  const { data } = useQuery({
    queryKey: ['catalog-items-bounded', key],
    enabled: unique.length > 0,
    staleTime: Infinity,
    queryFn: async () => {
      const found: CatalogItem[] = []
      for (let i = 0; i < unique.length; i += LOOKUP_CONCURRENCY) {
        const chunk = unique.slice(i, i + LOOKUP_CONCURRENCY)
        const results = await Promise.allSettled(
          chunk.map((id) =>
            queryClient.fetchQuery({
              queryKey: ['catalog-item', id],
              queryFn: () => api.get<CatalogItem>(`/catalog-items/${id}`),
              staleTime: Infinity,
            }),
          ),
        )
        for (const r of results) if (r.status === 'fulfilled') found.push(r.value)
      }
      return found
    },
  })
  const byId = useMemo(() => new Map((data ?? []).map((c) => [c.id, c])), [data])
  return { byId, truncated, loading: unique.length > 0 && data === undefined }
}

// Warnings about the blocks the channel's active schedule items use: empty
// manual blocks, manual blocks longer than their slot and dynamic blocks whose
// criteria match nothing. Computed from the same queries the Programming tab
// uses plus the criteria-preview endpoint (one call per distinct dynamic block).
function useBlockWarnings(channel: Channel, timeZone: string | undefined): BlockWarning[] {
  const scheduleId = channel.schedule_id
  const { data: items } = useQuery({
    queryKey: ['schedule-items', scheduleId],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${scheduleId}/items`),
    enabled: !!scheduleId,
  })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const blocksById = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b])), [blocks])

  const active = useMemo(() => (items ?? []).filter((i) => i.active), [items])
  const usedBlocks = useMemo(() => {
    const seen = new Map<string, Block>()
    for (const item of active) {
      const block = blocksById.get(item.block_id ?? '')
      if (block) seen.set(block.id, block)
    }
    return [...seen.values()]
  }, [active, blocksById])
  const manual = useMemo(() => usedBlocks.filter((b) => !b.dynamic), [usedBlocks])
  const dynamic = useMemo(
    // A block held for review (no collection yet) cannot be previewed; it is skipped.
    () => usedBlocks.filter((b) => b.collection_id),
    [usedBlocks],
  )

  const itemQueries = useQueries({
    queries: manual.map((b) => ({
      queryKey: ['blocks', b.id, 'items'],
      queryFn: () => api.get<BlockItem[]>(`/blocks/${b.id}/items`),
    })),
  })
  const blockItems = useMemo(() => {
    const map = new Map<string, BlockItem[]>()
    manual.forEach((b, i) => {
      const data = itemQueries[i]?.data
      if (data) map.set(b.id, data)
    })
    return map
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [manual, itemQueries.map((q) => q.dataUpdatedAt).join(',')])

  const criteriaQueries = useQueries({
    queries: dynamic.map((b) => ({
      queryKey: ['criteria-preview', b.collection_id],
      queryFn: () => api.post<CriteriaPreview>('/catalog-items/preview-criteria', { collection_id: b.collection_id }),
      staleTime: 0,
    })),
  })

  // Server-evaluated: items whose query fails to evaluate (not detectable from
  // the stored rows alone).
  const { data: serverWarnings } = useChannelEpgWarnings(scheduleId ? channel.id : undefined)

  const slots = useMemo(() => computeSlots(items ?? [], timeZone), [items, timeZone])
  // Only blocks of a timed item that has a next timed item need durations.
  const durationIds = useMemo(() => {
    const ids: string[] = []
    for (const slot of slots) {
      for (const bi of blockItems.get(slot.item.block_id ?? '') ?? []) {
        if (airsUntilSlotEnd(bi)) break
        if (bi.trim_duration_ms === null && bi.catalog_item_id) ids.push(bi.catalog_item_id)
      }
    }
    return ids
  }, [slots, blockItems])
  const { byId: catalog, truncated } = useCatalogItems(durationIds)

  return useMemo(() => {
    const out: BlockWarning[] = []
    manual.forEach((b) => {
      const list = blockItems.get(b.id)
      if (list && list.length === 0) {
        out.push({
          kind: 'empty',
          key: `empty-${b.id}`,
          blockId: b.id,
          blockName: b.name,
          message: `Block "${b.name}" has no items, so its schedule item produces no programming.`,
        })
      }
    })
    dynamic.forEach((b, i) => {
      const data = criteriaQueries[i]?.data
      if (data && data.match_count === 0) {
        out.push({
          kind: 'no-match',
          key: `no-match-${b.id}`,
          blockId: b.id,
          blockName: b.name,
          message: `The collection of block "${b.name}" match no catalog items right now.`,
        })
      }
    })
    ;(serverWarnings ?? []).forEach((w) => {
      out.push({
        kind: w.kind === 'filler-ratio' || w.kind === 'unfilled-slot' ? w.kind : 'criteria-error',
        key: `${w.kind}-${w.block_id}-${w.block_item_id ?? ''}`,
        blockId: w.block_id,
        blockName: blocksById.get(w.block_id)?.name ?? w.block_id,
        message: `Block "${blocksById.get(w.block_id)?.name ?? w.block_id}": ${w.message}`,
      })
    })
    // A block whose durations could not all be looked up is skipped, not guessed.
    if (!truncated) out.push(...tooLongWarnings(slots, blocksById, blockItems, catalog, formatDurationMs))
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [manual, dynamic, blockItems, slots, blocksById, catalog, truncated, serverWarnings, criteriaQueries.map((q) => q.dataUpdatedAt).join(',')])
}

// Guide tab: this channel's grid, either as stored (GET /epg) or as the
// dry-run of a reset (GET /channels/{id}/epg-preview), with warnings next to
// the problems they describe. `active` is whether the tab is showing; the data
// is re-read each time it becomes visible.
export function ChannelGuide({ channel, active }: { channel: Channel; active: boolean }) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<'stored' | 'preview'>('stored')
  const [now, setNow] = useState(() => Date.now())
  // Debugging aid, UI state only: draw every entry (fillers included) at its
  // real span instead of the programs-only guide.
  const [showFiller, setShowFiller] = useState(false)
  const timeZone = validTimeZone(channel.timezone)

  const windowStart = useMemo(() => {
    const raw = now - LOOKBACK_MS
    return raw - (raw % HALF_HOUR_MS)
  }, [now])
  const windowEnd = windowStart + WINDOW_MS

  const stored = useQuery({
    queryKey: ['epg', 'channel', channel.id, windowStart, windowEnd],
    queryFn: () => api.get<EpgItem[]>(`/epg?channel_id=${channel.id}&start=${windowStart}&end=${windowEnd}&group_tail=1`),
    staleTime: 0,
  })
  const preview = useChannelEpgPreview(channel.id, { start: windowStart, end: windowEnd }, mode === 'preview')
  const current = mode === 'preview' ? preview : stored
  const entries = useMemo(() => current.data ?? [], [current.data])

  // GET /epg carries the titles, so only an entry without one (an older
  // backend) costs a catalog lookup. Tail groups never do.
  const titleIds = useMemo(
    () =>
      [...entries]
        .sort((a, b) => a.start_at - b.start_at)
        .filter((e): e is EpgEntry => !isTailGroup(e) && epgItemTitle(e) === undefined)
        .map((e) => e.catalog_item_id),
    [entries],
  )
  const titles = useCatalogItems(titleIds)
  // Gap warnings are about the real schedule, so they look at every entry.
  const gaps = useMemo(() => findGaps(entries), [entries])
  // What the grid draws: programs and fallbacks with the time of hidden
  // fillers attributed to a neighbouring program, or everything as is.
  const cells = useMemo(() => (showFiller ? allEntriesAsCells(entries) : buildGuideCells(entries)), [entries, showFiller])
  const hiddenCount = entries.length - cells.length
  const blockWarnings = useBlockWarnings(channel, timeZone)

  // Re-read whenever the tab is shown again: the schedule, blocks or grid may
  // have changed on another tab.
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    if (!active) return
    setNow(Date.now())
    queryClient.invalidateQueries({ queryKey: ['epg', 'channel', channel.id] })
    queryClient.invalidateQueries({ queryKey: ['channels', channel.id, 'epg-warnings'] })
    queryClient.invalidateQueries({ queryKey: ['criteria-preview'] })
    queryClient.invalidateQueries({ queryKey: ['schedule-items', channel.schedule_id] })
    queryClient.invalidateQueries({ queryKey: ['blocks'] })
    queryClient.invalidateQueries({ queryKey: ['channels', channel.id, 'epg-preview'] })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active])

  function refresh() {
    setNow(Date.now())
    queryClient.invalidateQueries({ queryKey: ['epg', 'channel', channel.id] })
    queryClient.invalidateQueries({ queryKey: ['channels', channel.id, 'epg-preview'] })
  }

  const fmtRange = (start: number, end: number) =>
    `${formatWeekday(start, timeZone)} ${formatTime(start, timeZone)} to ${formatTime(end, timeZone)} (${formatDurationMs(end - start)})`

  return (
    <div className="flex flex-col gap-4" data-testid="guide">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2" role="group" aria-label="Grid source">
          <Button
            size="sm"
            variant={mode === 'stored' ? 'default' : 'outline'}
            data-testid="guide-mode-stored"
            aria-pressed={mode === 'stored'}
            onClick={() => setMode('stored')}
          >
            Stored grid
          </Button>
          <Button
            size="sm"
            variant={mode === 'preview' ? 'default' : 'outline'}
            data-testid="guide-mode-preview"
            aria-pressed={mode === 'preview'}
            onClick={() => setMode('preview')}
          >
            Preview after reset
          </Button>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant={showFiller ? 'default' : 'outline'}
            size="sm"
            data-testid="guide-show-filler"
            aria-pressed={showFiller}
            onClick={() => setShowFiller((v) => !v)}
          >
            Show filler
          </Button>
          <Button variant="outline" size="sm" onClick={refresh} data-testid="guide-refresh">
            <RefreshCw className="size-4" />
            Refresh
          </Button>
        </div>
      </div>

      {mode === 'preview' && (
        <div
          className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed border-primary bg-primary/5 p-3 text-sm"
          data-testid="guide-preview-banner"
        >
          <span className="flex items-center gap-2">
            <Badge>Preview</Badge>
            This is what the grid would look like if it were reset now. Nothing is saved, and random filler picks may
            differ from a real reset.
          </span>
          <Button size="sm" variant="outline" data-testid="guide-back-to-stored" onClick={() => setMode('stored')}>
            Back to stored grid
          </Button>
        </div>
      )}

      {blockWarnings.map((w) => (
        <div key={w.key} data-testid={`guide-warning-${w.kind}`}>
          <InlineWarning
            title={
              w.kind === 'empty'
                ? 'Empty block'
                : w.kind === 'too-long'
                  ? 'Block longer than its slot'
                  : w.kind === 'criteria-error'
                    ? 'Query could not be evaluated'
                    : w.kind === 'filler-ratio'
                      ? 'Long fillers between programs'
                      : w.kind === 'unfilled-slot'
                        ? 'Slot can end empty'
                        : 'Criteria match nothing'
            }
          >
            {w.message}{' '}
            <Link to={`/blocks/${w.blockId}`} className="underline">
              Edit block
            </Link>
          </InlineWarning>
        </div>
      ))}

      {gaps.length > 0 && (
        <div data-testid="guide-warning-gaps">
          <InlineWarning title={`${gaps.length} ${gaps.length === 1 ? 'gap' : 'gaps'} in the displayed range`}>
            Nothing is scheduled between entries ({channel.timezone}):
            <ul className="mt-1 list-disc pl-5">
              {gaps.slice(0, 5).map((g) => (
                <li key={g.start}>{fmtRange(g.start, g.end)}</li>
              ))}
            </ul>
            {gaps.length > 5 && <span>and {gaps.length - 5} more.</span>}
          </InlineWarning>
        </div>
      )}

      {current.isLoading && <Loading />}
      {current.isError && (
        <QueryError
          subject={mode === 'preview' ? 'the preview' : 'the grid'}
          error={current.error}
          onRetry={() => void current.refetch()}
          stale={current.data !== undefined}
          testId="guide-error"
        />
      )}
      {current.data && entries.length === 0 && (
        <p className="text-sm text-muted-foreground" data-testid="guide-empty">
          {mode === 'preview' ? 'The preview has no programming in this range.' : 'No programming stored in this range.'}
        </p>
      )}
      {entries.length > 0 && (
        <GuideGrid
          rows={[
            {
              id: channel.id,
              cells,
              entries,
              channel: { id: channel.id, number: channel.number, name: channel.name },
            },
          ]}
          windowStart={windowStart}
          windowEnd={windowEnd}
          timeZone={timeZone}
          titles={titles.byId}
          preview={mode === 'preview'}
          hiddenFillerCount={showFiller ? 0 : hiddenCount}
          scrollLabel={`Programs on ${channel.name}, scroll left and right for time`}
          footer={
            titles.truncated ? (
              <span>Titles are looked up for the first {MAX_LOOKUPS} distinct items only.</span>
            ) : undefined
          }
        />
      )}
    </div>
  )
}
