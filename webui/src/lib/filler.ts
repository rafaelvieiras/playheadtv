// Shared bits for the filler pool and rule editors: labels, criteria helpers
// and the small formatters used on both pages.
import type { FillerKind, FillerScope, SelectionStrategy } from '@/lib/types'

export const KIND_LABELS: Record<FillerKind, string> = {
  preroll: 'PreRoll',
  midroll: 'MidRoll',
  postroll: 'PostRoll',
  tail: 'Tail',
  fallback: 'Fallback',
}

export const SCOPE_LABELS: Record<FillerScope, string> = {
  channel: 'Channel',
  block: 'Block',
}

export const STRATEGY_LABELS: Record<SelectionStrategy, string> = {
  sequential: 'Sequential',
  random: 'Random',
  weighted: 'Weighted',
}

export function formatDurationMs(ms: number): string {
  const totalMinutes = Math.round(ms / 60000)
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours === 0) return `${minutes}min`
  if (minutes === 0) return `${hours}h`
  return `${hours}h ${minutes}min`
}
