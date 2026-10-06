// Helpers for the cut rule set editor: the draft model (everything typed in
// minutes, as text), the conversion to the request body with the problems the
// author can fix on the spot, and the one-line summaries of the list page. The
// deep checks (ordering, shadowed rules, overlapping phases) stay in the
// backend, which the editor's live preview also asks.
import type {
  CutChapter,
  CutChapterRole,
  CutFallback,
  CutGuards,
  CutItemKind,
  CutPhase,
  CutPreviewInput,
  CutRule,
  CutRuleSet,
  CutRuleSetInput,
  CutSelector,
  CutStrategy,
  CutStrategyType,
  FillerScope,
} from '@/lib/types'

export const MAX_CUT_RULES = 20
export const MAX_CUT_PHASES = 5
export const MAX_CUT_NAME = 100

export const CUT_KIND_LABELS: Record<CutItemKind, string> = {
  movie: 'Movies',
  episode: 'Episodes',
  other: 'Other',
}

export const CUT_STRATEGY_LABELS: Record<CutStrategyType, string> = {
  interval: 'Every N minutes',
  percentage: 'At percentages of the duration',
  offsets: 'At fixed times',
  phased: 'Phased cadence',
  chapter: 'At chapters (middle, after the intro)',
  none: 'No cut',
}

// Strategies a chapter strategy may fall back to.
export const CUT_FALLBACK_TYPES: CutStrategyType[] = ['percentage', 'interval', 'offsets', 'phased', 'none']

export const CUT_ROLE_LABELS: Record<CutChapterRole, string> = {
  after_intro: 'After the intro chapter',
  middle: 'Middle chapter',
}

export type HasChaptersChoice = 'any' | 'yes' | 'no'
export type PhaseBound = 'before_end' | 'from_start'

export interface PhaseDraft {
  // Stable key for the list, never sent.
  key: string
  every_minutes: string
  // Where the phase ends (minutes); the last phase runs to the end.
  until_minutes: string
}

export interface StrategyDraft {
  type: CutStrategyType
  every_minutes: string
  // Comma separated, e.g. "33, 66".
  percentages: string
  // Comma separated minutes from the start, e.g. "20, 50".
  offsets_minutes: string
  // Phased: whether every phase but the last ends a number of minutes from the
  // start or before the end of the item.
  phase_bound: PhaseBound
  phases: PhaseDraft[]
  roles: CutChapterRole[]
  fallback_enabled: boolean
  fallback: StrategyDraft | null
}

export interface RuleDraft {
  key: string
  name: string
  kinds: CutItemKind[]
  min_minutes: string
  max_minutes: string
  has_chapters: HasChaptersChoice
  strategy: StrategyDraft
  snap_minutes: string
  // Kept as loaded: the editor has no field for a per-rule override.
  guards: CutGuards | undefined
}

export interface GuardsDraft {
  min_segment_minutes: string
  min_from_start_minutes: string
  min_from_end_minutes: string
  max_breaks: string
}

export interface RuleSetDraft {
  scope_type: FillerScope
  scope_id: string
  name: string
  position: number
  rules: RuleDraft[]
  default_enabled: boolean
  default: StrategyDraft
  guards: GuardsDraft
}

let keyCounter = 0
export function newKey(prefix: string): string {
  keyCounter += 1
  return `${prefix}-${keyCounter}`
}

export function emptyPhase(every = '', until = ''): PhaseDraft {
  return { key: newKey('phase'), every_minutes: every, until_minutes: until }
}

function baseStrategy(type: CutStrategyType): StrategyDraft {
  return {
    type,
    every_minutes: '60',
    percentages: '50',
    offsets_minutes: '',
    phase_bound: 'before_end',
    phases: [emptyPhase('40', '20'), emptyPhase('5')],
    roles: ['after_intro', 'middle'],
    fallback_enabled: false,
    fallback: null,
  }
}

// withType switches a draft to another strategy type, keeping what was typed
// and giving a chapter strategy its default fallback (one cut at 50%).
export function withType(d: StrategyDraft, type: CutStrategyType): StrategyDraft {
  const next = { ...d, type }
  if (type === 'chapter' && next.fallback === null) {
    next.fallback = baseStrategy('percentage')
    next.fallback_enabled = true
  }
  return next
}

export function emptyStrategy(type: CutStrategyType = 'percentage'): StrategyDraft {
  return withType(baseStrategy(type), type)
}

