import { formatDateTime } from './format.ts'
import type { Block, ScheduleItem } from '@/lib/types'

export const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
export const DAY_SHORT_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

// Weekday numbers (0 = Sunday) in the order the day picker and labels show
// them: Monday first.
export const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]

export const WEEKDAY_SHORTCUTS = {
  everyDay: [0, 1, 2, 3, 4, 5, 6],
  weekdays: [1, 2, 3, 4, 5],
  weekend: [6, 0],
} as const

function sameDays(days: number[], preset: readonly number[]): boolean {
  return days.length === preset.length && preset.every((d) => days.includes(d))
}

// Compact label of a weekly item's days: "Every day", "Weekdays", "Weekend",
// a single day's full name, or short names in Monday-first order ("Thu, Fri").
export function formatWeekdays(days: number[]): string {
  if (days.length === 0) return '?'
  if (sameDays(days, WEEKDAY_SHORTCUTS.everyDay)) return 'Every day'
  if (sameDays(days, WEEKDAY_SHORTCUTS.weekdays)) return 'Weekdays'
  if (sameDays(days, WEEKDAY_SHORTCUTS.weekend)) return 'Weekend'
  if (days.length === 1) return `Every ${DAY_LABELS[days[0]]}`
  return WEEK_ORDER.filter((d) => days.includes(d))
    .map((d) => DAY_SHORT_LABELS[d])
    .join(', ')
}

// One timestamp, one reading. A row used to be formatted here in the browser's
// zone ("11/23/2026 08:52 PM") while its own dialog showed another, and the
// pane never said which zone any of it was in. Every schedule timestamp is
// read in the channel's zone and rendered by lib/format.ts, and the pane names
// that zone (lib/format.ts's tzLabel).
function partsInZone(ms: number, timeZone?: string) {
  const parts = new Intl.DateTimeFormat('en-US', {
    ...(timeZone ? { timeZone } : {}),
    hour12: false,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(new Date(ms))
  const pick = (type: string) => Number(parts.find((p) => p.type === type)?.value)
  const hour = pick('hour')
  // Some ICU builds report midnight as hour 24.
  return {
    year: pick('year'),
    month: pick('month'),
    day: pick('day'),
    hour: hour % 24,
    minute: pick('minute'),
    second: pick('second'),
  }
}

/** How far `timeZone` is ahead of UTC at `ms`, in milliseconds. */
function zoneOffset(ms: number, timeZone: string): number {
  const p = partsInZone(ms, timeZone)
  return Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) - ms
}

/** The value a `datetime-local` input shows for `ms`, read in `timeZone`. */
export function toZonedInputValue(ms: number, timeZone?: string): string {
  const p = partsInZone(ms, timeZone)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${p.year}-${pad(p.month)}-${pad(p.day)}T${pad(p.hour)}:${pad(p.minute)}`
}

/** The instant a `datetime-local` value means in `timeZone`. */
export function fromZonedInputValue(value: string, timeZone?: string): number {
  const [date = '', time = ''] = value.split('T')
  const [year, month, day] = date.split('-').map(Number)
  const [hour, minute] = time.split(':').map(Number)
  if ([year, month, day, hour, minute].some((n) => !Number.isFinite(n))) return NaN
  if (!timeZone) return new Date(year, month - 1, day, hour, minute).getTime()
  const asIfUtc = Date.UTC(year, month - 1, day, hour, minute)
  // The offset at the guessed instant is the one that applies, unless the guess
  // lands on the other side of a daylight-saving change; the second pass settles
  // that.
  const guess = asIfUtc - zoneOffset(asIfUtc, timeZone)
  return asIfUtc - zoneOffset(guess, timeZone)
}

export function summarizeRecurrence(item: ScheduleItem, timeZone?: string): string {
  if (item.recurrence_type === 'continuous') return 'Continuous (whenever no timed item airs)'
  if (item.recurrence_type === 'weekly') {
    return `${formatWeekdays(item.days_of_week)} at ${item.start_time_local ?? '?'}`
  }
  if (item.start_at === null) return 'Once (no date set)'
  return `Once on ${formatDateTime(item.start_at, timeZone)}`
}

// Items grouped by kind, each group ordered by time: continuous first (the
// base layer), then weekly by weekday and time, then one-off by date.
export function groupItemsByKind(items: ScheduleItem[]) {
  const byTime = (a: ScheduleItem, b: ScheduleItem) =>
    Math.min(7, ...a.days_of_week) - Math.min(7, ...b.days_of_week) ||
    (a.start_time_local ?? '').localeCompare(b.start_time_local ?? '') ||
    (a.start_at ?? 0) - (b.start_at ?? 0) ||
    a.created_at - b.created_at
  return {
    continuous: items.filter((i) => i.recurrence_type === 'continuous').sort(byTime),
    weekly: items.filter((i) => i.recurrence_type === 'weekly').sort(byTime),
    once: items.filter((i) => i.recurrence_type === 'once').sort(byTime),
  }
}

// A schedule made only of weekly items at 00:00 that together cover each
// weekday exactly once (seven one-day items, one seven-day item, or anything
// in between) and point to the same dynamic block is the old way of faking a
// 24/7 rotation; it can be replaced by one continuous item. Returns that
// block's id, else undefined.
export function legacyDailyDynamicBlock(items: ScheduleItem[], blocksById: Map<string, Block>): string | undefined {
  if (items.length === 0 || items.length > 7) return undefined
  if (!items.every((i) => i.recurrence_type === 'weekly' && i.start_time_local === '00:00')) return undefined
  const days = items.flatMap((i) => i.days_of_week)
  if (days.length !== 7 || new Set(days).size !== 7 || !days.every((d) => d >= 0 && d <= 6)) return undefined
  const blockId = items[0].block_id ?? ''
  if (!items.every((i) => (i.block_id ?? '') === blockId)) return undefined
  return blocksById.get(blockId)?.dynamic ? blockId : undefined
}
