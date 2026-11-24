import type { PluginConfigField } from '@/lib/types'

// Small pure helpers behind the Plugins page. Kept out of the component so the
// ordering rule — the one thing here that can silently pick the wrong release —
// is unit-tested.

/**
 * Orders plugin versions the way the rest of the project does
 * (`client/lib/plugin/plugin_manifest.dart`, and the plugin's own releases):
 * digit runs compare as numbers, everything else as text, so `2026.10.26` sorts
 * above `2026.9.30` and `v2026.11.5` above `v2026.11.31`.
 */
export function comparePluginVersions(a: string, b: string): number {
  const left = versionParts(a)
  const right = versionParts(b)
  for (let i = 0; i < Math.max(left.length, right.length); i++) {
    const l = i < left.length ? left[i] : 0
    const r = i < right.length ? right[i] : 0
    if (l === r) continue
    return l < r ? -1 : 1
  }
  return 0
}

function versionParts(version: string): number[] {
  return version.split(/[^0-9]+/).filter((part) => part !== '').map((part) => Number(part))
}

/** The newest version in the list, or undefined for an empty one. */
export function latestPluginVersion(versions: string[]): string | undefined {
  return versions.reduce<string | undefined>(
    (best, version) => (best === undefined || comparePluginVersions(version, best) > 0 ? version : best),
    undefined,
  )
}

/** Versions newest first, for a picker that offers the best one first. */
export function versionsNewestFirst(versions: string[]): string[] {
  return [...versions].sort((a, b) => comparePluginVersions(b, a))
}

/** One setting as the row summary shows it: a choice by its label, a bool as On/Off. */
export function formatPluginSetting(field: PluginConfigField, value: unknown): string {
  if (value === undefined || value === null) return 'not set'
  if (field.type === 'bool') return value === true ? 'On' : 'Off'
  if (field.type === 'choice') {
    const option = (field.options ?? []).find((entry) => entry.value === value)
    return option ? option.label : String(value)
  }
  return String(value)
}

/**
 * What a source is currently configured with, one line — so the admin page says
 * what a source is set to without opening every dialog (the playback client's
 * Settings shows the same line).
 */
export function describePluginSettings(fields: PluginConfigField[], settings: Record<string, unknown>): string {
  if (fields.length === 0) return ''
  return fields.map((field) => `${field.label} ${formatPluginSetting(field, settings[field.key])}`).join(' · ')
}