export function emptyRule(): RuleDraft {
  return {
    key: newKey('rule'),
    name: '',
    kinds: [],
    min_minutes: '',
    max_minutes: '',
    has_chapters: 'any',
    strategy: emptyStrategy('percentage'),
    snap_minutes: '',
    guards: undefined,
  }
}

export function emptyGuards(): GuardsDraft {
  return { min_segment_minutes: '', min_from_start_minutes: '', min_from_end_minutes: '', max_breaks: '' }
}

export function emptyDraft(): RuleSetDraft {
  return {
    scope_type: 'channel',
    scope_id: '',
    name: '',
    position: 0,
    rules: [emptyRule()],
    default_enabled: false,
    default: emptyStrategy('percentage'),
    guards: emptyGuards(),
  }
}

// ---- number helpers ----

// minutesText renders milliseconds as minutes without float noise.
export function minutesText(ms: number): string {
  return String(Math.round((ms / 60000) * 1000) / 1000)
}

export function msFromMinutes(minutes: number): number {
  return Math.round(minutes * 60000)
}

// parseNumber returns null for blank text and NaN for a non-number.
function parseNumber(text: string): number | null {
  const t = text.trim()
  if (t === '') return null
  const n = Number(t)
  return Number.isFinite(n) ? n : Number.NaN
}

// parseList splits on commas and spaces; null when an entry is not a number.
export function parseList(text: string): number[] | null {
  const parts = text
    .split(/[\s,;]+/)
    .map((p) => p.trim())
    .filter((p) => p !== '')
  const out: number[] = []
  for (const p of parts) {
    const n = Number(p)
    if (!Number.isFinite(n)) return null
    out.push(n)
  }
  return out
}

// ---- draft <-> API ----

function fallbackToDraft(f: CutFallback): StrategyDraft {
  return { ...strategyToDraft({ ...f }), fallback_enabled: false, fallback: null }
}

export function strategyToDraft(s: CutStrategy): StrategyDraft {
  const d = baseStrategy(s.type)
  if (s.every_ms) d.every_minutes = minutesText(s.every_ms)
  if (s.percentages) d.percentages = s.percentages.join(', ')
  if (s.offsets_ms) d.offsets_minutes = s.offsets_ms.map(minutesText).join(', ')
  if (s.phases && s.phases.length > 0) {
    d.phase_bound = s.phases.some((p) => p.until_ms) ? 'from_start' : 'before_end'
    d.phases = s.phases.map((p) => {
      const until = p.until_ms || p.until_before_end_ms
      return emptyPhase(minutesText(p.every_ms), until ? minutesText(until) : '')
    })
  }
  if (s.roles) d.roles = [...s.roles]
  if (s.type === 'chapter') {
    d.fallback_enabled = s.fallback !== undefined
    d.fallback = s.fallback ? fallbackToDraft(s.fallback) : baseStrategy('percentage')
  }
  return d
}

export function ruleToDraft(r: CutRule): RuleDraft {
  const m = r.match ?? {}
  return {
    key: newKey('rule'),
    name: r.name ?? '',
    kinds: [...(m.kinds ?? [])],
    min_minutes: m.min_duration_ms ? minutesText(m.min_duration_ms) : '',
    max_minutes: m.max_duration_ms ? minutesText(m.max_duration_ms) : '',
    has_chapters: m.has_chapters === undefined ? 'any' : m.has_chapters ? 'yes' : 'no',
    strategy: strategyToDraft(r.strategy),
    snap_minutes: r.snap_tolerance_ms ? minutesText(r.snap_tolerance_ms) : '',
    guards: r.guards,
  }
}

export function setToDraft(set: CutRuleSet): RuleSetDraft {
  const g = set.guards ?? {}
  return {
    scope_type: set.scope_type,
    scope_id: set.scope_id,
    name: set.name,
    position: set.position,
    rules: set.rules.map(ruleToDraft),
    default_enabled: set.default !== undefined && set.default !== null,
    default: set.default ? strategyToDraft(set.default) : emptyStrategy('percentage'),
    guards: {
      min_segment_minutes: g.min_segment_ms ? minutesText(g.min_segment_ms) : '',
      min_from_start_minutes: g.min_from_start_ms ? minutesText(g.min_from_start_ms) : '',
      min_from_end_minutes: g.min_from_end_ms ? minutesText(g.min_from_end_ms) : '',
      max_breaks: g.max_breaks ? String(g.max_breaks) : '',
    },
  }
}

