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

// Limits of a cut rule set, mirrored from backend/internal/domain/cut_rule.go
// and the checks in backend/internal/cutplan/validate.go. The editor reports a
// problem before the request is sent, so the preview never asks the planner
// about a value it is going to refuse.
export const MAX_CUT_RULES = 20
export const MAX_CUT_PHASES = 5
export const MAX_CUT_NAME = 100
export const MAX_CUT_RULE_NAME = 100
export const MAX_CUT_PERCENTAGES = 10
export const MAX_CUT_OFFSETS = 20
export const MAX_CUT_BREAKS = 50
// domain.MinCutIntervalMs/MaxCutIntervalMs (60000 to 86400000 ms), in minutes.
export const MIN_CUT_INTERVAL_MINUTES = 1
export const MAX_CUT_INTERVAL_MINUTES = 1440
// domain.MaxCutSnapToleranceMs, in minutes.
export const MAX_CUT_SNAP_MINUTES = 30
// A guard rail's day-long bound (86400000 ms), in minutes.
export const MAX_CUT_GUARD_MINUTES = 1440
// The longest sample an item preview accepts (cutruleset.Preview: 7 days).
export const MAX_SAMPLE_MINUTES = 7 * 24 * 60

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
  after_intro: 'After the intro',
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

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: string }

function fail<T>(error: string): Parsed<T> {
  return { ok: false, error }
}

// CutProblem is one thing the author has to fix. id is the id of the control
// the message belongs under ('' for the form itself), so one submit can mark
// every field at once instead of reporting the first problem at the bottom of
// the page.
export interface CutProblem {
  id: string
  message: string
}

function push(problems: CutProblem[], id: string, message: string) {
  problems.push({ id, message })
}

// collectMinutes reads a decimal number of minutes inside [min, max]; the
// message names minutes, never the milliseconds the API takes.
function collectMinutes(
  text: string,
  id: string,
  problems: CutProblem[],
  what: string,
  min: number,
  max: number,
): number | null {
  const n = parseNumber(text)
  if (n === null) {
    push(problems, id, `${what} is required.`)
    return null
  }
  if (Number.isNaN(n) || n < min || n > max) {
    push(problems, id, `${what} must be a number of minutes from ${min} to ${max}.`)
    return null
  }
  return msFromMinutes(n)
}

// collectPositiveMinutes reads a decimal number of minutes above 0 (a phase
// bound has no upper limit in the API).
function collectPositiveMinutes(text: string, id: string, problems: CutProblem[], what: string): number | null {
  const n = parseNumber(text)
  if (n === null) {
    push(problems, id, `${what} is required.`)
    return null
  }
  if (Number.isNaN(n) || n <= 0) {
    push(problems, id, `${what} must be a number of minutes above 0.`)
    return null
  }
  return msFromMinutes(n)
}

function collectPercentages(text: string, id: string, problems: CutProblem[]): number[] | undefined {
  const list = parseList(text)
  if (list === null || list.length === 0) {
    push(problems, id, 'Enter the percentages as numbers, e.g. 50 or 33, 66.')
    return undefined
  }
  let bad = false
  if (list.length > MAX_CUT_PERCENTAGES) {
    push(problems, id, `At most ${MAX_CUT_PERCENTAGES} percentages.`)
    bad = true
  }
  for (const p of list) {
    if (!(p > 0 && p < 100)) {
      push(problems, id, 'Each percentage must be above 0 and below 100.')
      bad = true
      break
    }
  }
  for (let i = 1; i < list.length; i++) {
    if (list[i] <= list[i - 1]) {
      push(problems, id, 'The percentages must ascend and never repeat, e.g. 33, 66.')
      bad = true
      break
    }
  }
  return bad ? undefined : list
}

function collectOffsets(text: string, id: string, problems: CutProblem[]): number[] | undefined {
  const list = parseList(text)
  if (list === null || list.length === 0) {
    push(problems, id, 'Enter the cut times in minutes, e.g. 20, 50.')
    return undefined
  }
  let bad = false
  if (list.length > MAX_CUT_OFFSETS) {
    push(problems, id, `At most ${MAX_CUT_OFFSETS} cut times.`)
    bad = true
  }
  for (const o of list) {
    if (o <= 0) {
      push(problems, id, 'Each cut time must be a number of minutes above 0.')
      bad = true
      break
    }
  }
  for (let i = 1; i < list.length; i++) {
    if (list[i] <= list[i - 1]) {
      push(problems, id, 'The cut times must ascend and never repeat, e.g. 20, 50.')
      bad = true
      break
    }
  }
  return bad ? undefined : list
}

