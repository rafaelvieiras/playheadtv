import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  defaultSamples,
  describeRules,
  draftToConfig,
  draftToInput,
  emptyDraft,
  emptyRule,
  emptyStrategy,
  formatDuration,
  minutesText,
  moveItem,
  parseList,
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
  assert.match(validateDraft(d) ?? '', /Rule 1: the interval/)
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
  assert.match(validateDraft(d) ?? '', /phase 1 end/)
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
    'episodes or movies, with chapters: chapters: after the intro chapter and middle chapter, else at 33%, 66%',
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