// sameDraft compares two drafts ignoring the list keys and the fields of a
// strategy type that is not selected.
export function sameDraft(a: RuleSetDraft, b: RuleSetDraft): boolean {
  return JSON.stringify(canonical(a)) === JSON.stringify(canonical(b))
}

function canonical(d: RuleSetDraft) {
  const strat = (s: StrategyDraft): unknown => ({
    type: s.type,
    ...(s.type === 'interval' ? { every: s.every_minutes } : {}),
    ...(s.type === 'percentage' ? { pct: s.percentages } : {}),
    ...(s.type === 'offsets' ? { off: s.offsets_minutes } : {}),
    ...(s.type === 'phased'
      ? { bound: s.phase_bound, phases: s.phases.map((p, i) => [p.every_minutes, i === s.phases.length - 1 ? '' : p.until_minutes]) }
      : {}),
    ...(s.type === 'chapter'
      ? { roles: s.roles, fb: s.fallback_enabled && s.fallback ? strat(s.fallback) : null }
      : {}),
  })
  return {
    ...d,
    rules: d.rules.map(({ key: _key, strategy, ...r }) => ({ ...r, strategy: strat(strategy) })),
    default: d.default_enabled ? strat(d.default) : null,
  }
}

type Parsed<T> = { ok: true; value: T } | { ok: false; error: string }

function fail<T>(error: string): Parsed<T> {
  return { ok: false, error }
}

function positiveMs(text: string, label: string): Parsed<number> {
  const n = parseNumber(text)
  if (n === null || Number.isNaN(n) || n <= 0) return fail(`${label} must be a number of minutes above 0.`)
  return { ok: true, value: msFromMinutes(n) }
}

// strategyToInput converts a strategy draft; label prefixes the error ("Rule 2").
export function strategyToInput(d: StrategyDraft, label: string): Parsed<CutStrategy> {
  switch (d.type) {
    case 'none':
      return { ok: true, value: { type: 'none' } }
    case 'interval': {
      const every = positiveMs(d.every_minutes, `${label}: the interval`)
      return every.ok ? { ok: true, value: { type: 'interval', every_ms: every.value } } : every
    }
    case 'percentage': {
      const list = parseList(d.percentages)
      if (list === null || list.length === 0) return fail(`${label}: enter the percentages as numbers, e.g. 50 or 33, 66.`)
      return { ok: true, value: { type: 'percentage', percentages: list } }
    }
    case 'offsets': {
      const list = parseList(d.offsets_minutes)
      if (list === null || list.length === 0) return fail(`${label}: enter the cut times in minutes, e.g. 20, 50.`)
      return { ok: true, value: { type: 'offsets', offsets_ms: list.map(msFromMinutes) } }
    }
    case 'phased': {
      const phases: CutPhase[] = []
      for (let i = 0; i < d.phases.length; i++) {
        const p = d.phases[i]
        const n = i + 1
        const every = positiveMs(p.every_minutes, `${label}: phase ${n} cadence`)
        if (!every.ok) return every
        const phase: CutPhase = { every_ms: every.value }
        if (i < d.phases.length - 1) {
          const until = positiveMs(p.until_minutes, `${label}: phase ${n} end`)
          if (!until.ok) return until
          if (d.phase_bound === 'from_start') phase.until_ms = until.value
          else phase.until_before_end_ms = until.value
        }
        phases.push(phase)
      }
      return { ok: true, value: { type: 'phased', phases } }
    }
    case 'chapter': {
      if (d.roles.length === 0) return fail(`${label}: pick at least one chapter position.`)
      const value: CutStrategy = { type: 'chapter', roles: d.roles }
      if (d.fallback_enabled && d.fallback) {
        const fb = strategyToInput(d.fallback, `${label} fallback`)
        if (!fb.ok) return fb
        value.fallback = fb.value as CutFallback
      }
      return { ok: true, value }
    }
  }
}

