import assert from 'node:assert/strict'
import { test } from 'node:test'

import { validateWatermarkFile, validateWatermarkOptions } from '../src/lib/watermark.ts'

test('only PNG, JPEG and WebP up to 1 MiB are accepted', () => {
  assert.equal(validateWatermarkFile({ type: 'image/png', size: 10 }), null)
  assert.equal(validateWatermarkFile({ type: 'image/jpeg', size: 10 }), null)
  assert.equal(validateWatermarkFile({ type: 'image/webp', size: 1 << 20 }), null)
  assert.notEqual(validateWatermarkFile({ type: 'image/svg+xml', size: 10 }), null)
  assert.notEqual(validateWatermarkFile({ type: 'image/gif', size: 10 }), null)
  assert.notEqual(validateWatermarkFile({ type: 'image/png', size: (1 << 20) + 1 }), null)
})

test('opacity is 0-100 and size 2-40', () => {
  assert.equal(validateWatermarkOptions(0, 2), null)
  assert.equal(validateWatermarkOptions(100, 40), null)
  assert.notEqual(validateWatermarkOptions(101, 8), null)
  assert.notEqual(validateWatermarkOptions(-1, 8), null)
  assert.notEqual(validateWatermarkOptions(50, 1), null)
  assert.notEqual(validateWatermarkOptions(50, 41), null)
  assert.notEqual(validateWatermarkOptions(NaN, 8), null)
})
