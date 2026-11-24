import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  MAX_CUT_BREAKS,
  MAX_CUT_GUARD_MINUTES,
  MAX_CUT_INTERVAL_MINUTES,
  MAX_CUT_OFFSETS,
  MAX_CUT_RULES,
  MAX_CUT_PERCENTAGES,
  MAX_CUT_SNAP_MINUTES,
  MIN_CUT_INTERVAL_MINUTES,
  defaultSamples,
  describeRules,
  draftProblems,
  draftToConfig,
  draftToInput,
  emptyDraft,
  emptyRule,
  emptyStrategy,
  formatDuration,
  mapPlannerMessage,
  minutesText,
  moveItem,
  parseList,
  problemsById,
  sameDraft,
  sampleToPreview,
  setToDraft,
  validateDraft,
  withType,
} from '../src/lib/cutRules.ts'
import type { CutRuleSet } from '../src/lib/types.ts'

const MIN = 60000

const set = (): CutRuleSet => ({
  id: 's1',
  tenant_id: 'default',
  scope_type: 'channel',
  scope_id: 'ch1',
  name: 'Cuts',
  position: 2,
  created_at: 1,
  rules: [
    { match: { kinds: ['movie'], min_duration_ms: 120 * MIN }, strategy: { type: 'phased', phases: [{ every_ms: 40 * MIN, until_before_end_ms: 20 * MIN }, { every_ms: 5 * MIN }] } },
    {
      name: 'chapters',
      match: { kinds: ['episode', 'movie'], has_chapters: true },
      strategy: { type: 'chapter', roles: ['after_intro', 'middle'], fallback: { type: 'percentage', percentages: [33, 66] } },
      snap_tolerance_ms: 5 * MIN,
      guards: { max_breaks: 3 },
    },
    { match: { kinds: ['episode'] }, strategy: { type: 'percentage', percentages: [50] } },
  ],
  default: { type: 'interval', every_ms: 60 * MIN },
  guards: { min_segment_ms: 2 * MIN, max_breaks: 8 },
})

test('a stored set survives draft -> request body unchanged', () => {
  const s = set()
  const input = draftToInput(setToDraft(s))
  assert.deepEqual(input.rules, s.rules)
  assert.deepEqual(input.default, s.default)
  assert.deepEqual(input.guards, s.guards)
  assert.equal(input.scope_id, 'ch1')
  assert.equal(input.position, 2)
})

test('phased bounds from the start round-trip', () => {
  const s = set()
  s.rules = [{ match: {}, strategy: { type: 'phased', phases: [{ every_ms: 10 * MIN, until_ms: 60 * MIN }, { every_ms: 5 * MIN }] } }]
  const input = draftToInput(setToDraft(s))
  assert.deepEqual(input.rules[0].strategy.phases, [{ every_ms: 10 * MIN, until_ms: 60 * MIN }, { every_ms: 5 * MIN }])
})

test('sameDraft ignores keys and unused strategy fields', () => {
  const a = setToDraft(set())
  const b = setToDraft(set())
  assert.equal(sameDraft(a, b), true)
  b.rules[2].strategy.every_minutes = '999' // percentage rule: unused field
  assert.equal(sameDraft(a, b), true)
  b.rules[2].strategy.percentages = '40'
  assert.equal(sameDraft(a, b), false)
})

test('validateDraft names what to fix', () => {
  const d = emptyDraft()
  assert.equal(validateDraft(d), 'Select a channel.')
  d.scope_id = 'ch'
  assert.equal(validateDraft(d), null)
  d.rules[0].strategy = emptyStrategy('interval')
  d.rules[0].strategy.every_minutes = 'abc'
  assert.match(validateDraft(d) ?? '', /The interval/)
  d.rules[0].strategy = emptyStrategy('percentage')
  d.rules[0].strategy.percentages = '50, x'
  assert.match(validateDraft(d) ?? '', /percentages/)
  d.rules[0].strategy.percentages = '50'
  d.rules[0].min_minutes = '-3'
  assert.match(validateDraft(d) ?? '', /minimum duration/)
  d.rules[0].min_minutes = ''
  d.guards.max_breaks = '2.5'
  assert.match(validateDraft(d) ?? '', /whole number/)
  d.guards.max_breaks = ''
  d.rules = []
  assert.match(validateDraft(d) ?? '', /at least one rule/)
  d.default_enabled = true
  assert.equal(validateDraft(d), null)
})

test('phased phases: the last phase takes no end, the others need one', () => {
  const d = emptyDraft()
  d.scope_id = 'ch'
  d.rules[0].strategy = emptyStrategy('phased')
  d.rules[0].strategy.phases[0].until_minutes = ''
  assert.match(validateDraft(d) ?? '', /Phase 1 end/)
  d.rules[0].strategy.phases[0].until_minutes = '20'
  assert.equal(validateDraft(d), null)
  const cfg = draftToConfig(d)
  assert.ok(cfg.ok)
  if (cfg.ok) assert.deepEqual(cfg.value.rules[0].strategy.phases, [{ every_ms: 40 * MIN, until_before_end_ms: 20 * MIN }, { every_ms: 5 * MIN }])
})