function selectorToInput(r: RuleDraft, label: string): Parsed<CutSelector> {
  const sel: CutSelector = {}
  if (r.kinds.length > 0) sel.kinds = r.kinds
  for (const [text, field, what] of [
    [r.min_minutes, 'min_duration_ms', 'the minimum duration'],
    [r.max_minutes, 'max_duration_ms', 'the maximum duration'],
  ] as const) {
    const n = parseNumber(text)
    if (n === null) continue
    if (Number.isNaN(n) || n <= 0) return fail(`${label}: ${what} must be a number of minutes above 0, or empty.`)
    sel[field] = msFromMinutes(n)
  }
  if (r.has_chapters !== 'any') sel.has_chapters = r.has_chapters === 'yes'
  return { ok: true, value: sel }
}

function guardsToInput(g: GuardsDraft): Parsed<CutGuards> {
  const out: CutGuards = {}
  for (const [text, field, what] of [
    [g.min_segment_minutes, 'min_segment_ms', 'the minimum segment'],
    [g.min_from_start_minutes, 'min_from_start_ms', 'the minimum distance from the start'],
    [g.min_from_end_minutes, 'min_from_end_ms', 'the minimum distance from the end'],
  ] as const) {
    const n = parseNumber(text)
    if (n === null) continue
    if (Number.isNaN(n) || n < 0) return fail(`Guard rails: ${what} must be a number of minutes, or empty for the default.`)
    if (n > 0) out[field] = msFromMinutes(n)
  }
  const mb = parseNumber(g.max_breaks)
  if (mb !== null) {
    if (Number.isNaN(mb) || !Number.isInteger(mb) || mb < 0) return fail('Guard rails: the maximum number of breaks must be a whole number, or empty for the default.')
    if (mb > 0) out.max_breaks = mb
  }
  return { ok: true, value: out }
}

export interface CutConfigInput {
  rules: CutRule[]
  default?: CutStrategy
  guards: CutGuards
}

// draftToConfig converts the rules, default and guards (everything but the
// scope), or reports the first thing the author has to fix.
export function draftToConfig(d: RuleSetDraft): Parsed<CutConfigInput> {
  const rules: CutRule[] = []
  for (let i = 0; i < d.rules.length; i++) {
    const r = d.rules[i]
    const label = `Rule ${i + 1}`
    const match = selectorToInput(r, label)
    if (!match.ok) return match
    const strategy = strategyToInput(r.strategy, label)
    if (!strategy.ok) return strategy
    const rule: CutRule = { match: match.value, strategy: strategy.value }
    if (r.name.trim()) rule.name = r.name.trim()
    const snap = parseNumber(r.snap_minutes)
    if (snap !== null) {
      if (Number.isNaN(snap) || snap < 0) return fail(`${label}: the chapter snap distance must be a number of minutes, or empty.`)
      if (snap > 0) rule.snap_tolerance_ms = msFromMinutes(snap)
    }
    if (r.guards) rule.guards = r.guards
    rules.push(rule)
  }
  const guards = guardsToInput(d.guards)
  if (!guards.ok) return guards
  const config: CutConfigInput = { rules, guards: guards.value }
  if (d.default_enabled) {
    const def = strategyToInput(d.default, 'Default')
    if (!def.ok) return def
    config.default = def.value
  }
  return { ok: true, value: config }
}

// validateDraft returns the first problem found before the request is sent.
export function validateDraft(d: RuleSetDraft): string | null {
  if (!d.scope_id) return d.scope_type === 'channel' ? 'Select a channel.' : 'Select a block.'
  if (!Number.isInteger(d.position)) return 'Position must be a whole number.'
  if (d.name.length > MAX_CUT_NAME) return `The name must be at most ${MAX_CUT_NAME} characters.`
  if (d.rules.length === 0 && !d.default_enabled) return 'Add at least one rule or enable the default strategy.'
  if (d.rules.length > MAX_CUT_RULES) return `A rule set holds at most ${MAX_CUT_RULES} rules.`
  const config = draftToConfig(d)
  return config.ok ? null : config.error
}

export function draftToInput(d: RuleSetDraft): CutRuleSetInput {
  const config = draftToConfig(d)
  if (!config.ok) throw new Error(config.error)
  return {
    scope_type: d.scope_type,
    scope_id: d.scope_id,
    name: d.name.trim(),
    position: d.position,
    ...config.value,
  }
}

export function moveItem<T>(items: T[], index: number, delta: number): T[] {
  const to = index + delta
  if (index < 0 || index >= items.length || to < 0 || to >= items.length) return items
  const next = items.slice()
  ;[next[index], next[to]] = [next[to], next[index]]
  return next
}

// ---- preview ----

