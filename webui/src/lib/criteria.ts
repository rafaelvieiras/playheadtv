import type { DynamicCriteria } from '@/lib/types'

// Shared validation for a block's dynamic criteria: at least one genre or a
// year bound is required (mirrors the backend's validatePoolCriteria).
export function validateCriteria(criteria: DynamicCriteria | null): string | undefined {
  if (!criteria) return undefined
  const hasGenres = (criteria.genres ?? []).length > 0
  const hasYearRange = criteria.year_from !== null || criteria.year_to !== null
  if (!hasGenres && !hasYearRange) {
    return 'Enter at least one genre or a year range for the dynamic criteria.'
  }
  return undefined
}

export function formatDuration(ms: number): string {
  const totalSeconds = Math.round(ms / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}min` : `${hours}h`
  if (minutes > 0) return seconds > 0 ? `${minutes}min ${seconds}s` : `${minutes}min`
  return `${seconds}s`
}

// Parses a stored criteria JSON string. The backend may have stored it without
// genres (JSON `genres: null` or missing), so genres is normalized to [] here;
// every consumer can then assume an array. Returns null for empty/invalid input.
export function parseCriteriaJson(raw: string | null | undefined): DynamicCriteria | null {
  if (!raw) return null
  try {
    return normalizeCriteria(JSON.parse(raw) as DynamicCriteria | null)
  } catch {
    return null
  }
}

export function normalizeCriteria(criteria: DynamicCriteria | null): DynamicCriteria | null {
  if (!criteria) return null
  return { ...criteria, genres: criteria.genres ?? [] }
}
