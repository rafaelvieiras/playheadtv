import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { ChannelFillerRules } from '@/components/ChannelFillerRules'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { ChannelLogoSection } from '@/components/ChannelLogoSection'
import { SaveBar } from '@/components/SaveBar'
import { WatermarkSection } from '@/components/WatermarkSection'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { channelToInput, hasAdvancedRules, simpleFallbackState } from '@/lib/deco'
import { ApiError, api } from '@/lib/http'
import type { Channel, FillerPool, FillerRule, FillerRuleInput, Schedule, ScheduleItem } from '@/lib/types'

// NONE_VALUE stands in for "no value": Radix Select can't use an empty string
// as an item value.
const NONE_VALUE = '__none__'

// The channel's decorative behavior in one place: a short list of common
// controls (base schedule, watermark, fallback filler) and a collapsed
// Advanced section with the per-kind filler rules. Each control saves with its
// own request; there is no new resource behind it.
export function ChannelDeco({ channel }: { channel: Channel }) {
  const queryClient = useQueryClient()

  const { data: rules } = useQuery({
    queryKey: ['filler-rules'],
    queryFn: () => api.get<FillerRule[]>('/filler-rules'),
  })
  const { data: pools } = useQuery({
    queryKey: ['filler-pools'],
    queryFn: () => api.get<FillerPool[]>('/filler-pools'),
  })
  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
  })
  const { data: scheduleItems } = useQuery({
    queryKey: ['schedules', channel.schedule_id, 'items'],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${channel.schedule_id}/items`),
    enabled: !!channel.schedule_id,
  })

  const sortedSchedules = useMemo(
    () => [...(schedules ?? [])].sort((a, b) => a.name.localeCompare(b.name)),
    [schedules],
  )
  const sortedPools = useMemo(() => [...(pools ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [pools])

  // Base schedule: part of the channel record, saved with its own button.
  const [baseDraft, setBaseDraft] = useState<string | null>(channel.base_schedule_id)
  const [baseError, setBaseError] = useState<string | null>(null)
  const baseDirty = baseDraft !== channel.base_schedule_id
  const saveBase = useMutation({
    mutationFn: () =>
      api.put<Channel>(`/channels/${channel.id}`, { ...channelToInput(channel), base_schedule_id: baseDraft }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Base schedule saved.')
      setBaseError(null)
    },
    onError: (err) =>
      setBaseError(
        err instanceof ApiError ? `Could not save the base schedule (${err.message})` : 'Could not save the base schedule.',
      ),
  })

  // Fallback filler: one channel-scoped `fallback` rule, created, repointed or
  // deleted as the pool choice changes.
  const fallback = rules ? simpleFallbackState(rules, channel.id) : undefined
  const invalidateRules = () => queryClient.invalidateQueries({ queryKey: ['filler-rules'] })
  const setFallback = useMutation({
    mutationFn: async (poolId: string | null) => {
      if (!fallback) return
      if (poolId === null) {
        if (fallback.state === 'single' && fallback.rule) await api.delete(`/filler-rules/${fallback.rule.id}`)
        return
      }
      if (fallback.state === 'single' && fallback.rule) {
        const r = fallback.rule
        const body: FillerRuleInput = {
          scope_type: r.scope_type,
          scope_id: r.scope_id,
          kind: r.kind,
          filler_pool_id: poolId,
          selection_strategy: r.selection_strategy,
          position: r.position,
          max_fit_attempts: r.max_fit_attempts ?? 1,
        }
        await api.put<FillerRule>(`/filler-rules/${r.id}`, body)
      } else {
        const body: FillerRuleInput = {
          scope_type: 'channel',
          scope_id: channel.id,
          kind: 'fallback',
          filler_pool_id: poolId,
          selection_strategy: 'sequential',
          position: 0,
          max_fit_attempts: 1,
        }
        await api.post<FillerRule>('/filler-rules', body)
      }
    },
    onSuccess: () => {
      invalidateRules()
      toast.success('Fallback filler saved.')
    },
    onError: (err) =>
      toast.error(
        err instanceof ApiError ? `Could not save the fallback filler (${err.message})` : 'Could not save the fallback filler.',
      ),
  })

  const advancedRules =
    rules !== undefined &&
    hasAdvancedRules(
      rules,
      channel.id,
      (scheduleItems ?? []).map((i) => i.block_id ?? ''),
    )
  // Choosing "None" deletes the rule on the server, so it asks first.
  const [clearingFallback, setClearingFallback] = useState(false)
  const [advancedOverride, setAdvancedOverride] = useState<boolean | null>(null)
  const advancedOpen = advancedOverride ?? advancedRules

  return (
    <div className="flex flex-col gap-8" data-testid="channel-deco">
      <p className="text-sm text-muted-foreground" data-testid="deco-defaults">
        Nothing here is required. By default a channel has no watermark, no base schedule and no fallback filler: when
        nothing is scheduled the player shows its plain gap slate.
      </p>

      <section className="flex flex-col gap-3" data-testid="deco-base-schedule">
        <Field>
          <FieldLabel htmlFor="deco-base-schedule-select">Base schedule</FieldLabel>
          <Select value={baseDraft ?? NONE_VALUE} onValueChange={(v) => setBaseDraft(v === NONE_VALUE ? null : v)}>
            <SelectTrigger id="deco-base-schedule-select" className="w-full">
              <SelectValue placeholder="No base schedule" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
              {sortedSchedules
                .filter((s) => s.id !== channel.schedule_id)
                .map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
          <FieldDescription data-testid="base-schedule-help">
            Plays in every gap the channel's schedule leaves, with its own breaks, instead of filler clips.
          </FieldDescription>
          {baseDraft && (
            <FieldDescription data-testid="base-schedule-continuous-note">
              With a base schedule set, a continuous item in the schedule no longer fills the gaps; the base schedule
              does.
            </FieldDescription>
          )}
        </Field>
        <SaveBar
          dirty={baseDirty}
          saving={saveBase.isPending}
          onSave={() => saveBase.mutate()}
          onDiscard={() => {
            setBaseDraft(channel.base_schedule_id)
            setBaseError(null)
          }}
          error={baseError}
        />
      </section>

      <ChannelLogoSection channel={channel} />

      <WatermarkSection ownerType="channel" ownerId={channel.id} />

      <section className="flex flex-col gap-3" data-testid="deco-fallback">
        <Field>
          <FieldLabel htmlFor="deco-fallback-select">Fallback filler</FieldLabel>
          {fallback?.state === 'complex' ? (
            <p className="text-sm" data-testid="deco-fallback-complex">
              This channel has several fallback rules or custom options. Edit them under Advanced.
            </p>
          ) : (
            <Select
              value={fallback?.rule?.filler_pool_id ?? NONE_VALUE}
              disabled={!fallback || setFallback.isPending}
              onValueChange={(v) => {
                if (v === NONE_VALUE && fallback?.rule) {
                  setClearingFallback(true)
                  return
                }
                setFallback.mutate(v === NONE_VALUE ? null : v)
              }}
            >
              <SelectTrigger id="deco-fallback-select" className="w-full">
                <SelectValue placeholder="No fallback filler" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE_VALUE}>None</SelectItem>
                {sortedPools.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <FieldDescription data-testid="deco-fallback-help">
            What plays when nothing is scheduled. Without one, that time stays empty.
          </FieldDescription>
        </Field>

        <ConfirmDialog
          open={clearingFallback}
          onClose={() => setClearingFallback(false)}
          title="Remove the fallback filler of this channel?"
          description="The rule is deleted, not just unassigned. Time the schedule does not cover stays empty — the player shows its plain gap slate."
          confirmLabel="Remove fallback"
          pendingLabel="Removing…"
          onConfirm={() =>
            setFallback.mutate(null, { onSuccess: () => setClearingFallback(false) })
          }
          isPending={setFallback.isPending}
          error={setFallback.error}
          testId="deco-fallback-confirm"
        />
      </section>

      <p className="text-sm text-muted-foreground" data-testid="deco-block-note">
        Individual blocks can override the watermark and have their own filler rules; set those in the{' '}
        <Link to="/blocks" className="underline">
          block editor
        </Link>
        .
      </p>

      <section className="flex flex-col gap-3" data-testid="deco-advanced">
        <Button
          type="button"
          variant="outline"
          className="self-start"
          data-testid="deco-advanced-toggle"
          aria-expanded={advancedOpen}
          onClick={() => setAdvancedOverride(!advancedOpen)}
        >
          {advancedOpen ? 'Hide advanced' : 'Show advanced'}
        </Button>
        {advancedOpen && (
          <div className="flex flex-col gap-3" data-testid="deco-advanced-body">
            <p className="text-sm text-muted-foreground">
              Filler rules by kind (PreRoll, MidRoll, PostRoll, Tail, Fallback), with their scope, selection strategy,
              position and several pools. The pool library is shared by every channel: manage it under{' '}
              <Link to="/filler-pools" className="underline">
                Filler pools
              </Link>{' '}
              and{' '}
              <Link to="/filler-rules" className="underline">
                Filler rules
              </Link>
              .
            </p>
            <ChannelFillerRules channelId={channel.id} />
          </div>
        )}
      </section>
    </div>
  )
}
