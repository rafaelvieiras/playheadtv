import type { BlockPlaybackOrder, Interleave } from '@/lib/types'

// The interleave parameters as a form holds them: the per-round count is null
// while the field holds something that is not a positive whole number.
export interface InterleaveDraft {
  per_round: number | null
  on_exhaustion: Interleave['on_exhaustion']
  series_order: Interleave['series_order']
}

export const DEFAULT_INTERLEAVE: InterleaveDraft = { per_round: 1, on_exhaustion: 'restart', series_order: 'title' }

export function toDraft(value: Interleave | null | undefined): InterleaveDraft {
  return value ? { ...value } : { ...DEFAULT_INTERLEAVE }
}

export function validateInterleave(draft: InterleaveDraft): string | undefined {
  if (draft.per_round === null) return 'Episodes per series per round must be a whole number, 1 or more.'
  return undefined
}

// Fields to spread into a block write (POST/PUT /blocks): nothing for the
// default order (the backend then stores title), the parameters for interleaved.
export function blockPlaybackPayload(
  order: BlockPlaybackOrder,
  draft: InterleaveDraft
): { playback_order?: BlockPlaybackOrder; interleave?: Interleave } {
  if (order !== 'interleaved' || draft.per_round === null) return {}
  return { playback_order: 'interleaved', interleave: { ...draft, per_round: draft.per_round } }
}

// A positive integer, or null for anything else (empty, 0, fractions, text).
export function parsePerRound(text: string): number | null {
  const n = Number(text)
  return text.trim() !== '' && Number.isInteger(n) && n >= 1 ? n : null
}
