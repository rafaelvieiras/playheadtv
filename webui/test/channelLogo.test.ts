import assert from 'node:assert/strict'
import { test } from 'node:test'

import { CHANNEL_LOGO_MAX_BYTES, channelLogoPath, validateChannelLogoFile } from '../src/lib/channelLogo.ts'

test('accepts the three image types the backend decodes', () => {
  for (const type of ['image/png', 'image/jpeg', 'image/webp']) {
    assert.equal(validateChannelLogoFile({ type, size: 1024 }), null)
  }
})

test('rejects anything else, including an SVG', () => {
  assert.match(String(validateChannelLogoFile({ type: 'image/svg+xml', size: 1024 })), /PNG, JPEG or WebP/)
  assert.match(String(validateChannelLogoFile({ type: 'application/pdf', size: 1024 })), /PNG, JPEG or WebP/)
})

test('rejects a file above the 1 MiB cap and accepts one exactly at it', () => {
  assert.equal(validateChannelLogoFile({ type: 'image/png', size: CHANNEL_LOGO_MAX_BYTES }), null)
  assert.match(String(validateChannelLogoFile({ type: 'image/png', size: CHANNEL_LOGO_MAX_BYTES + 1 })), /1 MiB/)
})

test('a channel without a logo has no path to fetch', () => {
  assert.equal(channelLogoPath(null), null)
  assert.equal(channelLogoPath(undefined), null)
  assert.equal(channelLogoPath(''), null)
})

test('the path points at the image route and escapes the id', () => {
  assert.equal(channelLogoPath('abc-123'), '/channel-logos/abc-123/image')
  assert.equal(channelLogoPath('a/b'), '/channel-logos/a%2Fb/image')
})
