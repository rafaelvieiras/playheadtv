// Pure logic behind the channel Deco tab: which filler rules the simple
// "Fallback filler" control can stand for, and when the Advanced section
// should start expanded.
import type { Channel, ChannelInput, FillerRule } from '@/lib/types'

export function channelToInput(channel: Channel): ChannelInput {
  return {
    number: channel.number,
    name: channel.name,
    guide_art_url: channel.guide_art_url,
    timezone: channel.timezone,
    schedule_id: channel.schedule_id,
    base_schedule_id: channel.base_schedule_id,
  }
}

// The channel-scoped fallback rules of one channel, in position order.
export function channelFallbackRules(rules: FillerRule[], channelId: string): FillerRule[] {
  return rules
    .filter((r) => r.scope_type === 'channel' && r.scope_id === channelId && r.kind === 'fallback')
    .sort((a, b) => a.position - b.position)
}

// A rule the simple control can show and edit without hiding anything: the
// default selection and fit attempts, so changing only its pool loses nothing.
export function isSimpleFallback(rule: FillerRule): boolean {
  return rule.selection_strategy === 'sequential' && (rule.max_fit_attempts ?? 1) === 1
}

// What the simple Fallback filler control stands for: 'none' (no channel
// fallback), 'single' (one plain rule, editable in place) or 'complex' (more
// than one rule, or one with non-default options: only Advanced shows it
// faithfully).
export function simpleFallbackState(
  rules: FillerRule[],
  channelId: string,
): { state: 'none' | 'single' | 'complex'; rule?: FillerRule } {
  const fallbacks = channelFallbackRules(rules, channelId)
  if (fallbacks.length === 0) return { state: 'none' }
  if (fallbacks.length === 1 && isSimpleFallback(fallbacks[0])) return { state: 'single', rule: fallbacks[0] }
  return { state: 'complex', rule: fallbacks[0] }
}

// True when the channel has filler rules beyond the simple fallback: any
// per-kind rule (preroll/midroll/postroll/tail) on the channel, any rule of a
// block in its schedule, or a fallback that is not the plain single one.
export function hasAdvancedRules(rules: FillerRule[], channelId: string, scheduleBlockIds: Iterable<string>): boolean {
  const blockIds = new Set(scheduleBlockIds)
  const applicable = rules.filter((r) =>
    r.scope_type === 'channel' ? r.scope_id === channelId : blockIds.has(r.scope_id),
  )
  if (applicable.length === 0) return false
  const simple = simpleFallbackState(rules, channelId)
  if (simple.state === 'complex') return true
  return applicable.some((r) => r !== simple.rule)
}