function collectPhases(d: StrategyDraft, id: string, problems: CutProblem[]): CutPhase[] {
  if (d.phases.length === 0 || d.phases.length > MAX_CUT_PHASES) {
    push(problems, `${id}-phases`, `A phased cadence holds 1 to ${MAX_CUT_PHASES} phases.`)
    return []
  }
  const phases: CutPhase[] = []
  let prev: number | null = null
  for (let i = 0; i < d.phases.length; i++) {
    const p = d.phases[i]
    const n = i + 1
    const every = collectMinutes(
      p.every_minutes,
      `${id}-phase-${i}-every`,
      problems,
      `Phase ${n} cadence`,
      MIN_CUT_INTERVAL_MINUTES,
      MAX_CUT_INTERVAL_MINUTES,
    )
    const phase: CutPhase = { every_ms: every ?? 0 }
    if (i < d.phases.length - 1) {
      const untilId = `${id}-phase-${i}-until`
      const until = collectPositiveMinutes(p.until_minutes, untilId, problems, `Phase ${n} end`)
      if (until !== null) {
        if (prev !== null) {
          const ordered = d.phase_bound === 'from_start' ? until > prev : until < prev
          if (!ordered) {
            push(
              problems,
              untilId,
              d.phase_bound === 'from_start'
                ? 'Each phase must end after the one before it.'
                : 'Each phase must end earlier than the one before it, counted from the end.',
            )
          }
        }
        prev = until
        if (d.phase_bound === 'from_start') phase.until_ms = until
        else phase.until_before_end_ms = until
      }
    }
    phases.push(phase)
  }
  return phases
}

// collectStrategy converts one strategy draft and records every problem it
// finds under the id of the control that has to change.
function collectStrategy(d: StrategyDraft, id: string, problems: CutProblem[]): CutStrategy {
  switch (d.type) {
    case 'none':
      return { type: 'none' }
    case 'interval': {
      const every = collectMinutes(
        d.every_minutes,
        `${id}-every`,
        problems,
        'The interval',
        MIN_CUT_INTERVAL_MINUTES,
        MAX_CUT_INTERVAL_MINUTES,
      )
      return { type: 'interval', every_ms: every ?? 0 }
    }
    case 'percentage': {
      const list = collectPercentages(d.percentages, `${id}-percentages`, problems)
      return { type: 'percentage', percentages: list ?? [] }
    }
    case 'offsets': {
      const list = collectOffsets(d.offsets_minutes, `${id}-offsets`, problems)
      return { type: 'offsets', offsets_ms: (list ?? []).map(msFromMinutes) }
    }
    case 'phased': {
      const phases = collectPhases(d, id, problems)
      return { type: 'phased', phases }
    }
    case 'chapter': {
      if (d.roles.length === 0) push(problems, `${id}-roles`, 'Pick at least one chapter position.')
      const value: CutStrategy = { type: 'chapter', roles: d.roles }
      if (d.fallback_enabled && d.fallback) {
        value.fallback = collectStrategy(d.fallback, `${id}-fallback`, problems) as CutFallback
      }
      return value
    }
  }
}

// strategyToInput converts a strategy draft; label prefixes the first problem
// ("Rule 2"). The editor uses draftProblems instead, which marks the field.
export function strategyToInput(d: StrategyDraft, label: string): Parsed<CutStrategy> {
  const problems: CutProblem[] = []
  const value = collectStrategy(d, '', problems)
  return problems.length > 0 ? fail(`${label}: ${problems[0].message}`) : { ok: true, value }
}

function collectSelector(r: RuleDraft, id: string, problems: CutProblem[]): CutSelector {
  const sel: CutSelector = {}
  if (r.kinds.length > 0) sel.kinds = r.kinds
  const min = parseNumber(r.min_minutes)
  const max = parseNumber(r.max_minutes)
  const minMs = min === null ? 0 : msFromMinutes(min)
  const maxMs = max === null ? 0 : msFromMinutes(max)
  if (min !== null && (Number.isNaN(min) || min <= 0)) {
    push(problems, `${id}-min`, 'The minimum duration must be a number of minutes above 0, or empty.')
  } else if (minMs > 0) sel.min_duration_ms = minMs
  if (max !== null && (Number.isNaN(max) || max <= 0)) {
    push(problems, `${id}-max`, 'The maximum duration must be a number of minutes above 0, or empty.')
  } else if (maxMs > 0) sel.max_duration_ms = maxMs
  if (minMs > 0 && maxMs > 0 && minMs >= maxMs) {
    push(problems, `${id}-min`, 'The minimum duration must be below the maximum.')
  }
  if (r.has_chapters !== 'any') sel.has_chapters = r.has_chapters === 'yes'
  return sel
}

