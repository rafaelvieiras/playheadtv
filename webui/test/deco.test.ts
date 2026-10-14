import assert from 'node:assert/strict'
import { test } from 'node:test'

import { hasAdvancedRules, simpleFallbackState } from '../src/lib/deco.ts'
import type { FillerRule } from '../src/lib/types.ts'

function rule(over: Partial<FillerRule>): FillerRule {
  return {
    id: 'r',
    tenant_id: 'default',
    scope_type: 'channel',
    scope_id: 'ch1',
    kind: 'fallback',
    filler_pool_id: 'p1',
    selection_strategy: 'sequential',
    position: 0,
    max_fit_attempts: 1,
    created_at: 0,
    ...over,
  }
}

test('a channel with no rules has nothing advanced and no fallback', () => {
  assert.equal(hasAdvancedRules([], 'ch1', []), false)
  assert.equal(simpleFallbackState([], 'ch1').state, 'none')
})

test('a single plain channel fallback is the simple state', () => {
  const rules = [rule({ id: 'a' })]
  assert.equal(hasAdvancedRules(rules, 'ch1', []), false)
  assert.equal(simpleFallbackState(rules, 'ch1').state, 'single')
})

test('rules of other channels and blocks outside the schedule are ignored', () => {
  const rules = [
    rule({ id: 'a', scope_id: 'ch2', kind: 'midroll' }),
    rule({ id: 'b', scope_type: 'block', scope_id: 'b9' }),
  ]
  assert.equal(hasAdvancedRules(rules, 'ch1', ['b1']), false)
})

test('a per-kind channel rule makes the channel advanced', () => {
  for (const kind of ['preroll', 'midroll', 'postroll', 'tail'] as const) {
    assert.equal(hasAdvancedRules([rule({ kind })], 'ch1', []), true)
  }
  assert.equal(hasAdvancedRules([rule({ id: 'a' }), rule({ id: 'b', kind: 'midroll' })], 'ch1', []), true)
})

test('a block rule of a scheduled block makes it advanced', () => {
  const rules = [rule({ scope_type: 'block', scope_id: 'b1', kind: 'preroll' })]
  assert.equal(hasAdvancedRules(rules, 'ch1', ['b1']), true)
})

test('several fallbacks or a non-default fallback are complex', () => {
  const two = [rule({ id: 'a' }), rule({ id: 'b', position: 1 })]
  assert.equal(simpleFallbackState(two, 'ch1').state, 'complex')
  assert.equal(hasAdvancedRules(two, 'ch1', []), true)
  const random = [rule({ selection_strategy: 'random' })]
  assert.equal(simpleFallbackState(random, 'ch1').state, 'complex')
  assert.equal(hasAdvancedRules(random, 'ch1', []), true)
  assert.equal(hasAdvancedRules([rule({ max_fit_attempts: 5 })], 'ch1', []), true)
})
