// Shared bits for the filler pool and rule editors: labels, criteria helpers
// and the small formatters used on both pages.
import type { DynamicCriteria, FillerKind, FillerPool, FillerScope, SelectionStrategy } from '@/lib/types'

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

export function parsePoolCriteria(pool: Pick<FillerPool, 'criteria'>): DynamicCriteria | null {
  if (!pool.criteria) return null
  try {
    return JSON.parse(pool.criteria) as DynamicCriteria
  } catch {
    return null
  }
}

// Same rule the backend enforces: dynamic criteria need a genre or a year
// bound. Returns a message, or undefined when the criteria are valid (or the
// pool is manual).
export function validateCriteria(criteria: DynamicCriteria | null): string | undefined {
  if (!criteria) return undefined
  const hasGenres = criteria.genres.length > 0
  const hasYearRange = criteria.year_from !== null || criteria.year_to !== null
  if (!hasGenres && !hasYearRange) {
    return 'Enter at least one genre or a year range for the dynamic criteria.'
  }
  return undefined
}

export function summarizeCriteria(raw: string): string {
  const criteria = parsePoolCriteria({ criteria: raw })
  if (!criteria) return '—'
  const parts: string[] = [criteria.kind]
  if (criteria.genres.length > 0) parts.push(criteria.genres.join(', '))
  if (criteria.year_from !== null || criteria.year_to !== null) {
    parts.push(`${criteria.year_from ?? '…'}–${criteria.year_to ?? '…'}`)
  }
  return parts.join(' · ')
}
