// Pure helpers for the channel Guide: gap detection over a list of grid
// entries and the "block longer than its slot" check. Everything here works on
// data the page already loaded; nothing calls the backend.
import { DAY_LABELS } from '@/lib/schedule'
import type { Block, BlockItem, CatalogItem, EpgEntry, EpgEntryKind, ScheduleItem } from '@/lib/types'

// Silence shorter than this between two entries is a seam, not a gap: no
// warning and no gap block.
export const MIN_GAP_MS = 60_000

export interface Gap {
  start: number
  end: number
}

// Interior gaps only: silence between two displayed entries. The edges of the
// range are not gaps (the grid is materialized on a rolling window, so the
// range may simply start before or end after what exists).
export function findGaps(entries: EpgEntry[]): Gap[] {
  const sorted = [...entries].sort((a, b) => a.start_at - b.start_at)
  const gaps: Gap[] = []
  let coveredUntil: number | undefined
  for (const entry of sorted) {
    if (coveredUntil !== undefined && entry.start_at - coveredUntil >= MIN_GAP_MS) {
      gaps.push({ start: coveredUntil, end: entry.start_at })
    }
    coveredUntil = coveredUntil === undefined ? entry.end_at : Math.max(coveredUntil, entry.end_at)
  }
  return gaps
}

export const ENTRY_KIND_LABELS: Record<EpgEntryKind, string> = {
  program: 'Program',
  preroll: 'PreRoll',
  midroll: 'MidRoll',
  postroll: 'PostRoll',
  tail: 'Tail',
  fallback: 'Fallback',
}

// Tailwind classes per entry kind; program stays neutral so fillers stand out.
export const ENTRY_KIND_CLASSES: Record<EpgEntryKind, string> = {
  program: 'border-border bg-muted/40',
  preroll: 'border-sky-500/60 bg-sky-500/15',
  midroll: 'border-amber-500/60 bg-amber-500/15',
  postroll: 'border-violet-500/60 bg-violet-500/15',
  tail: 'border-emerald-500/60 bg-emerald-500/15',
  fallback: 'border-rose-500/60 bg-rose-500/15',
}

export type BlockWarningKind = 'empty' | 'too-long' | 'no-match'

export interface BlockWarning {
  kind: BlockWarningKind
  key: string
  blockId: string
  blockName: string
  message: string
}

function localDateKey(ms: number, timeZone: string | undefined): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(ms)
}

function minutesOfDay(hhmm: string): number {
  const [h, m] = hhmm.split(':').map(Number)
  return h * 60 + m
}

export interface Slot {
  item: ScheduleItem
  label: string
  lengthMs: number
}

// For each active timed item (weekly or once), the time until the next timed
// item of the same recurrence type on the same day: weekly items compare with
// the weekly items of the same weekday, once items with the once items of the
// same local date. The last timed item of a day has no slot (undefined).
export function computeSlots(items: ScheduleItem[], timeZone: string | undefined): Slot[] {
  const slots: Slot[] = []
  const weekly = items.filter((i) => i.active && i.recurrence_type === 'weekly' && i.day_of_week !== null && i.start_time_local)
  for (const item of weekly) {
    const start = minutesOfDay(item.start_time_local as string)
    const later = weekly
      .filter((o) => o.day_of_week === item.day_of_week && minutesOfDay(o.start_time_local as string) > start)
      .map((o) => minutesOfDay(o.start_time_local as string))
    if (later.length === 0) continue
    const next = Math.min(...later)
    slots.push({
      item,
      label: `${DAY_LABELS[item.day_of_week as number]} ${item.start_time_local} to ${String(Math.floor(next / 60)).padStart(2, '0')}:${String(next % 60).padStart(2, '0')}`,
      lengthMs: (next - start) * 60000,
    })
  }
  const once = items.filter((i) => i.active && i.recurrence_type === 'once' && i.start_at !== null)
  for (const item of once) {
    const day = localDateKey(item.start_at as number, timeZone)
    const later = once
      .filter((o) => (o.start_at as number) > (item.start_at as number) && localDateKey(o.start_at as number, timeZone) === day)
      .map((o) => o.start_at as number)
    if (later.length === 0) continue
    const next = Math.min(...later)
    const fmt = (ms: number) => new Date(ms).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', timeZone })
    slots.push({ item, label: `${day} ${fmt(item.start_at as number)} to ${fmt(next)}`, lengthMs: next - (item.start_at as number) })
  }
  return slots
}

// True for a series item that airs until the end of the slot, which makes
// every item after it in the block unreachable. A series item without an
// amount (an older API response) counts as such, as it behaved before amounts.
export function airsUntilSlotEnd(bi: BlockItem): boolean {
  return bi.item_type === 'series' && (bi.series?.amount?.mode ?? 'slot_end') === 'slot_end'
}

// Estimated airtime of one visit of a series item with a bounded amount.
function seriesVisitEstimateMs(bi: BlockItem): number {
  const series = bi.series
  if (!series || series.episode_count <= 0) return 0
  if (series.amount?.mode === 'count') {
    return Math.round(((series.amount.count ?? 1) * series.total_duration_ms) / series.episode_count)
  }
  return series.total_duration_ms
}

// Estimated airtime of one visit of a criteria item: N average matches, or all
// of them.
function criteriaVisitEstimateMs(bi: BlockItem): number {
  const c = bi.criteria
  if (!c || c.match_count <= 0) return 0
  if (c.amount?.mode === 'count') {
    return Math.round(((c.amount.count ?? 1) * c.total_duration_ms) / c.match_count)
  }
  return c.total_duration_ms
}

// Total airtime of a manual block, or undefined when any item's duration is
// unknown (not loaded yet). A series item that airs "until the end of the slot"
// has no length of its own, so counting stops there: only what comes before it
// can overrun the slot. Any other series item has a bounded length that
// depends on which episodes come up: it counts as that many average episodes
// (or the whole series for "until the series ends"), an estimate; a criteria
// item counts the same way over its matches.
export function blockTotalDurationMs(items: BlockItem[], catalog: Map<string, CatalogItem>): number | undefined {
  let total = 0
  for (const bi of items) {
    if (bi.item_type === 'criteria') {
      total += criteriaVisitEstimateMs(bi)
      continue
    }
    if (bi.item_type === 'series') {
      if (airsUntilSlotEnd(bi)) break
      total += seriesVisitEstimateMs(bi)
      continue
    }
    const duration = bi.trim_duration_ms ?? catalog.get(bi.catalog_item_id ?? '')?.duration_ms
    if (duration === undefined) return undefined
    total += duration
  }
  return total
}

export function tooLongWarnings(
  slots: Slot[],
  blocksById: Map<string, Block>,
  blockItems: Map<string, BlockItem[]>,
  catalog: Map<string, CatalogItem>,
  formatDuration: (ms: number) => string,
): BlockWarning[] {
  const out: BlockWarning[] = []
  for (const slot of slots) {
    const block = blocksById.get(slot.item.block_id)
    const items = blockItems.get(slot.item.block_id)
    if (!block || block.criteria || !items || items.length === 0) continue
    const total = blockTotalDurationMs(items, catalog)
    if (total === undefined || total <= slot.lengthMs) continue
    out.push({
      kind: 'too-long',
      key: `too-long-${slot.item.id}`,
      blockId: block.id,
      blockName: block.name,
      message: `Block "${block.name}" runs ${formatDuration(total)} but its slot (${slot.label}) is ${formatDuration(slot.lengthMs)}.`,
    })
  }
  return out
}