// selectorCovers mirrors cutplan's covers: every item matching b also matches
// a, so a rule with selector b listed after a can never be reached.
function selectorCovers(a: CutSelector, b: CutSelector): boolean {
  if (a.kinds && a.kinds.length > 0) {
    if (!b.kinds || b.kinds.length === 0) return false
    for (const k of b.kinds) if (!a.kinds.includes(k)) return false
  }
  const aMin = a.min_duration_ms ?? 0
  const bMin = b.min_duration_ms ?? 0
  if (aMin > bMin) return false
  const aMax = a.max_duration_ms ?? 0
  const bMax = b.max_duration_ms ?? 0
  if (aMax > 0 && (bMax === 0 || bMax > aMax)) return false
  if (a.has_chapters !== undefined && (b.has_chapters === undefined || a.has_chapters !== b.has_chapters)) return false
  return true
}

function collectGuards(g: GuardsDraft, problems: CutProblem[]): CutGuards {
  const out: CutGuards = {}
  for (const [text, field, id, what] of [
    [g.min_segment_minutes, 'min_segment_ms', 'cr-guard-segment', 'The shortest segment'],
    [g.min_from_start_minutes, 'min_from_start_ms', 'cr-guard-start', 'The distance from the start'],
    [g.min_from_end_minutes, 'min_from_end_ms', 'cr-guard-end', 'The distance from the end'],
  ] as const) {
    const n = parseNumber(text)
    if (n === null) continue
    if (Number.isNaN(n) || n < 0 || n > MAX_CUT_GUARD_MINUTES) {
      push(problems, id, `${what} must be a number of minutes from 0 to ${MAX_CUT_GUARD_MINUTES}, or empty for the default.`)
      continue
    }
    if (n > 0) out[field] = msFromMinutes(n)
  }
  const mb = parseNumber(g.max_breaks)
  if (mb !== null) {
    if (Number.isNaN(mb) || !Number.isInteger(mb) || mb < 0 || mb > MAX_CUT_BREAKS) {
      push(problems, 'cr-guard-max', `The maximum number of breaks must be a whole number from 0 to ${MAX_CUT_BREAKS}.`)
    } else if (mb > 0) {
      out.max_breaks = mb
    }
  }
  return out
}

export interface CutConfigInput {
  rules: CutRule[]
  default?: CutStrategy
  guards: CutGuards
}

interface CollectedConfig {
  config: CutConfigInput
  problems: CutProblem[]
}

const RULE_ID = (index: number) => `cr-rule-${index}`

function collectConfig(d: RuleSetDraft): CollectedConfig {
  const problems: CutProblem[] = []
  const rules: CutRule[] = []
  for (let i = 0; i < d.rules.length; i++) {
    const r = d.rules[i]
    const id = RULE_ID(i)
    if (r.name.trim().length > MAX_CUT_RULE_NAME) {
      push(problems, `${id}-name`, `The rule name must be at most ${MAX_CUT_RULE_NAME} characters.`)
    }
    const rule: CutRule = { match: collectSelector(r, id, problems), strategy: collectStrategy(r.strategy, id, problems) }
    if (r.name.trim()) rule.name = r.name.trim()
    const snap = parseNumber(r.snap_minutes)
    if (snap !== null) {
      if (Number.isNaN(snap) || snap < 0 || snap > MAX_CUT_SNAP_MINUTES) {
        push(problems, `${id}-snap`, `The snap distance must be from 0 to ${MAX_CUT_SNAP_MINUTES} minutes, or empty.`)
      } else if (snap > 0) {
        rule.snap_tolerance_ms = msFromMinutes(snap)
      }
    }
    if (r.guards) rule.guards = r.guards
    rules.push(rule)
  }
  for (let i = 0; i < rules.length; i++) {
    for (let j = 0; j < i; j++) {
      if (selectorCovers(rules[j].match ?? {}, rules[i].match ?? {})) {
        push(problems, `${RULE_ID(i)}-card`, `Can never match: rule ${j + 1} already matches every item this one would.`)
      }
    }
  }
  const config: CutConfigInput = { rules, guards: collectGuards(d.guards, problems) }
  if (d.default_enabled) {
    config.default = collectStrategy(d.default, 'cr-default', problems)
  } else if (rules.length === 0) {
    push(problems, 'cr-default-enabled', 'Add at least one rule, or enable the default strategy.')
  }
  if (rules.length > MAX_CUT_RULES) push(problems, '', `A rule set holds at most ${MAX_CUT_RULES} rules.`)
  return { config, problems }
}

