import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  defaultValue,
  emptyCondition,
  emptyQuery,
  fieldDef,
  opsFor,
  summarizeQuery,
  validateQuery,
  type Query,
} from '../src/lib/query.ts'

// What a collection's saved query has to survive: a field this build knows (so
// an editor can offer a control), a field only the server knows (so it is shown
// and reported by name instead of being rewritten), and a row an operator just
// added (so it asks for a field instead of a value nobody typed).

test('a condition an operator adds has no field yet, and says so', () => {
  const row = emptyCondition()
  assert.equal(row.field, undefined)
  assert.equal(row.op, undefined)
  const q = { version: 1, all: [row] } as Query
  assert.equal(validateQuery(q), 'Query › all #1: choose a field.')
})

test('an unknown field is reported by its own name, never as Kind', () => {
  const q = { version: 1, all: [{ field: 'media_source_id2', op: 'equals', value: 'src-1' }] } as Query
  assert.equal(fieldDef('media_source_id2'), undefined)
  assert.match(validateQuery(q) ?? '', /media_source_id2/)
  assert.doesNotMatch(validateQuery(q) ?? '', /Kind/)
})

test('a query is described with the field it names, known or not', () => {
  assert.equal(summarizeQuery(emptyQuery()), 'kind: Movie')
  assert.equal(
    summarizeQuery({ version: 1, all: [{ field: 'media_source_id', op: 'equals', value: 'src-1' }] }),
    'media source: src-1',
  )
  assert.equal(
    summarizeQuery({ version: 1, all: [{ field: 'unknown_field', op: 'equals', value: 'x' }] }),
    'unknown_field: x',
  )
  assert.equal(summarizeQuery({ version: 1, all: [{}] }), 'a field still to choose')
})

test('the server field that broke the editor has a definition and an operator list', () => {
  const def = fieldDef('media_source_id')
  assert.ok(def)
  assert.deepEqual(opsFor(def.type), ['equals', 'in'])
  assert.equal(defaultValue(def, 'in').length, 0)
})

test('a complete query passes and null means no query at all', () => {
  assert.equal(validateQuery(null), undefined)
  assert.equal(validateQuery(emptyQuery()), undefined)
  const inQuery = {
    version: 1,
    any: [
      { field: 'genre', op: 'in', value: ['Horror', 'Drama'] },
      { not: { field: 'duration', op: 'lt', value: 90 } },
    ],
  } as Query
  assert.equal(validateQuery(inQuery), undefined)
})

test('an incomplete condition is the one problem reported, not a value error', () => {
  const chosenFieldOnly = { version: 1, all: [{ field: 'genre', op: 'equals', value: '' }] } as Query
  assert.equal(validateQuery(chosenFieldOnly), 'Query › all #1 (Genre): enter a value.')
  const range = { version: 1, all: [{ field: 'year', op: 'between', value: ['2000', ''] }] } as Query
  assert.equal(validateQuery(range), 'Query › all #1 (Year): enter both ends of the range.')
})