test('switching a strategy to chapter gives it a percentage fallback', () => {
  const d = withType(emptyStrategy('interval'), 'chapter')
  assert.equal(d.fallback_enabled, true)
  assert.equal(d.fallback?.type, 'percentage')
  const r = emptyRule()
  r.strategy = d
  const dr = emptyDraft()
  dr.scope_id = 'x'
  dr.rules = [r]
  const cfg = draftToConfig(dr)
  assert.ok(cfg.ok)
  if (cfg.ok) assert.deepEqual(cfg.value.rules[0].strategy, { type: 'chapter', roles: ['after_intro', 'middle'], fallback: { type: 'percentage', percentages: [50] } })
})

test('describeRules summarises each rule and the default', () => {
  assert.deepEqual(describeRules(set()), [
    'movies, at least 2h: every 40m until the last 20m, then every 5m',
    'episodes or movies, with chapters: chapters: after the intro and middle chapter, else at 33%, 66%',
    'episodes: at 50%',
    'otherwise: every 1h',
  ])
})

test('sample preview body', () => {
  const cfg = draftToConfig({ ...emptyDraft(), scope_id: 'c' })
  assert.ok(cfg.ok)
  if (!cfg.ok) return
  const [movie] = defaultSamples()
  const body = sampleToPreview(cfg.value, { ...movie, chapter_minutes: '0, 6, 61' })
  assert.ok(body.ok)
  if (body.ok) {
    assert.equal(body.value.kind, 'movie')
    assert.equal(body.value.duration_ms, 130 * MIN)
    assert.deepEqual(body.value.chapters?.map((c) => c.start_ms), [0, 6 * MIN, 61 * MIN])
  }
  const withIntro = sampleToPreview(cfg.value, { ...movie, intro_end_minutes: '4' })
  assert.ok(withIntro.ok)
  if (withIntro.ok) assert.deepEqual(withIntro.value.segments, [{ type: 'intro', start_ms: 0, end_ms: 4 * MIN }])
  const noIntro = sampleToPreview(cfg.value, { ...movie, intro_end_minutes: '' })
  assert.ok(noIntro.ok)
  if (noIntro.ok) assert.equal(noIntro.value.segments, undefined)
  assert.equal(sampleToPreview(cfg.value, { ...movie, intro_end_minutes: 'x' }).ok, false)
  assert.equal(sampleToPreview(cfg.value, { ...movie, duration_minutes: '' }).ok, false)
  assert.equal(sampleToPreview(cfg.value, { ...movie, chapter_minutes: '1, b' }).ok, false)
})

test('small helpers', () => {
  assert.equal(formatDuration(0), '0s')
  assert.equal(formatDuration(80 * MIN), '1h20m')
  assert.equal(formatDuration(2376000), '39m36s')
  assert.equal(minutesText(39.6 * MIN), '39.6')
  assert.deepEqual(parseList('33, 66  80;90'), [33, 66, 80, 90])
  assert.equal(parseList('1, a'), null)
  assert.deepEqual(parseList(''), [])
  assert.deepEqual(moveItem([1, 2, 3], 0, 1), [2, 1, 3])
  assert.deepEqual(moveItem([1, 2, 3], 0, -1), [1, 2, 3])
})

test('the constants are the server limits in the units the editor uses', () => {
  // domain.MinCutIntervalMs / MaxCutIntervalMs and cutplan's day-long guard.
  assert.equal(MIN_CUT_INTERVAL_MINUTES * MIN, 60000)
  assert.equal(MAX_CUT_INTERVAL_MINUTES * MIN, 86400000)
  assert.equal(MAX_CUT_GUARD_MINUTES * MIN, 86400000)
  // domain.MaxCutSnapToleranceMs.
  assert.equal(MAX_CUT_SNAP_MINUTES * MIN, 1800000)
  // domain.MaxCutRules, MaxCutPercentages, MaxCutOffsets, MaxCutBreaksLimit.
  assert.equal(MAX_CUT_RULES, 20)
  assert.equal(MAX_CUT_BREAKS, 50)
})

