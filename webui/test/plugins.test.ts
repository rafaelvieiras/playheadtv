import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  comparePluginVersions,
  describePluginSettings,
  latestPluginVersion,
  versionsNewestFirst,
} from '../src/lib/plugins.ts'
import type { PluginConfigField } from '../src/lib/types.ts'

test('digit runs compare as numbers, so 2026.10 is above 2026.9', () => {
  assert.equal(comparePluginVersions('2026.10.26', '2026.9.30'), 1)
  assert.equal(comparePluginVersions('2026.9.30', '2026.10.26'), -1)
})

test('a missing component counts as zero', () => {
  assert.equal(comparePluginVersions('1.2', '1.2.0'), 0)
  assert.equal(comparePluginVersions('1.2.1', '1.2'), 1)
})

test('a leading v does not change the order', () => {
  assert.equal(comparePluginVersions('v2026.11.31', 'v2026.11.5'), 1)
})

test('the latest of a list is the newest, not the last one written', () => {
  assert.equal(latestPluginVersion(['2026.9.30', '2026.10.26', '2026.10.4']), '2026.10.26')
  assert.equal(latestPluginVersion([]), undefined)
})

test('the picker offers the newest version first', () => {
  assert.deepEqual(versionsNewestFirst(['2026.9.30', '2026.10.26', '2026.10.4']), [
    '2026.10.26',
    '2026.10.4',
    '2026.9.30',
  ])
})

test('a source summary names each declared setting the way the plugin labels it', () => {
  const fields: PluginConfigField[] = [
    {
      key: 'max_height',
      label: 'Quality',
      type: 'choice',
      options: [
        { value: 1080, label: '1080p' },
        { value: 720, label: '720p' },
      ],
    },
    { key: 'max_items', label: 'Items', type: 'number' },
    { key: 'audio_only', label: 'Audio only', type: 'bool' },
  ]
  assert.equal(
    describePluginSettings(fields, { max_height: 720, max_items: 42, audio_only: true }),
    'Quality 720p · Items 42 · Audio only On',
  )
})

test('a setting nobody set says so instead of showing a placeholder value', () => {
  const fields: PluginConfigField[] = [{ key: 'url', label: 'Playlist URL', type: 'text' }]
  assert.equal(describePluginSettings(fields, {}), 'Playlist URL not set')
})
