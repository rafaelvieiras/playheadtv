import type { Block, ScheduleItem } from '@/lib/types'

export const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

export function summarizeRecurrence(item: ScheduleItem): string {
  if (item.recurrence_type === 'continuous') return 'Continuous (whenever no timed item airs)'
  if (item.recurrence_type === 'weekly') {
    const day = item.day_of_week !== null ? DAY_LABELS[item.day_of_week] : '?'
    return `Every ${day} at ${item.start_time_local ?? '?'}`
  }
  if (item.start_at === null) return 'Once (no date set)'
  const date = new Date(item.start_at)
  return `Once on ${date.toLocaleDateString('en-US')} ${date.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
  })}`
}

// Items grouped by kind, each group ordered by time: continuous first (the
// base layer), then weekly by weekday and time, then one-off by date.
export function groupItemsByKind(items: ScheduleItem[]) {
  const byTime = (a: ScheduleItem, b: ScheduleItem) =>
    (a.day_of_week ?? 0) - (b.day_of_week ?? 0) ||
    (a.start_time_local ?? '').localeCompare(b.start_time_local ?? '') ||
    (a.start_at ?? 0) - (b.start_at ?? 0) ||
    a.created_at - b.created_at
  return {
    continuous: items.filter((i) => i.recurrence_type === 'continuous').sort(byTime),
    weekly: items.filter((i) => i.recurrence_type === 'weekly').sort(byTime),
    once: items.filter((i) => i.recurrence_type === 'once').sort(byTime),
  }
}

// A schedule made of seven weekly items at 00:00 (one per weekday) pointing
// to the same dynamic block is the old way of faking a 24/7 rotation; it can
// be replaced by one continuous item. Returns that block's id, else undefined.
export function legacyDailyDynamicBlock(items: ScheduleItem[], blocksById: Map<string, Block>): string | undefined {
  if (items.length !== 7) return undefined
  if (!items.every((i) => i.recurrence_type === 'weekly' && i.start_time_local === '00:00')) return undefined
  const days = new Set(items.map((i) => i.day_of_week))
  if (days.size !== 7 || ![0, 1, 2, 3, 4, 5, 6].every((d) => days.has(d))) return undefined
  const blockId = items[0].block_id
  if (!items.every((i) => i.block_id === blockId)) return undefined
  return blocksById.get(blockId)?.criteria ? blockId : undefined
}
