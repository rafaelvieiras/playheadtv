import assert from 'node:assert/strict'
import { test } from 'node:test'

import { extractResetToken, validateNewPassword } from '../src/lib/passwordReset.ts'

test('the token is read from the fragment, then the query string', () => {
  assert.equal(extractResetToken('', '#token=fake-token-1'), 'fake-token-1')
  assert.equal(extractResetToken('?token=fake-token-2', ''), 'fake-token-2')
  assert.equal(extractResetToken('?token=from-query', '#token=from-hash'), 'from-hash')
  assert.equal(extractResetToken('', ''), '')
  assert.equal(extractResetToken('?other=1', '#x=2'), '')
})

test('a URL-safe token survives parsing unchanged', () => {
  assert.equal(extractResetToken('', '#token=ab-_CD09'), 'ab-_CD09')
})

test('the new password must be long enough and typed twice the same', () => {
  assert.equal(validateNewPassword('short', 'short') !== null, true)
  assert.equal(validateNewPassword('long-enough-1', 'long-enough-2') !== null, true)
  assert.equal(validateNewPassword('long-enough-1', 'long-enough-1'), null)
})