test('the limits the server enforces are mirrored, in minutes and per field', () => {
  const d = emptyDraft()
  d.scope_id = 'ch'
  d.rules[0].strategy = emptyStrategy('interval')
  for (const bad of ['0', '0.5', '1441', '-3', 'x']) {
    d.rules[0].strategy.every_minutes = bad
    const problems = draftProblems(d)
    assert.deepEqual(problems.map((p) => p.id), ['cr-rule-0-every'], bad)
    assert.doesNotMatch(problems[0].message, /86400000|60000/, bad)
    assert.match(problems[0].message, new RegExp(`${MIN_CUT_INTERVAL_MINUTES} to ${MAX_CUT_INTERVAL_MINUTES}`))
  }
  d.rules[0].strategy.every_minutes = '60'
  assert.deepEqual(draftProblems(d), [])

  d.rules[0].strategy = emptyStrategy('percentage')
  for (const bad of ['0', '100', '50, 50', '66, 33', '1,2,3,4,5,6,7,8,9,10,11']) {
    d.rules[0].strategy.percentages = bad
    assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-rule-0-percentages'], bad)
  }
  d.rules[0].strategy.percentages = '33, 66'
  assert.deepEqual(draftProblems(d), [])
  assert.equal(MAX_CUT_PERCENTAGES, 10)

  d.rules[0].strategy = emptyStrategy('offsets')
  for (const bad of ['0', '50, 20', '20, 20']) {
    d.rules[0].strategy.offsets_minutes = bad
    assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-rule-0-offsets'], bad)
  }
  d.rules[0].strategy.offsets_minutes = Array.from({ length: MAX_CUT_OFFSETS + 1 }, (_, i) => String(i + 1)).join(', ')
  assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-rule-0-offsets'])
  d.rules[0].strategy.offsets_minutes = '20, 50'
  assert.deepEqual(draftProblems(d), [])

  d.rules[0].strategy = emptyStrategy('percentage')
  d.rules[0].snap_minutes = String(MAX_CUT_SNAP_MINUTES + 1)
  assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-rule-0-snap'])
  d.rules[0].snap_minutes = String(MAX_CUT_SNAP_MINUTES)
  assert.deepEqual(draftProblems(d), [])

  d.guards.min_segment_minutes = '1441'
  d.guards.max_breaks = '51'
  assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-guard-segment', 'cr-guard-max'])
})

test('one submit reports every problem, each under its own field', () => {
  const d = emptyDraft()
  d.rules[0].strategy = emptyStrategy('interval')
  d.rules[0].strategy.every_minutes = '0'
  d.rules[0].min_minutes = '90'
  d.rules[0].max_minutes = '30'
  d.rules[1] = { ...emptyRule(), strategy: emptyStrategy('offsets') }
  d.rules[1].strategy.offsets_minutes = ''
  const errors = problemsById(draftProblems(d))
  assert.equal(errors['cr-scope-target'], 'Select a channel.')
  assert.match(errors['cr-rule-0-every'], /1 to 1440/)
  assert.match(errors['cr-rule-0-min'], /below the maximum/)
  assert.match(errors['cr-rule-1-offsets'], /cut times in minutes/)
  assert.equal(validateDraft(d), 'Select a channel.')
})

test('a rule shadowed by an earlier one is marked on the rule itself', () => {
  const d = emptyDraft()
  d.scope_id = 'ch'
  d.rules[0].strategy = emptyStrategy('percentage')
  d.rules[1] = emptyRule()
  assert.deepEqual(draftProblems(d).map((p) => p.id), ['cr-rule-1-card'])
  assert.match(draftProblems(d)[0].message, /rule 1 already matches/)
  d.rules[0].kinds = ['episode']
  d.rules[1].kinds = ['movie']
  assert.deepEqual(draftProblems(d), [])
})

test('a planner message goes back to its field in minutes', () => {
  const every = mapPlannerMessage('rule 1: every_ms must be between 60000 and 86400000')
  assert.equal(every?.id, 'cr-rule-0-every')
  assert.doesNotMatch(every?.message ?? '', /60000|86400000/)
  assert.match(every?.message ?? '', /1 to 1440/)
  assert.equal(mapPlannerMessage('rule 2: percentages must be strictly ascending')?.id, 'cr-rule-1-percentages')
  assert.equal(mapPlannerMessage('rule 3: phase 2: every_ms must be between 60000 and 86400000')?.id, 'cr-rule-2-phase-1-every')
  assert.equal(mapPlannerMessage('guards: max_breaks must be between 0 and 50')?.id, 'cr-guard-max')
  assert.equal(mapPlannerMessage('default: every_ms must be between 60000 and 86400000')?.id, 'cr-default-every')
  assert.equal(mapPlannerMessage('rule 2 can never match: rule 1 already matches every item it would')?.id, 'cr-rule-1-card')
  assert.equal(mapPlannerMessage('add at least one rule or a default strategy')?.id, 'cr-default-enabled')
  assert.equal(mapPlannerMessage('something else entirely'), null)
  for (const message of [
    'rule 1: every_ms must be between 60000 and 86400000',
    'guards: min_segment_ms must be between 0 and 86400000',
  ]) {
    assert.doesNotMatch(mapPlannerMessage(message)?.message ?? '', /\d{5,}/, message)
  }
})

test('a sample longer than a week is refused before the request', () => {
  const cfg = draftToConfig({ ...emptyDraft(), scope_id: 'c' })
  assert.ok(cfg.ok)
  if (!cfg.ok) return
  const [movie] = defaultSamples()
  assert.equal(sampleToPreview(cfg.value, { ...movie, duration_minutes: '10081' }).ok, false)
  assert.equal(sampleToPreview(cfg.value, { ...movie, duration_minutes: '10080' }).ok, true)
})
