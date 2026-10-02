import { formatCatalogTitle } from '@/lib/catalog'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { Link } from 'react-router-dom'

import { InlineWarning } from '@/components/InlineWarning'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useChannelEpgPreview } from '@/lib/authoring'
import { formatDurationMs } from '@/lib/filler'
import {
  airsUntilSlotEnd,
  computeSlots,
  ENTRY_KIND_CLASSES,
  ENTRY_KIND_LABELS,
  findGaps,
  tooLongWarnings,
  type BlockWarning,
} from '@/lib/guide'
import { api } from '@/lib/http'
import { storedQuery, validateQuery } from '@/lib/query'
import type {
  Block,
  BlockItem,
  CatalogItem,
  Channel,
  CriteriaPreview,
  EpgEntry,
  EpgEntryKind,
  ScheduleItem,
} from '@/lib/types'

const HALF_HOUR_MS = 30 * 60 * 1000
const LOOKBACK_MS = 30 * 60 * 1000
const WINDOW_MS = 24 * 60 * 60 * 1000
const PIXELS_PER_MINUTE = 4
const ROW_HEIGHT = 72
const RULER_HEIGHT = 44
// Title lookups are one GET per distinct catalog item: cap how many a grid
// triggers and run them a few at a time instead of all at once.
const MAX_LOOKUPS = 150
const LOOKUP_CONCURRENCY = 6

const px = (minutes: number) => minutes * PIXELS_PER_MINUTE

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
      const block = blocksById.get(item.block_id)
      if (block) seen.set(block.id, block)
    }
    return [...seen.values()]
  }, [active, blocksById])
  const manual = useMemo(() => usedBlocks.filter((b) => !b.criteria), [usedBlocks])
  const dynamic = useMemo(
    // Old-format criteria (not yet migrated) cannot be previewed; they are skipped.
    () => usedBlocks.filter((b) => b.criteria && storedQuery(b.criteria) && !validateQuery(storedQuery(b.criteria))),
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
      queryKey: ['criteria-preview', b.criteria],
      queryFn: () => api.post<CriteriaPreview>('/catalog-items/preview-criteria', storedQuery(b.criteria)),
      staleTime: 0,
    })),
  })

  const slots = useMemo(() => computeSlots(items ?? [], timeZone), [items, timeZone])
  // Only blocks of a timed item that has a next timed item need durations.
  const durationIds = useMemo(() => {
    const ids: string[] = []
    for (const slot of slots) {
      for (const bi of blockItems.get(slot.item.block_id) ?? []) {
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
          message: `The criteria of block "${b.name}" match no catalog items right now.`,
        })
      }
    })
    // A block whose durations could not all be looked up is skipped, not guessed.
    if (!truncated) out.push(...tooLongWarnings(slots, blocksById, blockItems, catalog, formatDurationMs))
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [manual, dynamic, blockItems, slots, blocksById, catalog, truncated, criteriaQueries.map((q) => q.dataUpdatedAt).join(',')])
}

