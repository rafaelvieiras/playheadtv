import type { SeriesSlice } from '@/lib/types'

// The editable form of a SeriesSlice: every field is the raw text of its input,
// so a half-typed value never has to round-trip through a number.
export interface SliceDraft {
  seasons: string // "1, 2, 5"
  seasonFrom: string
  seasonTo: string
  excludeSpecials: boolean
  episodeFrom: string
  episodeTo: string
  skipFirst: string
  skipLast: string
  keep: 'all' | 'first' | 'last'
  keepCount: string
  sample: string
}

export const EMPTY_SLICE_DRAFT: SliceDraft = {
  seasons: '',
  seasonFrom: '',
  seasonTo: '',
  excludeSpecials: false,
  episodeFrom: '',
  episodeTo: '',
  skipFirst: '',
  skipLast: '',
  keep: 'all',
  keepCount: '',
  sample: '',
}

const str = (n: number | undefined) => (n === undefined ? '' : String(n))

export function sliceToDraft(slice: SeriesSlice | null | undefined): SliceDraft {
  if (!slice) return EMPTY_SLICE_DRAFT
  return {
    seasons: (slice.seasons ?? []).join(', '),
    seasonFrom: str(slice.season_from),
    seasonTo: str(slice.season_to),
    excludeSpecials: !!slice.exclude_specials,
    episodeFrom: str(slice.episode_from),
    episodeTo: str(slice.episode_to),
    skipFirst: str(slice.skip_first),
    skipLast: str(slice.skip_last),
    keep: slice.first !== undefined ? 'first' : slice.last !== undefined ? 'last' : 'all',
    keepCount: str(slice.first ?? slice.last),
    sample: str(slice.sample),
  }
}

function whole(text: string, min: number, label: string): number | string {
  const t = text.trim()
  const n = Number(t)
  if (!Number.isInteger(n)) return `${label} must be a whole number.`
  if (n < min) return min === 0 ? `${label} must not be negative.` : `${label} must be ${min} or more.`
  return n
}

// draftToSlice turns the form into the request body: the slice (an empty
// object when nothing is set) or the first thing wrong with the text.
// Contradictions the backend knows about (first with last, a reversed range)
// are left to its own answer, which the preview shows.
export function draftToSlice(d: SliceDraft): { slice: SeriesSlice; error?: undefined } | { slice?: undefined; error: string } {
  const slice: SeriesSlice = {}
  const set = <K extends keyof SeriesSlice>(key: K, text: string, min: number, label: string): string | null => {
    if (text.trim() === '') return null
    const v = whole(text, min, label)
    if (typeof v === 'string') return v
    ;(slice as Record<string, unknown>)[key] = v
    return null
  }
  if (d.seasons.trim() !== '') {
    const seasons: number[] = []
    for (const part of d.seasons.split(/[\s,]+/).filter(Boolean)) {
      const v = whole(part, 0, `Season "${part}"`)
      if (typeof v === 'string') return { error: v }
      seasons.push(v)
    }
    slice.seasons = seasons
  }
  const fields: [keyof SeriesSlice, string, number, string][] = [
    ['season_from', d.seasonFrom, 0, 'Season from'],
    ['season_to', d.seasonTo, 0, 'Season to'],
    ['episode_from', d.episodeFrom, 0, 'Episode from'],
    ['episode_to', d.episodeTo, 0, 'Episode to'],
    ['skip_first', d.skipFirst, 1, 'Skip first'],
    ['skip_last', d.skipLast, 1, 'Skip last'],
    ['sample', d.sample, 1, 'Random sample'],
  ]
  for (const [key, text, min, label] of fields) {
    const err = set(key, text, min, label)
    if (err) return { error: err }
  }
  if (d.excludeSpecials) slice.exclude_specials = true
  if (d.keep !== 'all') {
    if (d.keepCount.trim() === '') return { error: `Enter how many episodes to keep (${d.keep}).` }
    const err = set(d.keep, d.keepCount, 1, d.keep === 'first' ? 'First' : 'Last')
    if (err) return { error: err }
  }
  return { slice }
}

// sliceSummary is a one-line description of a slice for the item list.
export function sliceSummary(slice: SeriesSlice | null | undefined): string {
  if (!slice) return ''
  const parts: string[] = []
  const seasons = [
    ...(slice.seasons ?? []).map((s) => `${s}`),
    ...(slice.season_from !== undefined || slice.season_to !== undefined
      ? [`${slice.season_from ?? '1'}-${slice.season_to ?? 'end'}`]
      : []),
  ]
  if (seasons.length) parts.push(`seasons ${seasons.join(', ')}`)
  if (slice.exclude_specials) parts.push('no specials')
  if (slice.episode_from !== undefined || slice.episode_to !== undefined) {
    parts.push(`episodes ${slice.episode_from ?? 1}-${slice.episode_to ?? 'end'}`)
  }
  if (slice.skip_first) parts.push(`skip first ${slice.skip_first}`)
  if (slice.skip_last) parts.push(`skip last ${slice.skip_last}`)
  if (slice.first) parts.push(`first ${slice.first}`)
  if (slice.last) parts.push(`last ${slice.last}`)
  if (slice.sample) parts.push(`random ${slice.sample}`)
  return parts.join(', ')
}

// sliceKey is a canonical string of a slice (sorted keys, empty values dropped)
// for comparing a form against the stored one.
export function sliceKey(slice: SeriesSlice | null): string {
  if (!slice) return '[]'
  const entries = Object.entries(slice).filter(([, v]) => v !== undefined && v !== false && !(Array.isArray(v) && v.length === 0))
  return JSON.stringify(entries.sort(([a], [b]) => (a < b ? -1 : 1)))
}