// draftProblems reports every problem in the draft, each bound to the control
// that has to change. An empty list means the request can be sent.
export function draftProblems(d: RuleSetDraft): CutProblem[] {
  const problems: CutProblem[] = []
  if (!d.scope_id) {
    push(problems, 'cr-scope-target', d.scope_type === 'channel' ? 'Select a channel.' : 'Select a block.')
  }
  if (!Number.isInteger(d.position)) push(problems, 'cr-position', 'Position must be a whole number.')
  if (d.name.length > MAX_CUT_NAME) push(problems, 'cr-name', `The name must be at most ${MAX_CUT_NAME} characters.`)
  problems.push(...collectConfig(d).problems)
  return problems
}

// draftToConfig converts the rules, default and guards (everything but the
// scope), or reports the first thing the author has to fix.
export function draftToConfig(d: RuleSetDraft): Parsed<CutConfigInput> {
  const { config, problems } = collectConfig(d)
  return problems.length > 0 ? fail(problems[0].message) : { ok: true, value: config }
}

// problemsById indexes the problems by control id (the first message per
// control wins), the shape the fields read.
export function problemsById(problems: CutProblem[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of problems) {
    if (p.id && !(p.id in out)) out[p.id] = p.message
  }
  return out
}

// validateDraft returns the first problem found before the request is sent.
export function validateDraft(d: RuleSetDraft): string | null {
  const problems = draftProblems(d)
  return problems.length > 0 ? problems[0].message : null
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

// mapPlannerMessage binds a refusal from the server ("rule 1: every_ms must
// be between 60000 and 86400000") back to the control that has to change, in
// the units the author types. The server reports the first problem of a rule
// set; null when the message is about the rule set as a whole or unknown.
export function mapPlannerMessage(message: string): CutProblem | null {
  const text = message.trim()
  const shadow = /^rule (\d+) can never match: (.*)$/.exec(text)
  if (shadow) {
    return { id: `${RULE_ID(Number(shadow[1]) - 1)}-card`, message: `Can never match: ${shadow[2]}.` }
  }
  const rule = /^rule (\d+): (.*)$/.exec(text)
  if (rule) return mapPlannerBody(rule[2], RULE_ID(Number(rule[1]) - 1))
  if (text === 'add at least one rule or a default strategy') {
    return { id: 'cr-default-enabled', message: 'Add at least one rule, or enable the default strategy.' }
  }
  const guards = /^guards: (.*)$/.exec(text)
  if (guards) return mapGuardMessage(guards[1])
  const def = /^default: (.*)$/.exec(text)
  if (def) return mapPlannerBody(def[1], 'cr-default')
  if (text.includes('rules must hold at most')) {
    return { id: '', message: `A rule set holds at most ${MAX_CUT_RULES} rules.` }
  }
  return null
}

function mapPlannerBody(body: string, base: string): CutProblem {
  const phase = /^phase (\d+): (.*)$/.exec(body)
  if (phase) {
    const n = Number(phase[1])
    const every = phase[2].startsWith('every_ms')
    return {
      id: `${base}-phase-${n - 1}-${every ? 'every' : 'until'}`,
      message: every
        ? `Phase ${n} cadence must be a number of minutes from ${MIN_CUT_INTERVAL_MINUTES} to ${MAX_CUT_INTERVAL_MINUTES}.`
        : `Phase ${n} needs its own end, ordered against the phases before it.`,
    }
  }
  if (body.startsWith('every_ms')) {
    return {
      id: `${base}-every`,
      message: `The interval must be a number of minutes from ${MIN_CUT_INTERVAL_MINUTES} to ${MAX_CUT_INTERVAL_MINUTES}.`,
    }
  }
  if (body.startsWith('percentages')) {
    return {
      id: `${base}-percentages`,
      message: `Enter percentages above 0 and below 100, in ascending order, at most ${MAX_CUT_PERCENTAGES} of them.`,
    }
  }
  if (body.startsWith('offsets_ms')) {
    return {
      id: `${base}-offsets`,
      message: `Enter ascending cut times above 0 minutes, at most ${MAX_CUT_OFFSETS} of them.`,
    }
  }
  if (body.startsWith('roles')) return { id: `${base}-roles`, message: 'Pick at least one chapter position.' }
  if (body.startsWith('kinds')) return { id: `${base}-type`, message: 'Pick the item kinds this rule applies to.' }
  if (body.startsWith('min_duration_ms') || body.startsWith('max_duration_ms') || body.startsWith('duration bounds')) {
    return {
      id: `${base}-min`,
      message: 'The minimum duration must be a positive number of minutes below the maximum, or empty.',
    }
  }
  if (body.startsWith('snap_tolerance_ms')) {
    return { id: `${base}-snap`, message: `The snap distance must be from 0 to ${MAX_CUT_SNAP_MINUTES} minutes, or empty.` }
  }
  if (body.startsWith('name')) return { id: `${base}-name`, message: `The rule name must be at most ${MAX_CUT_RULE_NAME} characters.` }
  if (body.startsWith('guards:')) return mapGuardMessage(body.slice('guards:'.length).trim())
  if (body.startsWith('fallback:')) return mapPlannerBody(body.slice('fallback:'.length).trim(), `${base}-fallback`)
  if (body.includes('does not apply to') || body.startsWith('strategy type')) {
    return { id: `${base}-type`, message: 'Choose the cut type and fill in only its own fields.' }
  }
  return { id: `${base}-type`, message: 'This rule cannot be saved as entered; check its cut settings.' }
}

function mapGuardMessage(body: string): CutProblem {
  if (body.startsWith('max_breaks')) {
    return {
      id: 'cr-guard-max',
      message: `The maximum number of breaks must be a whole number from 0 to ${MAX_CUT_BREAKS}.`,
    }
  }
  if (body.startsWith('min_segment_ms')) {
    return {
      id: 'cr-guard-segment',
      message: `The shortest segment must be a number of minutes from 0 to ${MAX_CUT_GUARD_MINUTES}, or empty.`,
    }
  }
  if (body.startsWith('min_from_start_ms')) {
    return {
      id: 'cr-guard-start',
      message: `The distance from the start must be a number of minutes from 0 to ${MAX_CUT_GUARD_MINUTES}, or empty.`,
    }
  }
  if (body.startsWith('min_from_end_ms')) {
    return {
      id: 'cr-guard-end',
      message: `The distance from the end must be a number of minutes from 0 to ${MAX_CUT_GUARD_MINUTES}, or empty.`,
    }
  }
  return { id: '', message: 'The guard rails cannot be saved as entered.' }
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
  // Optional end of the intro media segment in minutes, e.g. "4".
  intro_end_minutes: string
}

export function emptySample(kind: CutItemKind, minutes: string, chapters = '', introEnd = ''): SampleDraft {
  return { key: newKey('sample'), kind, duration_minutes: minutes, chapter_minutes: chapters, intro_end_minutes: introEnd }
}

export function defaultSamples(): SampleDraft[] {
  return [emptySample('movie', '130'), emptySample('episode', '45')]
}

// sampleToPreview builds the body of POST /cut-rule-sets/preview.
export function sampleToPreview(config: CutConfigInput, s: SampleDraft): Parsed<CutPreviewInput> {
  const minutes = parseNumber(s.duration_minutes)
  if (minutes === null || Number.isNaN(minutes) || minutes <= 0) return fail('Enter the sample duration in minutes.')
  if (minutes > MAX_SAMPLE_MINUTES) {
    return fail(`The sample duration must be at most ${MAX_SAMPLE_MINUTES} minutes (7 days).`)
  }
  const body: CutPreviewInput = { ...config, kind: s.kind, duration_ms: msFromMinutes(minutes) }
  if (s.chapter_minutes.trim() !== '') {
    const list = parseList(s.chapter_minutes)
    if (list === null) return fail('Enter the chapter starts as minutes, e.g. 0, 5, 22.')
    const chapters: CutChapter[] = list.map((m, i) => ({ name: `Chapter ${i + 1}`, start_ms: msFromMinutes(m) }))
    body.chapters = chapters
  }
  if ((s.intro_end_minutes ?? '').trim() !== '') {
    const end = parseNumber(s.intro_end_minutes)
    if (end === null || Number.isNaN(end) || end <= 0) return fail('Enter the intro end as minutes, e.g. 4.')
    body.segments = [{ type: 'intro', start_ms: 0, end_ms: msFromMinutes(end) }]
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