function GuideGrid({
  entries,
  windowStart,
  windowEnd,
  now,
  timeZone,
  titles,
  mode,
}: {
  entries: EpgEntry[]
  windowStart: number
  windowEnd: number
  now: number
  timeZone: string | undefined
  titles: Map<string, CatalogItem>
  mode: 'stored' | 'preview'
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const fmtTime = (ms: number) => new Date(ms).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', timeZone })
  const fmtDay = (ms: number) => new Date(ms).toLocaleDateString('en-US', { weekday: 'short', timeZone })

  const sorted = useMemo(() => [...entries].sort((a, b) => a.start_at - b.start_at), [entries])
  const gaps = useMemo(() => findGaps(entries), [entries])
  const trackWidth = px((windowEnd - windowStart) / 60000)
  const marks = useMemo(() => {
    const out: number[] = []
    for (let t = windowStart; t < windowEnd; t += HALF_HOUR_MS) out.push(t)
    return out
  }, [windowStart, windowEnd])

  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollLeft = Math.max(0, px((now - windowStart) / 60000) - px(20))
    // Re-run only when the window moves, not on every title that resolves.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [windowStart, mode])

  const place = (start: number, end: number) => {
    const s = Math.max(start, windowStart)
    const e = Math.min(end, windowEnd)
    return { left: px((s - windowStart) / 60000), width: Math.max(px((e - s) / 60000) - 4, 0) }
  }

  return (
    <div
      className={`overflow-hidden rounded-lg border bg-card ${mode === 'preview' ? 'border-dashed border-primary' : 'border-border'}`}
      data-testid="guide-grid"
      data-mode={mode}
    >
      <div ref={scrollRef} className="overflow-x-auto">
        <div style={{ width: trackWidth }} className="relative">
          <div className="relative border-b border-border" style={{ height: RULER_HEIGHT }}>
            {marks.map((mark, i) => {
              const dayChanged = i === 0 || fmtDay(mark) !== fmtDay(mark - HALF_HOUR_MS)
              return (
                <div
                  key={mark}
                  className="absolute top-0 flex h-full flex-col justify-center border-l border-border pl-1.5 text-xs text-muted-foreground"
                  style={{ left: px((mark - windowStart) / 60000) }}
                >
                  {dayChanged && <span className="font-medium text-foreground">{fmtDay(mark)}</span>}
                  <span>{fmtTime(mark)}</span>
                </div>
              )
            })}
          </div>
          <div className="relative" style={{ height: ROW_HEIGHT }}>
            {gaps.map((gap) => (
              <div
                key={`gap-${gap.start}`}
                data-testid="guide-gap"
                title={`Gap ${fmtTime(gap.start)} – ${fmtTime(gap.end)}`}
                className="absolute top-1 flex h-[calc(100%-0.5rem)] items-center justify-center overflow-hidden rounded-md border border-dashed border-destructive/60 bg-destructive/10 px-1 text-xs text-destructive"
                style={place(gap.start, gap.end)}
              >
                <span className="truncate">Gap · {formatDurationMs(gap.end - gap.start)}</span>
              </div>
            ))}
            {sorted.map((entry) => {
              const live = entry.start_at <= now && now < entry.end_at
              const item = titles.get(entry.catalog_item_id)
              const title = item ? formatCatalogTitle(item) : '…'
              return (
                <div
                  key={entry.id}
                  data-testid="guide-entry"
                  data-kind={entry.kind}
                  title={`${ENTRY_KIND_LABELS[entry.kind]}: ${title} (${fmtTime(entry.start_at)} – ${fmtTime(entry.end_at)})`}
                  className={`absolute top-1 flex h-[calc(100%-0.5rem)] flex-col justify-center overflow-hidden rounded-md border px-2 ${ENTRY_KIND_CLASSES[entry.kind]} ${live ? 'ring-2 ring-primary' : ''}`}
                  style={place(entry.start_at, entry.end_at)}
                >
                  {entry.kind !== 'program' && (
                    <span className="truncate text-[10px] font-semibold uppercase tracking-wide">{ENTRY_KIND_LABELS[entry.kind]}</span>
                  )}
                  <span className="truncate text-sm">{title}</span>
                  <span className="truncate text-xs tabular-nums text-muted-foreground">
                    {fmtTime(entry.start_at)} – {fmtTime(entry.end_at)}
                  </span>
                </div>
              )
            })}
            <div
              className="pointer-events-none absolute top-0 z-10 w-0.5 bg-destructive"
              style={{ left: px((now - windowStart) / 60000), height: ROW_HEIGHT }}
            />
          </div>
        </div>
      </div>
    </div>
  )
}

// Guide tab: this channel's grid, either as stored (GET /epg) or as the
// dry-run of a reset (GET /channels/{id}/epg-preview), with warnings next to
// the problems they describe. `active` is whether the tab is showing; the data
// is re-read each time it becomes visible.
export function ChannelGuide({ channel, active }: { channel: Channel; active: boolean }) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<'stored' | 'preview'>('stored')
  const [now, setNow] = useState(() => Date.now())
  const timeZone = validTimeZone(channel.timezone)

  const windowStart = useMemo(() => {
    const raw = now - LOOKBACK_MS
    return raw - (raw % HALF_HOUR_MS)
  }, [now])
  const windowEnd = windowStart + WINDOW_MS

  const stored = useQuery({
    queryKey: ['epg', 'channel', channel.id, windowStart, windowEnd],
    queryFn: () => api.get<EpgEntry[]>(`/epg?channel_id=${channel.id}&start=${windowStart}&end=${windowEnd}`),
    staleTime: 0,
  })
  const preview = useChannelEpgPreview(channel.id, { start: windowStart, end: windowEnd }, mode === 'preview')
  const current = mode === 'preview' ? preview : stored
  const entries = useMemo(() => current.data ?? [], [current.data])

  const titleIds = useMemo(
    () => [...entries].sort((a, b) => a.start_at - b.start_at).map((e) => e.catalog_item_id),
    [entries],
  )
  const titles = useCatalogItems(titleIds)
  const gaps = useMemo(() => findGaps(entries), [entries])
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

  const fmtRange = (start: number, end: number) => {
    const day = (ms: number) => new Date(ms).toLocaleDateString('en-US', { weekday: 'short', timeZone })
    const time = (ms: number) => new Date(ms).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', timeZone })
    return `${day(start)} ${time(start)} – ${time(end)} (${formatDurationMs(end - start)})`
  }

  const kindsPresent = new Set<EpgEntryKind>(entries.map((e) => e.kind))

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
        <Button variant="outline" size="sm" onClick={refresh} data-testid="guide-refresh">
          <RefreshCw className="size-4" />
          Refresh
        </Button>
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
            title={w.kind === 'empty' ? 'Empty block' : w.kind === 'too-long' ? 'Block longer than its slot' : 'Criteria match nothing'}
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

      {current.isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {current.isError && (
        <p className="text-sm text-destructive" data-testid="guide-error">
          Could not load the {mode === 'preview' ? 'preview' : 'grid'}.
        </p>
      )}
      {current.data && entries.length === 0 && (
        <p className="text-sm text-muted-foreground" data-testid="guide-empty">
          {mode === 'preview' ? 'The preview has no programming in this range.' : 'No programming stored in this range.'}
        </p>
      )}
      {entries.length > 0 && (
        <>
          <GuideGrid
            entries={entries}
            windowStart={windowStart}
            windowEnd={windowEnd}
            now={now}
            timeZone={timeZone}
            titles={titles.byId}
            mode={mode}
          />
          <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground" data-testid="guide-legend">
            <span>Times in {channel.timezone}.</span>
            {(Object.keys(ENTRY_KIND_LABELS) as EpgEntryKind[])
              .filter((k) => kindsPresent.has(k))
              .map((k) => (
                <span key={k} className="flex items-center gap-1">
                  <span className={`inline-block size-3 rounded-sm border ${ENTRY_KIND_CLASSES[k]}`} />
                  {ENTRY_KIND_LABELS[k]}
                </span>
              ))}
            {titles.truncated && <span>Titles are looked up for the first {MAX_LOOKUPS} distinct items only.</span>}
          </div>
        </>
      )}
    </div>
  )
}
