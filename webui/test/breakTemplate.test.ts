import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  MAX_BREAK_NAME,
  MAX_BREAK_STEPS,
  MAX_BREAK_TARGET_MINUTES,
  draftProblems,
  draftToInput,
  mapTemplateMessage,
  describeSteps,
  emptyDraft,
  emptyStep,
  moveStep,
  sameDraft,
  summarizeDraft,
  templateToDraft,
  validateDraft,
} from '../src/lib/breakTemplate.ts'

const valid = () => {
  const d = emptyDraft()
  d.scope_id = 'chan-1'
  d.steps = [
    { ...emptyStep('single'), filler_pool_id: 'a' },
    { ...emptyStep('fill'), filler_pool_id: 'b', target_minutes: 4, max_fit_attempts: 3 },
    { ...emptyStep('single'), filler_pool_id: 'c' },
  ]
  return d
}

test('a valid draft passes and serializes minutes as ms, leaving fill-only fields off single steps', () => {
  const d = valid()
  assert.equal(validateDraft(d), null)
  const input = draftToInput(d)
  assert.deepEqual(input.steps, [
    { step_type: 'single', filler_pool_id: 'a', selection_strategy: 'sequential' },
    { step_type: 'fill', filler_pool_id: 'b', selection_strategy: 'sequential', target_ms: 240000, max_fit_attempts: 3 },
    { step_type: 'single', filler_pool_id: 'c', selection_strategy: 'sequential' },
  ])
})

test('validation mirrors the backend limits', () => {
  const cases: [string, (d: ReturnType<typeof valid>) => void, RegExp][] = [
    ['no scope', (d) => (d.scope_id = ''), /Select a channel/],
    ['no steps', (d) => (d.steps = []), /at least one step/],
    ['too many steps', (d) => (d.steps = Array.from({ length: 11 }, () => ({ ...emptyStep(), filler_pool_id: 'a' }))), /at most 10/],
    ['missing pool', (d) => (d.steps[0].filler_pool_id = ''), /Select a filler pool/],
    ['zero length', (d) => (d.steps[1].target_minutes = 0), /more than 0 and at most 60/],
    ['over an hour', (d) => (d.steps[1].target_minutes = 61), /more than 0 and at most 60/],
    ['NaN length', (d) => (d.steps[1].target_minutes = NaN), /more than 0 and at most 60/],
    ['attempts 0', (d) => (d.steps[1].max_fit_attempts = 0), /Fit attempts/],
    ['attempts 21', (d) => (d.steps[1].max_fit_attempts = 21), /Fit attempts/],
    ['long name', (d) => (d.name = 'x'.repeat(101)), /at most 100/],
  ]
  for (const [name, mutate, want] of cases) {
    const d = valid()
    mutate(d)
    assert.match(validateDraft(d) ?? '', want, name)
  }
  const edge = valid()
  edge.steps[1].target_minutes = 60
  edge.steps[1].max_fit_attempts = 20
  assert.equal(validateDraft(edge), null)
  edge.steps = Array.from({ length: 10 }, () => ({ ...emptyStep(), filler_pool_id: 'a' }))
  assert.equal(validateDraft(edge), null)
})

test('moving steps swaps neighbours and ignores out-of-range moves', () => {
  assert.deepEqual(moveStep(['a', 'b', 'c'], 1, -1), ['b', 'a', 'c'])
  assert.deepEqual(moveStep(['a', 'b', 'c'], 1, 1), ['a', 'c', 'b'])
  assert.deepEqual(moveStep(['a', 'b', 'c'], 0, -1), ['a', 'b', 'c'])
  assert.deepEqual(moveStep(['a', 'b', 'c'], 2, 1), ['a', 'b', 'c'])
})

