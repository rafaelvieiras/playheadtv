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

// Bounds of filler_rule.max_fit_attempts, as the backend validates them.
export const MAX_FIT_ATTEMPTS_MIN = 1
export const MAX_FIT_ATTEMPTS_MAX = 20

export function validMaxFitAttempts(n: number): boolean {
  return Number.isInteger(n) && n >= MAX_FIT_ATTEMPTS_MIN && n <= MAX_FIT_ATTEMPTS_MAX
}

export function formatDurationMs(ms: number): string {
  // Under a minute, minutes would round to "0min": show seconds instead.
  if (ms < 60000) return `${Math.round(ms / 1000)}s`
  const totalMinutes = Math.round(ms / 60000)
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours === 0) return `${minutes}min`
  if (minutes === 0) return `${hours}h`
  return `${hours}h ${minutes}min`
}
