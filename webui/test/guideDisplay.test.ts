import assert from 'node:assert/strict'
import { test } from 'node:test'

import { buildGuideCells } from '../src/lib/guideDisplay.ts'

type Kind = 'program' | 'preroll' | 'midroll' | 'postroll' | 'tail' | 'fallback'
// Times are in seconds to keep the cases readable.
const e = (id: string, kind: Kind, start: number, end: number) => ({ id, kind, start_at: start * 1000, end_at: end * 1000 })
const spans = (cells: ReturnType<typeof buildGuideCells<ReturnType<typeof e>>>) =>
  cells.map((c) => [c.entry.id, c.displayStart / 1000, c.displayEnd / 1000])

test('programs only: nothing changes', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('b', 'program', 100, 200)])
  assert.deepEqual(spans(cells), [['a', 0, 100], ['b', 100, 200]])
})

test('preroll extends the next program backwards', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('pre', 'preroll', 100, 110), e('b', 'program', 110, 200)])
  assert.deepEqual(spans(cells), [['a', 0, 100], ['b', 100, 200]])
  assert.deepEqual(cells[1].absorbed.map((x) => x.id), ['pre'])
})

test('postroll, midroll and tail extend the previous program forwards', () => {
  const cells = buildGuideCells([
    e('a', 'program', 0, 100),
    e('mid', 'midroll', 100, 110),
    e('b', 'program', 110, 200),
    e('post', 'postroll', 200, 210),
    e('t1', 'tail', 210, 220),
    e('t2', 'tail', 220, 230),
  ])
  assert.deepEqual(spans(cells), [['a', 0, 110], ['b', 110, 230]])
  assert.deepEqual(cells[1].absorbed.map((x) => x.id), ['post', 't1', 't2'])
})

test('postroll then preroll between programs split at the boundary', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('post', 'postroll', 100, 110), e('pre', 'preroll', 110, 120), e('b', 'program', 120, 200)])
  assert.deepEqual(spans(cells), [['a', 0, 110], ['b', 110, 200]])
})

test('tail with no previous program falls back to the next one', () => {
  const cells = buildGuideCells([e('t', 'tail', 0, 10), e('a', 'program', 10, 100)])
  assert.deepEqual(spans(cells), [['a', 0, 100]])
})

test('preroll with no next program falls back to the previous one', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('pre', 'preroll', 100, 110)])
  assert.deepEqual(spans(cells), [['a', 0, 110]])
})

test('hidden entries with no program at all leave nothing to show', () => {
  assert.deepEqual(buildGuideCells([e('t', 'tail', 0, 10)]), [])
})

test('a fallback is its own cell and absorbs nothing', () => {
  const cells = buildGuideCells([e('f', 'fallback', 0, 50), e('t', 'tail', 50, 60), e('a', 'program', 60, 100)])
  // The fallback is in the way of the tail's previous side, so it goes to the next program.
  assert.deepEqual(spans(cells), [['f', 0, 50], ['a', 50, 100]])
})

test('a real gap next to a filler is not painted over', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('t', 'tail', 500, 510)])
  assert.deepEqual(spans(cells), [['a', 0, 100]])
})

test('now inside a hidden filler belongs to the program it was attributed to', () => {
  const cells = buildGuideCells([e('a', 'program', 0, 100), e('post', 'postroll', 100, 110), e('b', 'program', 110, 200)])
  const now = 105_000
  const live = cells.filter((c) => c.displayStart <= now && now < c.displayEnd)
  assert.deepEqual(live.map((c) => c.entry.id), ['a'])
})

test('input order does not matter and the input is not mutated', () => {
  const input = [e('b', 'program', 110, 200), e('pre', 'preroll', 100, 110), e('a', 'program', 0, 100)]
  const copy = structuredClone(input)
  assert.deepEqual(spans(buildGuideCells(input)), [['a', 0, 100], ['b', 100, 200]])
  assert.deepEqual(input, copy)
})
