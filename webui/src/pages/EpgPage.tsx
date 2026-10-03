import { formatCatalogTitle } from '@/lib/catalog'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/http'
import { epgItemClipCount, epgItemTitle } from '@/lib/guide'
import { allEntriesAsCells, buildGuideCells, type GuideCell } from '@/lib/guideDisplay'
import { isTailGroup } from '@/lib/types'
import type { CatalogItem, Channel, EpgEntry, EpgItem } from '@/lib/types'

// How far back/forward from "now" the grid fetches EPG entries for.
const LOOKBACK_MS = 30 * 60 * 1000
const LOOKAHEAD_MS = 8 * 60 * 60 * 1000
const HALF_HOUR_MS = 30 * 60 * 1000

// Time scale: each minute of programming occupies this many pixels.
const PIXELS_PER_MINUTE = 4
const CHANNEL_COLUMN_WIDTH = 180
const ROW_HEIGHT = 64
const RULER_HEIGHT = 36

function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })
}

function minutesToPixels(minutes: number): number {
  return minutes * PIXELS_PER_MINUTE
}

export function EpgPage() {
  const queryClient = useQueryClient()
  const [now, setNow] = useState(() => Date.now())
  const scrollRef = useRef<HTMLDivElement>(null)
  // Debugging aid, UI state only: draw every entry (fillers included) at its
  // real span instead of the programs-only guide.
  const [showFiller, setShowFiller] = useState(false)

  const { data: channels, isLoading: channelsLoading, isError: channelsError } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })

  // Truncate the window start to the nearest half hour so the ruler's marks
  // line up with the window boundary.
  const windowStart = useMemo(() => {
    const raw = now - LOOKBACK_MS
    return raw - (raw % HALF_HOUR_MS)
  }, [now])
  const windowEnd = now + LOOKAHEAD_MS

  const {
    data: entries,
    isLoading: entriesLoading,
    isError: entriesError,
  } = useQuery({
    queryKey: ['epg', windowStart, windowEnd],
    queryFn: () => api.get<EpgItem[]>(`/epg?start=${windowStart}&end=${windowEnd}&group_tail=1`),
  })

  const entriesByChannel = useMemo(() => {
    const map = new Map<string, EpgItem[]>()
    for (const entry of entries ?? []) {
      const list = map.get(entry.channel_id)
      if (list) {
        list.push(entry)
      } else {
        map.set(entry.channel_id, [entry])
      }
    }
    return map
  }, [entries])

  // What each channel's row draws: programs and fallbacks, with the time of
  // hidden fillers attributed to a neighbouring program (see guideDisplay.ts),
  // or every entry as is when "Show filler" is on.
  const cellsByChannel = useMemo(() => {
    const map = new Map<string, GuideCell<EpgItem>[]>()
    for (const [channelId, list] of entriesByChannel) {
      map.set(channelId, showFiller ? allEntriesAsCells(list) : buildGuideCells(list))
    }
    return map
  }, [entriesByChannel, showFiller])

  // GET /epg carries the titles; only an entry without one (an older
  // backend) is looked up. Tail groups never are.
  const catalogItemIds = useMemo(
    () => [
      ...new Set(
        [...cellsByChannel.values()]
          .flat()
          .map((c) => c.entry)
          .filter((e): e is EpgEntry => !isTailGroup(e) && epgItemTitle(e) === undefined)
          .map((e) => e.catalog_item_id),
      ),
    ],
    [cellsByChannel],
  )

  const catalogItemQueries = useQueries({
    queries: catalogItemIds.map((id) => ({
      queryKey: ['catalog-item', id],
      queryFn: () => api.get<CatalogItem>(`/catalog-items/${id}`),
      staleTime: Infinity,
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

  const sortedChannels = useMemo(
    () => [...(channels ?? [])].sort((a, b) => a.number - b.number),
    [channels],
  )

  const totalMinutes = (windowEnd - windowStart) / 60000
  const trackWidth = minutesToPixels(totalMinutes)

  const timeMarks = useMemo(() => {
    const marks: number[] = []
    for (let t = windowStart; t < windowEnd; t += HALF_HOUR_MS) {
      marks.push(t)
    }
    return marks
  }, [windowStart, windowEnd])

  const nowOffsetPx = minutesToPixels((now - windowStart) / 60000)

  // On first load (and whenever the window is refreshed), scroll so "now"
  // sits near the left edge of the visible area, with a bit of recent-past
  // context still showing. The grid itself only mounts once channels have
  // loaded, so this must re-run once that happens too — depending on
  // `windowStart` alone would fire before `scrollRef.current` exists and
  // never again, leaving the view stuck at the start of the window.
  const gridReady = channels !== undefined && channels.length > 0
  useEffect(() => {
    if (!scrollRef.current) return
    const target = Math.max(0, nowOffsetPx - minutesToPixels(20))
    scrollRef.current.scrollLeft = target
    // Only re-run when the window changes (refresh) or the grid mounts, not
    // on every render (e.g. catalog titles resolving).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [windowStart, gridReady])

  function refresh() {
    setNow(Date.now())
    queryClient.invalidateQueries({ queryKey: ['epg'] })
  }

  const isLoading = channelsLoading || entriesLoading
  const isError = channelsError || entriesError

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Programming guide</h1>
        <div className="flex items-center gap-2">
          <Button
            variant={showFiller ? 'default' : 'outline'}
            size="sm"
            data-testid="epg-show-filler"
            aria-pressed={showFiller}
            onClick={() => setShowFiller((v) => !v)}
          >
            Show filler
          </Button>
          <Button variant="outline" size="sm" onClick={refresh}>
            <RefreshCw className="size-4" />
            Refresh
          </Button>
        </div>
      </div>

      {channels && channels.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No channels yet. Create a channel before configuring the programming.
        </p>
      )}

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && (
        <p className="text-sm text-destructive">Could not load the programming guide.</p>
      )}

      {channels && channels.length > 0 && (
        <div className="overflow-hidden rounded-lg border border-border bg-card">
          <div ref={scrollRef} className="overflow-x-auto">
            <div style={{ width: CHANNEL_COLUMN_WIDTH + trackWidth }}>
              {/* Time ruler header */}
              <div
                className="sticky top-0 z-20 flex bg-card"
                style={{ height: RULER_HEIGHT }}
              >
                <div
                  className="sticky left-0 z-30 shrink-0 border-b border-r border-border bg-card"
                  style={{ width: CHANNEL_COLUMN_WIDTH }}
                />
                <div className="relative shrink-0 border-b border-border" style={{ width: trackWidth }}>
                  {timeMarks.map((mark) => (
                    <div
                      key={mark}
                      className="absolute top-0 flex h-full items-center border-l border-border pl-1.5 text-xs text-muted-foreground"
                      style={{ left: minutesToPixels((mark - windowStart) / 60000) }}
                    >
                      {formatTime(mark)}
                    </div>
                  ))}
                </div>
              </div>

              {/* Channel rows */}
              <div className="relative">
                {sortedChannels.map((channel) => {
                  const channelCells = cellsByChannel.get(channel.id) ?? []
                  return (
                    <div key={channel.id} className="flex border-b border-border last:border-b-0">
                      <div
                        className="sticky left-0 z-10 flex shrink-0 flex-col justify-center gap-0.5 border-r border-border bg-card px-3"
                        style={{ width: CHANNEL_COLUMN_WIDTH, height: ROW_HEIGHT }}
                      >
                        <span className="text-xs text-muted-foreground">{channel.number}</span>
                        <span className="truncate text-sm font-medium">{channel.name}</span>
                      </div>
                      <div
                        className="relative shrink-0"
                        style={{ width: trackWidth, height: ROW_HEIGHT }}
                      >
                        {channelCells.length === 0 && (
                          <div
                            className="sticky inline-flex h-full items-center"
                            style={{ left: CHANNEL_COLUMN_WIDTH + 8 }}
                          >
                            <span className="whitespace-nowrap text-xs text-muted-foreground">
                              No programming
                            </span>
                          </div>
                        )}
                        {channelCells.map((cell) => {
                          const entry = cell.entry
                          const isLive = cell.displayStart <= now && now < cell.displayEnd
                          const clips = epgItemClipCount(entry)
                          const item = isTailGroup(entry) ? undefined : catalogItemsById.get(entry.catalog_item_id)
                          const title = epgItemTitle(entry) ?? (item ? formatCatalogTitle(item) : '…')
                          const startOffset = Math.max(0, (cell.displayStart - windowStart) / 60000)
                          const clampedStart = Math.max(cell.displayStart, windowStart)
                          const clampedEnd = Math.min(cell.displayEnd, windowEnd)
                          const widthMinutes = Math.max(0, (clampedEnd - clampedStart) / 60000)

                          return (
                            <div
                              key={entry.id}
                              data-testid="epg-entry"
                              data-kind={entry.kind}
                              title={`${title} (${formatTime(cell.displayStart)} – ${formatTime(cell.displayEnd)})`}
                              className={`absolute top-1 flex h-[calc(100%-0.5rem)] flex-col justify-center overflow-hidden rounded-md border px-2 ${
                                isLive
                                  ? 'border-primary bg-primary/10'
                                  : 'border-border bg-muted/40'
                              }`}
                              style={{
                                left: minutesToPixels(startOffset),
                                width: Math.max(minutesToPixels(widthMinutes) - 4, 0),
                              }}
                            >
                              <span className="truncate text-sm">{title}</span>
                              <span className="truncate text-xs tabular-nums text-muted-foreground">
                                {formatTime(cell.displayStart)} – {formatTime(cell.displayEnd)}
                                {clips > 0 && ` · ${clips} ${clips === 1 ? 'clip' : 'clips'}`}
                              </span>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )
                })}

                {/* "Now" indicator line, spanning the full height of the rows. */}
                <div
                  className="pointer-events-none absolute top-0 z-20 w-0.5 bg-destructive"
                  style={{
                    left: CHANNEL_COLUMN_WIDTH + nowOffsetPx,
                    height: sortedChannels.length * ROW_HEIGHT,
                  }}
                />
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
