// The one guide grid. The multi-channel page and a single channel's Guide tab
// used to draw two different grids: the channel one had kind tints, a legend,
// day labels and a hidden-filler count, and the page had none of it. Both now
// render through this component, with `channelColumn` switching on the sticky
// label column that a multi-channel grid needs.
//
// What the grid owes the operator:
//  - every airing is a real button with a spoken name ("Retro Movies, Movie,
//    08:00 PM to 09:30 PM"), so a title is reachable without hovering a div;
//  - clicking or focusing one writes its title, time, kind and source to the
//    panel above the grid;
//  - arrow keys walk the grid (left/right within a row, up/down through the
//    same moment on the neighbouring rows);
//  - the "now" line is labelled and follows the clock on a timer that stops
//    while the tab is hidden;
//  - "Fit window" and a zoom step decide how much of the window is visible, in
//    component state only.
//
// The cell geometry (what each cell's displayed span is) is NOT decided here:
// it arrives as `GuideCell`s from lib/guideDisplay.ts, the same attribution
// rules client/lib/epg_guide.dart follows.
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { formatCatalogTitle } from '@/lib/catalog'
import { formatDurationMs } from '@/lib/filler'
import { formatTime, formatWeekday, tzLabel } from '@/lib/format'
import {
  ENTRY_KIND_CLASSES,
  ENTRY_KIND_LABELS,
  catalogKindLabel,
  epgItemClipCount,
  epgItemTitle,
  findGaps,
  type Gap,
} from '@/lib/guide'
import type { GuideCell } from '@/lib/guideDisplay'
import { isTailGroup } from '@/lib/types'
import type { CatalogItem, EpgEntryKind, EpgItem } from '@/lib/types'

const ROW_HEIGHT = 72
const RULER_HEIGHT = 44
const LABEL_COLUMN_WIDTH = 180
const MIN_PX_PER_MINUTE = 0.2
// Below this a cell cannot hold a readable title ("T..."): the automatic fit
// gives up and keeps the manual scale until the operator asks for the overview.
const READABLE_PX_PER_MINUTE = 3
const MAX_PX_PER_MINUTE = 24
const ZOOM_STEP = 1.5
// Ruler marks this close together would overlap, so the step grows instead.
const MIN_MARK_GAP_PX = 56
// The line marks a minute, so a quarter-minute tick keeps it honest without
// waking the page more than four times a minute.
const NOW_TICK_MS = 15_000
const MARK_STEPS_MS = [30, 60, 120, 180, 360, 720, 1440].map((minutes) => minutes * 60 * 1000)

export interface GuideRowChannel {
  id: string
  number: number
  name: string
}

export interface GuideRow {
  id: string
  cells: GuideCell<EpgItem>[]
  /** Every entry of the row, hidden ones included: gaps are about all of them. */
  entries: EpgItem[]
  /** The sticky label column's content (the channel link); unused without `channelColumn`. */
  header?: ReactNode
  /** Where the row airs, for the detail panel and the cell's spoken name. */
  channel?: GuideRowChannel
  /** Shown inside the row when it has nothing to draw ("No programming"). */
  emptyLabel?: string
}

// Whether a grid that asks to start fitted really should: on a phone the whole
// window only fits as slivers a few pixels wide, so there the operator starts
// at the manual scale (readable cells) and presses "Fit window" for the
// overview. Only the initial value is decided this way; resizing keeps
// whatever the operator chose.
function startsFitted(defaultView: 'fit' | 'manual'): boolean {
  if (defaultView !== 'fit') return false
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return true
  return window.matchMedia('(min-width: 1024px)').matches
}

// The cell whose displayed span covers `time`, or the last one that starts
// before it, so ArrowUp/ArrowDown lands on the airing under the cursor.
function cellIndexAt(cells: GuideCell<EpgItem>[], time: number): number {
  if (cells.length === 0) return -1
  let best = 0
  for (let i = 0; i < cells.length; i++) {
    const cell = cells[i]
    if (cell.displayStart <= time && time < cell.displayEnd) return i
    if (cell.displayStart <= time) best = i
  }
  return best
}

