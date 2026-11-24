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

// What each kind is for. "Tail" and "Fallback" are the whole mental model of a
// filler rule and were explained nowhere in the app.
export const KIND_HINTS: Record<FillerKind, string> = {
  preroll: 'Before the block it is attached to.',
  midroll: 'Between the items of the block it is attached to.',
  postroll: 'After the block it is attached to.',
  tail: 'Fills the time left at the end of a block, with whole items of the pool that fit.',
  fallback: 'Plays whenever the schedule leaves a channel with nothing to air.',
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

// How the pool is walked. Weighted is the one that needs a sentence: it reads
// the Weight column of every pool item.
export const STRATEGY_HINTS: Record<SelectionStrategy, string> = {
  sequential: 'Each item in turn, in pool order, and the rotation keeps its place.',
  random: 'Any item of the pool, with the same chance for each.',
  weighted: 'Any item of the pool, but an item is as likely as its Weight in the pool: weight 3 against weight 1 plays three times as often.',
}

export const WEIGHT_HINT =
  'Weight is how strongly a Weighted rule prefers the item: with weights 3 and 1, the first plays three times as often. It has no effect on Sequential or Random.'

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
