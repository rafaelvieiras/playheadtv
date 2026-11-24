import { useMemo, useState } from 'react'
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { ChannelLogoImage } from '@/components/ChannelLogoImage'
import { GuideGrid, type GuideRow } from '@/components/GuideGrid'
import { Loading, QueryError } from '@/components/QueryError'
import { formatTime } from '@/lib/format'
import { api } from '@/lib/http'
import { epgItemTitle } from '@/lib/guide'
import { allEntriesAsCells, buildGuideCells, type GuideCell } from '@/lib/guideDisplay'
import { isTailGroup } from '@/lib/types'
import type { CatalogItem, Channel, EpgEntry, EpgItem } from '@/lib/types'

// How far back/forward from "now" the grid fetches EPG entries for.
const LOOKBACK_MS = 30 * 60 * 1000
const LOOKAHEAD_MS = 8 * 60 * 60 * 1000
const HALF_HOUR_MS = 30 * 60 * 1000

export function EpgPage() {
  const queryClient = useQueryClient()
  const [now, setNow] = useState(() => Date.now())
  // Debugging aid, UI state only: draw every entry (fillers included) at its
  // real span instead of the programs-only guide.
  const [showFiller, setShowFiller] = useState(false)

  const {
    data: channels,
    isLoading: channelsLoading,
    isError: channelsError,
    error: channelsFailure,
    refetch: refetchChannels,
  } = useQuery({
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
    error: entriesFailure,
    refetch: refetchEntries,
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

  const rows = useMemo<GuideRow[]>(
    () =>
      sortedChannels.map((channel) => ({
        id: channel.id,
        cells: cellsByChannel.get(channel.id) ?? [],
        entries: entriesByChannel.get(channel.id) ?? [],
        channel: { id: channel.id, number: channel.number, name: channel.name },
        emptyLabel: 'No programming',
        header: (
          // The whole channel cell is the target, so it stays at least a row
          // tall on a phone instead of a 20 px name.
          <Link
            to={`/channels/${channel.id}`}
            aria-label={`Open channel ${channel.number}, ${channel.name}`}
            className="flex h-full w-full min-w-0 items-center gap-2 px-3 hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            {/* Fixed slot: a square logo and a wide one take the same room,
                so the number and the name start at the same x on every row.
                The logo is contained, never stretched. */}
            <ChannelLogoImage
              imageId={channel.logo_image_id}
              name={channel.name}
              className="h-8 w-16 shrink-0 object-contain"
            />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="text-xs text-muted-foreground">{channel.number}</span>
              <span className="truncate text-sm font-medium">{channel.name}</span>
            </span>
          </Link>
        ),
      })),
    [sortedChannels, cellsByChannel, entriesByChannel],
  )

  const cellCount = useMemo(
    () => [...cellsByChannel.values()].reduce((total, cells) => total + cells.length, 0),
    [cellsByChannel],
  )
  const hiddenFillerCount = showFiller ? 0 : (entries?.length ?? 0) - cellCount

  function refresh() {
    setNow(Date.now())
    queryClient.invalidateQueries({ queryKey: ['epg'] })
  }

  const isLoading = channelsLoading || entriesLoading
  const isError = channelsError || entriesError
  const hasChannels = channels !== undefined && channels.length > 0

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">Programming guide</h1>
        <div className="flex flex-wrap items-center gap-2">
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

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the programming guide"
          error={entriesError ? entriesFailure : channelsFailure}
          onRetry={() => {
            if (channelsError) void refetchChannels()
            if (entriesError) void refetchEntries()
          }}
          stale={entries !== undefined}
        />
      )}

      {/* The grid is only drawn from an answer that actually arrived: it used
          to render "No programming" on every row while the request had failed,
          which is the one thing an operator must not confuse with a channel
          that has nothing scheduled. */}
      {hasChannels && entries !== undefined && entries.length === 0 && (
        <p className="text-sm text-muted-foreground" role="status" data-testid="epg-empty">
          No programming is stored between {formatTime(windowStart)} and {formatTime(windowEnd)}. The materializer may
          not have run yet, or no channel has a schedule.
        </p>
      )}
      {hasChannels && entries !== undefined && entries.length > 0 && (
        <GuideGrid
          rows={rows}
          windowStart={windowStart}
          windowEnd={windowEnd}
          timeZone={undefined}
          titles={catalogItemsById}
          channelColumn
          defaultView="fit"
          hiddenFillerCount={hiddenFillerCount}
          scrollLabel="Programs by channel, scroll left and right for time"
        />
      )}
    </div>
  )
}