export interface SampleDraft {
  key: string
  kind: CutItemKind
  duration_minutes: string
  // Optional chapter starts in minutes, e.g. "0, 5, 22, 41".
  chapter_minutes: string
}

export function emptySample(kind: CutItemKind, minutes: string, chapters = ''): SampleDraft {
  return { key: newKey('sample'), kind, duration_minutes: minutes, chapter_minutes: chapters }
}

export function defaultSamples(): SampleDraft[] {
  return [emptySample('movie', '130'), emptySample('episode', '45')]
}

// sampleToPreview builds the body of POST /cut-rule-sets/preview.
export function sampleToPreview(config: CutConfigInput, s: SampleDraft): Parsed<CutPreviewInput> {
  const minutes = parseNumber(s.duration_minutes)
  if (minutes === null || Number.isNaN(minutes) || minutes <= 0) return fail('Enter the sample duration in minutes.')
  const body: CutPreviewInput = { ...config, kind: s.kind, duration_ms: msFromMinutes(minutes) }
  if (s.chapter_minutes.trim() !== '') {
    const list = parseList(s.chapter_minutes)
    if (list === null) return fail('Enter the chapter starts as minutes, e.g. 0, 5, 22.')
    const chapters: CutChapter[] = list.map((m, i) => ({ name: `Chapter ${i + 1}`, start_ms: msFromMinutes(m) }))
    body.chapters = chapters
  }
  return { ok: true, value: body }
}

// formatDuration renders milliseconds as "1h20m", "39m36s" or "45m".
export function formatDuration(ms: number): string {
  const total = Math.round(ms / 1000)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const out = `${h ? `${h}h` : ''}${m ? `${m}m` : ''}${s ? `${s}s` : ''}`
  return out === '' ? '0s' : out
}

// ---- summaries for lists ----

export function describeStrategy(s: CutStrategy | undefined): string {
  if (!s) return ''
  switch (s.type) {
    case 'none':
      return 'no cut'
    case 'interval':
      return `every ${formatDuration(s.every_ms ?? 0)}`
    case 'percentage':
      return `at ${(s.percentages ?? []).map((p) => `${p}%`).join(', ')}`
    case 'offsets':
      return `at ${(s.offsets_ms ?? []).map(formatDuration).join(', ')}`
    case 'phased': {
      const phases = s.phases ?? []
      return phases
        .map((p, i) => {
          const cadence = `every ${formatDuration(p.every_ms)}`
          if (i === phases.length - 1) return i === 0 ? cadence : `then ${cadence}`
          const until = p.until_ms ? `until ${formatDuration(p.until_ms)}` : `until the last ${formatDuration(p.until_before_end_ms ?? 0)}`
          return `${i === 0 ? '' : 'then '}${cadence} ${until}`
        })
        .join(', ')
    }
    case 'chapter': {
      const roles = (s.roles ?? []).map((r) => CUT_ROLE_LABELS[r].toLowerCase()).join(' and ')
      const fb = s.fallback ? `, else ${describeStrategy(s.fallback)}` : ''
      return `chapters: ${roles}${fb}`
    }
  }
}

export function describeSelector(m: CutSelector | undefined): string {
  const sel = m ?? {}
  const parts: string[] = []
  parts.push(sel.kinds && sel.kinds.length > 0 ? sel.kinds.map((k) => CUT_KIND_LABELS[k].toLowerCase()).join(' or ') : 'any item')
  if (sel.min_duration_ms && sel.max_duration_ms) parts.push(`${formatDuration(sel.min_duration_ms)} to ${formatDuration(sel.max_duration_ms)}`)
  else if (sel.min_duration_ms) parts.push(`at least ${formatDuration(sel.min_duration_ms)}`)
  else if (sel.max_duration_ms) parts.push(`under ${formatDuration(sel.max_duration_ms)}`)
  if (sel.has_chapters !== undefined) parts.push(sel.has_chapters ? 'with chapters' : 'without chapters')
  return parts.join(', ')
}

// describeRules lists one line per rule ("movies, at least 3h: every 1h"),
// then the default.
export function describeRules(set: Pick<CutRuleSet, 'rules' | 'default'>): string[] {
  const lines = set.rules.map((r) => `${describeSelector(r.match)}: ${describeStrategy(r.strategy)}`)
  if (set.default) lines.push(`otherwise: ${describeStrategy(set.default)}`)
  return lines
}
