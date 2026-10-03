// What the guide shows for one channel's entries: only programs (and
// fallbacks) get a cell; preroll, midroll, postroll and tail entries are
// hidden and the time they occupy is attributed to a neighbouring program, so
// the grid has no false holes. Pure and dependency-free (type imports only);
// the same rules live in client/lib/epg_guide.dart.
//
// Attribution, per hidden entry:
//  - a preroll extends the NEXT program backwards (its displayed start moves
//    earlier);
//  - a postroll, midroll or tail extends the PREVIOUS program forwards (its
//    displayed end moves later);
//  - with no program on that side, it goes to the other neighbour; with none
//    there either, the entry stays unattributed and its time shows as a gap.
// A neighbour only absorbs an entry that touches it (within `toleranceMs`,
// counting what it already absorbed), so a real gap next to a filler is never
// painted over. A fallback is a visible cell of its own and absorbs nothing.
import type { EpgEntryKind } from './types.ts'

export const HIDDEN_GUIDE_KINDS: ReadonlySet<EpgEntryKind> = new Set(['preroll', 'midroll', 'postroll', 'tail'])

export function isHiddenGuideKind(kind: EpgEntryKind): boolean {
  return HIDDEN_GUIDE_KINDS.has(kind)
}

interface TimedEntry {
  kind: EpgEntryKind
  start_at: number
  end_at: number
}

// One visible cell: the entry plus the span the guide draws for it, which is
// the entry's own span widened by the hidden entries attributed to it. The
// entry's own start_at/end_at stay the real airtime; "on air now" for a cell is
// `displayStart <= now < displayEnd`, so a live filler counts as its program.
export interface GuideCell<T extends TimedEntry> {
  entry: T
  displayStart: number
  displayEnd: number
  absorbed: T[]
}

const DEFAULT_TOLERANCE_MS = 1000

export function buildGuideCells<T extends TimedEntry>(entries: readonly T[], toleranceMs = DEFAULT_TOLERANCE_MS): GuideCell<T>[] {
  const sorted = [...entries].sort((a, b) => a.start_at - b.start_at || a.end_at - b.end_at)
  const cells = new Map<T, GuideCell<T>>()
  for (const e of sorted) {
    if (!isHiddenGuideKind(e.kind)) {
      cells.set(e, { entry: e, displayStart: e.start_at, displayEnd: e.end_at, absorbed: [] })
    }
  }
  const done = new Set<T>()

  // Walks the entries toward `dir` ('next' = from the end backwards, so the
  // nearest following visible entry is known) and hands every hidden entry in
  // `pick` to the nearest visible entry on that side when it is a program that
  // touches it.
  const attribute = (pick: (e: T) => boolean, dir: 'next' | 'previous') => {
    const order = dir === 'next' ? [...sorted].reverse() : sorted
    let neighbour: GuideCell<T> | undefined
    for (const e of order) {
      const cell = cells.get(e)
      if (cell) {
        neighbour = e.kind === 'program' ? cell : undefined
        continue
      }
      if (done.has(e) || !pick(e) || !neighbour) continue
      if (dir === 'next') {
        if (e.end_at < neighbour.displayStart - toleranceMs) continue
        neighbour.displayStart = Math.min(neighbour.displayStart, e.start_at)
        neighbour.absorbed.unshift(e)
      } else {
        if (e.start_at > neighbour.displayEnd + toleranceMs) continue
        neighbour.displayEnd = Math.max(neighbour.displayEnd, e.end_at)
        neighbour.absorbed.push(e)
      }
      done.add(e)
    }
  }

  attribute((e) => e.kind === 'preroll', 'next')
  attribute((e) => e.kind !== 'preroll', 'previous')
  attribute((e) => e.kind === 'preroll', 'previous')
  attribute((e) => e.kind !== 'preroll', 'next')

  return sorted.flatMap((e) => cells.get(e) ?? [])
}

// The cells for "show filler": every entry, each drawn at its own real span.
export function allEntriesAsCells<T extends TimedEntry>(entries: readonly T[]): GuideCell<T>[] {
  return [...entries]
    .sort((a, b) => a.start_at - b.start_at || a.end_at - b.end_at)
    .map((e) => ({ entry: e, displayStart: e.start_at, displayEnd: e.end_at, absorbed: [] }))
}