test('a loaded template round-trips and is not dirty until edited', () => {
  const t = {
    id: 't', tenant_id: 'x', scope_type: 'block' as const, scope_id: 'b', kind: 'postroll' as const, name: 'N', position: 2, created_at: 1,
    steps: [
      { id: '1', break_template_id: 't', position: 0, step_type: 'single' as const, filler_pool_id: 'a', selection_strategy: 'random' as const, target_ms: 0, max_fit_attempts: 1 },
      { id: '2', break_template_id: 't', position: 1, step_type: 'fill' as const, filler_pool_id: 'b', selection_strategy: 'weighted' as const, target_ms: 90000, max_fit_attempts: 5 },
    ],
  }
  const loaded = templateToDraft(t)
  assert.equal(loaded.steps[1].target_minutes, 1.5)
  assert.ok(sameDraft(loaded, templateToDraft(t)))
  const edited = templateToDraft(t)
  edited.steps[1].target_minutes = 2
  assert.ok(!sameDraft(loaded, edited))
  // Touching the unused fields of a single step is not an edit.
  const noise = templateToDraft(t)
  noise.steps[0].target_minutes = 9
  assert.ok(sameDraft(loaded, noise))
  assert.equal(describeSteps(t), '1 item → 1.5 min fill')
})

test('one submit reports every problem, each under its own field', () => {
  const d = valid()
  d.scope_id = ''
  d.position = NaN
  d.name = 'x'.repeat(101)
  d.steps[1].filler_pool_id = ''
  d.steps[1].target_minutes = 0
  d.steps[1].max_fit_attempts = 0
  const errors = Object.fromEntries(draftProblems(d).map((p) => [p.id, p.message]))
  assert.equal(errors['bt-scope-target'], 'Select a channel.')
  assert.equal(errors['bt-position'], 'Position must be a whole number.')
  assert.match(errors['bt-name'], /at most 100/)
  assert.equal(errors['bt-step-1-pool'], 'Select a filler pool.')
  assert.match(errors['bt-step-1-target'], /more than 0 and at most 60/)
  assert.match(errors['bt-step-1-attempts'], /1 to 20/)
  assert.equal(draftProblems(valid()).length, 0)
  const empty = emptyDraft()
  empty.scope_id = 'c'
  empty.steps = []
  assert.deepEqual(draftProblems(empty).map((p) => p.id), ['bt-steps'])
})

test('the summary counts items and adds only the fill lengths it knows', () => {
  const d = emptyDraft()
  d.steps = [{ ...emptyStep('single'), filler_pool_id: 'a' }]
  assert.equal(summarizeDraft(d), '1 step: 1 item.')
  d.steps = Array.from({ length: 9 }, () => ({ ...emptyStep('fill'), filler_pool_id: 'a', target_minutes: 2 / 9 }))
  d.steps.push({ ...emptyStep('single'), filler_pool_id: 'b' })
  assert.equal(summarizeDraft(d), '10 steps: ~2 min of fills + 1 item.')
  d.steps = []
  assert.equal(summarizeDraft(d), 'No steps yet.')
})

test('the template bounds are the server limits in minutes', () => {
  // domain.MaxBreakSteps, MaxBreakTargetMs (one hour), MaxBreakTemplateName.
  assert.equal(MAX_BREAK_STEPS, 10)
  assert.equal(MAX_BREAK_TARGET_MINUTES * 60000, 3600000)
  assert.equal(MAX_BREAK_NAME, 100)
})

test('a refusal from the server goes back to its step and its field, in minutes', () => {
  const target = mapTemplateMessage('step 2: target_ms must be between 1 and 3600000')
  assert.equal(target?.id, 'bt-step-1-target')
  assert.doesNotMatch(target?.message ?? '', /3600000|target_ms/)
  assert.match(target?.message ?? '', /more than 0 and at most 60/)
  assert.equal(mapTemplateMessage('step 1: max_fit_attempts must be between 1 and 20')?.id, 'bt-step-0-attempts')
  assert.equal(mapTemplateMessage('step 1: filler pool 9f3 does not exist')?.id, 'bt-step-0-pool')
  assert.equal(mapTemplateMessage('step 3: filler_pool_id is required')?.id, 'bt-step-2-pool')
  assert.equal(mapTemplateMessage('steps must hold at least one step')?.id, 'bt-steps')
  assert.equal(mapTemplateMessage('scope_id is required')?.id, 'bt-scope-target')
  assert.equal(mapTemplateMessage('something else entirely'), null)
})