export function GuideGrid({
  rows,
  windowStart,
  windowEnd,
  timeZone,
  titles,
  channelColumn = false,
  initialPixelsPerMinute = 4,
  defaultView = 'manual',
  preview = false,
  hiddenFillerCount = 0,
  scrollLabel = 'Programs by channel, scroll left and right for time',
  footer,
}: {
  rows: GuideRow[]
  windowStart: number
  windowEnd: number
  /** The zone every label is rendered in; undefined means the viewer's own. */
  timeZone: string | undefined
  titles: Map<string, CatalogItem>
  channelColumn?: boolean
  initialPixelsPerMinute?: number
  defaultView?: 'fit' | 'manual'
  preview?: boolean
  hiddenFillerCount?: number
  scrollLabel?: string
  /** Extra legend items the caller owns (e.g. the truncated-title note). */
  footer?: ReactNode
}) {
  const totalMinutes = Math.max((windowEnd - windowStart) / 60000, 1)
  const scrollRef = useRef<HTMLDivElement>(null)
  const [viewportWidth, setViewportWidth] = useState(0)
  const [fit, setFit] = useState(() => startsFitted(defaultView))
  // Pressing "Fit window" is an explicit request for the overview, so it wins
  // over the readable-minimum rule below; the automatic start does not.
  const [fitRequested, setFitRequested] = useState(false)
  const [manualPpm, setManualPpm] = useState(initialPixelsPerMinute)
  const [now, setNow] = useState(() => Date.now())
  const nowRef = useRef(now)
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  const [focusKey, setFocusKey] = useState<string | null>(null)
  const cellRefs = useRef(new Map<string, HTMLButtonElement>())
  const scrolledFor = useRef('')

  // How wide the visible part of the track is: "Fit window" needs it, and a
  // resize has to re-fit. A ResizeObserver also reports the first size, so the
  // effect never sets state itself.
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const measure = () => setViewportWidth(el.clientWidth)
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    window.addEventListener('resize', measure)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [])

  // The clock the "now" line follows. It stops while the tab is hidden (no
  // point waking up for a page nobody sees) and every timer and listener is
  // removed on unmount.
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined
    const tick = () => {
      nowRef.current = Date.now()
      setNow(nowRef.current)
    }
    const start = () => {
      if (timer !== undefined) return
      timer = setInterval(() => {
        if (!document.hidden) tick()
      }, NOW_TICK_MS)
    }
    const stop = () => {
      if (timer === undefined) return
      clearInterval(timer)
      timer = undefined
    }
    const onVisibility = () => {
      if (document.hidden) {
        stop()
      } else {
        tick()
        start()
      }
    }
    start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])

  const labelWidth = channelColumn ? LABEL_COLUMN_WIDTH : 0
  const availableWidth = Math.max(viewportWidth - labelWidth, 160)
  // "Fit window" is not clamped on the low end: on a phone the whole window
  // only fits below the manual zoom floor, and that overview is the point.
  const fitPpm = Math.min(MAX_PX_PER_MINUTE, availableWidth / totalMinutes)
  // An eight-hour window fitted into a card is a couple of pixels per minute:
  // every title becomes one clipped letter. That is a useful overview but a
  // useless default, so the automatic fit only holds while it stays readable
  // and the operator's own "Fit window" always does what it says.
  const fitReadable = fitPpm >= READABLE_PX_PER_MINUTE
  const fitActive = fit && (fitRequested || fitReadable)
  // Before the first measurement a fit view still draws at the manual scale,
  // instead of collapsing to the minimum and snapping on the next frame.
  const ppm = fitActive && viewportWidth > 0 ? fitPpm : manualPpm
  const trackWidth = totalMinutes * ppm

  const toPx = (minutes: number) => minutes * ppm
  const place = (start: number, end: number) => {
    const s = Math.max(start, windowStart)
    const e = Math.min(end, windowEnd)
    // Never zero wide: a cell nobody can see is still a keyboard stop, so a
    // very compressed airing stays a sliver instead of disappearing.
    return { left: Math.max(0, toPx((s - windowStart) / 60000)), width: Math.max(toPx((e - s) / 60000) - 4, 2) }
  }

  // Only the cells that overlap the window are drawn (and reachable): an entry
  // fetched for its overlap can start hours before it.
  const gridRows = useMemo(
    () =>
      rows.map((row) => ({
        ...row,
        cells: row.cells.filter((cell) => cell.displayEnd > windowStart && cell.displayStart < windowEnd),
      })),
    [rows, windowStart, windowEnd],
  )

  const gapsByRow = useMemo(() => new Map(gridRows.map((row) => [row.id, findGaps(row.entries)])), [gridRows])

  const markStepMs = useMemo(() => {
    const step = MARK_STEPS_MS.find((candidate) => (candidate / 60000) * ppm >= MIN_MARK_GAP_PX)
    return step ?? MARK_STEPS_MS[MARK_STEPS_MS.length - 1]
  }, [ppm])

  // The first mark is the window's own left edge (the callers align it to a
  // half hour), so the ruler is never blank before its first label.
  const marks = useMemo(() => {
    const out: number[] = []
    for (let t = windowStart; t < windowEnd; t += markStepMs) out.push(t)
    return out
  }, [windowStart, windowEnd, markStepMs])

  // Put "now" near the left edge once, when the window changes, and keep the
  // whole window in view when fitting. Re-scrolling on every tick or on every
  // title that resolves would fight the operator's own scrolling.
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const key = `${windowStart}:${fitActive}`
    if (fitActive) {
      el.scrollLeft = 0
    } else if (scrolledFor.current !== key) {
      el.scrollLeft = Math.max(0, toPx((nowRef.current - windowStart) / 60000 - 20))
    }
    scrolledFor.current = key
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [windowStart, fitActive, ppm])

  // Titles, kinds and clock times are resolved once per window and title set,
  // not on every keystroke, focus change or clock tick: `Intl` formatting is
  // the expensive part of drawing several hundred cells.
  const flat = useMemo(() => {
    const describe = (entry: EpgItem) => {
      const known = epgItemTitle(entry)
      const item = isTailGroup(entry) ? undefined : titles.get(entry.catalog_item_id)
      const title = known ?? (item ? formatCatalogTitle(item) : '…')
      // "Movie" for a movie, the entry's own kind ("Fallback") when the
      // catalog kind is unknown. A grouped tail stands for many items.
      const catalogKind = isTailGroup(entry) ? undefined : entry.item_kind
      const kind = catalogKindLabel(catalogKind) ?? catalogKindLabel(item?.kind) ?? ENTRY_KIND_LABELS[entry.kind]
      return { title, kind }
    }
    return gridRows.flatMap((row, rowIndex) =>
      row.cells.map((cell, cellIndex) => {
        const { title, kind } = describe(cell.entry)
        const timeText = `${formatTime(cell.displayStart, timeZone)} to ${formatTime(cell.displayEnd, timeZone)}`
        const parts = [title, kind, timeText]
        if (channelColumn && row.channel) parts.push(row.channel.name)
        return {
          key: `${row.id}:${cell.entry.id}`,
          cell,
          row,
          rowIndex,
          cellIndex,
          title,
          kind,
          timeText,
          label: parts.join(', '),
        }
      }),
    )
  }, [gridRows, titles, timeZone, channelColumn])
  const byKey = useMemo(() => new Map(flat.map((item) => [item.key, item])), [flat])
  const selected = selectedKey ? byKey.get(selectedKey) : undefined
  const tabbableKey = focusKey && byKey.has(focusKey) ? focusKey : flat[0]?.key

  function zoomBy(factor: number) {
    const base = fitActive && viewportWidth > 0 ? fitPpm : manualPpm
    setManualPpm(Math.min(MAX_PX_PER_MINUTE, Math.max(MIN_PX_PER_MINUTE, base * factor)))
    setFit(false)
    setFitRequested(false)
  }

  function focusCell(key: string | undefined) {
    if (!key) return
    setFocusKey(key)
    cellRefs.current.get(key)?.focus()
  }

  function onCellKeyDown(event: KeyboardEvent<HTMLButtonElement>, rowIndex: number, cellIndex: number) {
    const row = gridRows[rowIndex]
    const current = row?.cells[cellIndex]
    if (!row || !current) return
    let target: { rowIndex: number; cellIndex: number } | undefined
    if (event.key === 'ArrowRight') {
      target = { rowIndex, cellIndex: Math.min(cellIndex + 1, row.cells.length - 1) }
    } else if (event.key === 'ArrowLeft') {
      target = { rowIndex, cellIndex: Math.max(cellIndex - 1, 0) }
    } else if (event.key === 'Home') {
      target = { rowIndex, cellIndex: 0 }
    } else if (event.key === 'End') {
      target = { rowIndex, cellIndex: row.cells.length - 1 }
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      const direction = event.key === 'ArrowDown' ? 1 : -1
      for (let r = rowIndex + direction; r >= 0 && r < gridRows.length; r += direction) {
        const candidate = cellIndexAt(gridRows[r].cells, current.displayStart)
        if (candidate >= 0) {
          target = { rowIndex: r, cellIndex: candidate }
          break
        }
      }
    }
    if (!target) return
    event.preventDefault()
    const targetRow = gridRows[target.rowIndex]
    const targetCell = targetRow.cells[target.cellIndex]
    focusCell(`${targetRow.id}:${targetCell.entry.id}`)
  }

  const kindsPresent = useMemo(() => new Set(flat.map((item) => item.cell.entry.kind)), [flat])
  const selectedKind = selected?.kind
  const selectedKindLabel = selected ? ENTRY_KIND_LABELS[selected.cell.entry.kind] : undefined

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-end gap-2" role="group" aria-label="Grid zoom">
        <Button variant="outline" size="sm" data-testid="guide-zoom-out" onClick={() => zoomBy(1 / ZOOM_STEP)}>
          Zoom out
        </Button>
        <Button variant="outline" size="sm" data-testid="guide-zoom-in" onClick={() => zoomBy(ZOOM_STEP)}>
          Zoom in
        </Button>
        <Button
          variant={fitActive ? 'default' : 'outline'}
          size="sm"
          data-testid="guide-fit"
          aria-pressed={fitActive}
          onClick={() => {
            setFit(true)
            setFitRequested(true)
          }}
        >
          Fit window
        </Button>
        <span className="text-xs tabular-nums text-muted-foreground" data-testid="guide-zoom-level">
          {ppm.toFixed(1)} px per minute
        </span>
      </div>

      {/* Fixed height: focusing a cell must not move the grid under the
          operator's finger or keyboard. */}
      <div className="h-24 overflow-y-auto" data-testid="guide-detail-region">
        {selected ? (
          <section
            aria-label="Airing details"
            className="flex h-full flex-col gap-1 rounded-md border border-border bg-muted/30 p-3 text-sm"
            data-testid="guide-detail"
          >
            <p className="truncate font-medium" data-testid="guide-detail-title">
              {selected.title}
            </p>
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
              <dt className="text-muted-foreground">Time</dt>
              <dd data-testid="guide-detail-time">
                {formatWeekday(selected.cell.displayStart, timeZone)} {selected.timeText} (
                {formatDurationMs(selected.cell.displayEnd - selected.cell.displayStart)})
              </dd>
              <dt className="text-muted-foreground">Kind</dt>
              <dd data-testid="guide-detail-kind">
                {selectedKindLabel}
                {selectedKind && selectedKind !== selectedKindLabel && ` · ${selectedKind}`}
              </dd>
              <dt className="text-muted-foreground">Source</dt>
              <dd data-testid="guide-detail-source">
                {selected.cell.entry.kind === 'program'
                  ? 'Schedule'
                  : selected.cell.entry.kind === 'fallback'
                    ? 'Fallback filler'
                    : `${ENTRY_KIND_LABELS[selected.cell.entry.kind]} filler`}
                {selected.row.channel && (
                  <>
                    {' on '}
                    <Link to={`/channels/${selected.row.channel.id}`} className="underline underline-offset-4">
                      {selected.row.channel.number} {selected.row.channel.name}
                    </Link>
                  </>
                )}
              </dd>
              {epgItemClipCount(selected.cell.entry) > 0 && (
                <>
                  <dt className="text-muted-foreground">Clips</dt>
                  <dd>{epgItemClipCount(selected.cell.entry)}</dd>
                </>
              )}
              {selected.cell.absorbed.length > 0 && (
                <>
                  <dt className="text-muted-foreground">Includes</dt>
                  <dd>
                    {selected.cell.absorbed.length} hidden {selected.cell.absorbed.length === 1 ? 'entry' : 'entries'}{' '}
                    whose time belongs to this airing
                  </dd>
                </>
              )}
            </dl>
          </section>
        ) : (
          <p className="text-sm text-muted-foreground">
            Select an airing to see its title, time, kind and source.
          </p>
        )}
      </div>

      <div
        className={`overflow-hidden rounded-lg border bg-card ${preview ? 'border-dashed border-primary' : 'border-border'}`}
        data-testid="guide-grid"
        data-mode={preview ? 'preview' : 'stored'}
      >
        <div
          ref={scrollRef}
          className="overflow-x-auto focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
          role="region"
          aria-label={scrollLabel}
          tabIndex={0}
          data-testid="guide-scroll"
        >
          <div style={{ width: labelWidth + trackWidth }} className="relative">
            {/* Ruler: the clock times and, where the window crosses midnight,
                the day they belong to. */}
            <div className="sticky top-0 z-20 flex bg-card" style={{ height: RULER_HEIGHT }}>
              {channelColumn && (
                <div
                  className="sticky left-0 z-30 shrink-0 border-b border-r border-border bg-card"
                  style={{ width: LABEL_COLUMN_WIDTH }}
                />
              )}
              <div className="relative shrink-0 border-b border-border" style={{ width: trackWidth }}>
                {marks.map((mark, index) => {
                  const dayChanged = index === 0 || formatWeekday(mark, timeZone) !== formatWeekday(marks[index - 1], timeZone)
                  return (
                    <div
                      key={mark}
                      className="absolute top-0 flex h-full flex-col justify-center border-l border-border pl-1.5 text-xs text-muted-foreground"
                      style={{ left: toPx((mark - windowStart) / 60000) }}
                    >
                      {dayChanged && (
                        <span className="font-medium text-foreground">{formatWeekday(mark, timeZone)}</span>
                      )}
                      <span>{formatTime(mark, timeZone)}</span>
                    </div>
                  )
                })}
              </div>
            </div>

            {gridRows.map((row, rowIndex) => (
              <div
                key={row.id}
                className="flex border-b border-border last:border-b-0"
                style={{ height: ROW_HEIGHT }}
                data-testid="guide-row"
              >
                {channelColumn && (
                  <div
                    className="sticky left-0 z-30 flex shrink-0 items-center border-r border-border bg-card"
                    style={{ width: LABEL_COLUMN_WIDTH }}
                  >
                    {row.header}
                  </div>
                )}
                <div className="relative shrink-0" style={{ width: trackWidth, height: ROW_HEIGHT }}>
                  {(gapsByRow.get(row.id) ?? []).map((gap: Gap) => (
                    <div
                      key={`gap-${gap.start}`}
                      data-testid="guide-gap"
                      role="note"
                      aria-label={`No programming from ${formatTime(gap.start, timeZone)} to ${formatTime(gap.end, timeZone)}, ${formatDurationMs(gap.end - gap.start)}`}
                      className="absolute top-1 flex h-[calc(100%-0.5rem)] items-center justify-center overflow-hidden rounded-md border border-dashed border-destructive/60 bg-destructive/10 px-1 text-xs text-destructive"
                      style={place(gap.start, gap.end)}
                    >
                      <span className="truncate">Gap · {formatDurationMs(gap.end - gap.start)}</span>
                    </div>
                  ))}
                  {row.cells.length === 0 && row.emptyLabel && (
                    <div className="sticky inline-flex h-full items-center" style={{ left: labelWidth + 8 }}>
                      <span className="whitespace-nowrap text-xs text-muted-foreground">{row.emptyLabel}</span>
                    </div>
                  )}
                  {row.cells.map((cell, cellIndex) => {
                    const entry = cell.entry
                    const key = `${row.id}:${entry.id}`
                    const info = byKey.get(key)
                    const live = cell.displayStart <= now && now < cell.displayEnd
                    const isSelected = selectedKey === key
                    const clips = epgItemClipCount(entry)
                    return (
                      <button
                        key={entry.id}
                        ref={(element) => {
                          if (element) cellRefs.current.set(key, element)
                          else cellRefs.current.delete(key)
                        }}
                        type="button"
                        data-testid="guide-entry"
                        data-kind={entry.kind}
                        data-clips={clips || undefined}
                        data-absorbed={cell.absorbed.length || undefined}
                        data-selected={isSelected || undefined}
                        data-live={live || undefined}
                        aria-label={info?.label}
                        tabIndex={key === tabbableKey ? 0 : -1}
                        onFocus={() => {
                          // The tab stop follows the last cell the operator
                          // reached, so leaving and re-entering the grid
                          // resumes where they were.
                          setFocusKey(key)
                          setSelectedKey(key)
                        }}
                        onClick={() => setSelectedKey(key)}
                        onKeyDown={(event) => onCellKeyDown(event, rowIndex, cellIndex)}
                        className={`absolute top-1 flex h-[calc(100%-0.5rem)] cursor-pointer flex-col justify-center overflow-hidden rounded-md border px-2 text-left focus-visible:z-20 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none ${ENTRY_KIND_CLASSES[entry.kind]} ${
                          live ? 'ring-2 ring-primary' : ''
                        } ${isSelected ? 'z-10 outline-2 outline-offset-1 outline-ring' : ''}`}
                        style={place(cell.displayStart, cell.displayEnd)}
                      >
                        {entry.kind !== 'program' && (
                          <span className="truncate text-[10px] font-semibold uppercase tracking-wide">
                            {ENTRY_KIND_LABELS[entry.kind]}
                          </span>
                        )}
                        <span className="truncate text-sm">{info?.title}</span>
                        <span className="truncate text-xs tabular-nums text-muted-foreground">
                          {info?.timeText}
                          {clips > 0 && ` · ${clips} ${clips === 1 ? 'clip' : 'clips'}`}
                        </span>
                      </button>
                    )
                  })}
                </div>
              </div>
            ))}

            {/* "Now", on its own timer. Below the sticky label column, so it
                never draws over a channel name while scrolled. */}
            {now >= windowStart && now <= windowEnd && (
              <div
                data-testid="guide-now"
                aria-hidden="true"
                className="pointer-events-none absolute top-0 z-10 w-0.5 bg-destructive"
                style={{ left: labelWidth + toPx((now - windowStart) / 60000), height: RULER_HEIGHT + gridRows.length * ROW_HEIGHT }}
              >
                <span
                  className="absolute left-1 rounded-sm border border-destructive bg-card px-1 text-[10px] font-semibold text-destructive"
                  style={{ top: RULER_HEIGHT }}
                >
                  Now
                </span>
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground" data-testid="guide-legend">
        <span data-testid="guide-timezone">{tzLabel(timeZone)}</span>
        {(Object.keys(ENTRY_KIND_LABELS) as EpgEntryKind[])
          .filter((kind) => kindsPresent.has(kind))
          .map((kind) => (
            <span key={kind} className="flex items-center gap-1">
              <span className={`inline-block size-3 rounded-sm border ${ENTRY_KIND_CLASSES[kind]}`} />
              <span>{ENTRY_KIND_LABELS[kind]}</span>
            </span>
          ))}
        {hiddenFillerCount > 0 && (
          <span data-testid="guide-hidden-count">
            {hiddenFillerCount} filler {hiddenFillerCount === 1 ? 'entry' : 'entries'} hidden (their time belongs to the
            neighbouring program).
          </span>
        )}
        {footer}
      </div>
    </div>
  )
}
