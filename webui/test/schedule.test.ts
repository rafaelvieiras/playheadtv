import assert from 'node:assert/strict'
import { test } from 'node:test'

import { formatWeekdays, groupItemsByKind, legacyDailyDynamicBlock, summarizeRecurrence } from '../src/lib/schedule.ts'

const item = (id: string, days: number[], time: string | null = '22:00', block = 'b1') =>
  ({
    id,
    tenant_id: 't',
    schedule_id: 's',
    block_id: block,
    recurrence_type: 'weekly' as const,
    days_of_week: days,
    start_time_local: time,
    start_at: null,
    active: true,
    created_at: 0,
    updated_at: 0,
  })

test('compact weekday labels', () => {
  assert.equal(formatWeekdays([5, 4]), 'Thu, Fri')
  assert.equal(formatWeekdays([0, 1, 2, 3, 4, 5, 6]), 'Every day')
  assert.equal(formatWeekdays([1, 2, 3, 4, 5]), 'Weekdays')
  assert.equal(formatWeekdays([0, 6]), 'Weekend')
  assert.equal(formatWeekdays([4]), 'Every Thursday')
  assert.equal(formatWeekdays([0, 1]), 'Mon, Sun')
})

test('weekly summary is the label plus the time', () => {
  assert.equal(summarizeRecurrence(item('a', [4, 5])), 'Thu, Fri at 22:00')
  assert.equal(summarizeRecurrence(item('a', [0, 1, 2, 3, 4, 5, 6], '18:00')), 'Every day at 18:00')
})

test('weekly items sort by their first weekday', () => {
  const groups = groupItemsByKind([item('thu', [4]), item('sun-sat', [6, 0]), item('mon', [1, 2])])
  assert.deepEqual(groups.weekly.map((i) => i.id), ['sun-sat', 'mon', 'thu'])
})

const dynamicBlocks = new Map([['b1', { id: 'b1', dynamic: true }]]) as never

test('legacy daily rotation is still detected: seven one-day items', () => {
  const items = [0, 1, 2, 3, 4, 5, 6].map((d) => item(`i${d}`, [d], '00:00'))
  assert.equal(legacyDailyDynamicBlock(items, dynamicBlocks), 'b1')
})

test('legacy daily rotation is detected for one seven-day item and mixed shapes', () => {
  assert.equal(legacyDailyDynamicBlock([item('all', [0, 1, 2, 3, 4, 5, 6], '00:00')], dynamicBlocks), 'b1')
  assert.equal(
    legacyDailyDynamicBlock([item('wk', [1, 2, 3, 4, 5], '00:00'), item('we', [6, 0], '00:00')], dynamicBlocks),
    'b1',
  )
})

test('legacy daily rotation is not detected when days overlap, are missing or times differ', () => {
  assert.equal(legacyDailyDynamicBlock([item('a', [0, 1, 2, 3, 4, 5], '00:00')], dynamicBlocks), undefined)
  assert.equal(
    legacyDailyDynamicBlock([item('a', [0, 1, 2, 3, 4, 5, 6], '00:00'), item('b', [1], '00:00')], dynamicBlocks),
    undefined,
  )
  assert.equal(legacyDailyDynamicBlock([item('a', [0, 1, 2, 3, 4, 5, 6], '01:00')], dynamicBlocks), undefined)
  assert.equal(
    legacyDailyDynamicBlock([item('a', [1, 2, 3, 4, 5], '00:00', 'b1'), item('b', [6, 0], '00:00', 'b2')], dynamicBlocks),
    undefined,
  )
})
