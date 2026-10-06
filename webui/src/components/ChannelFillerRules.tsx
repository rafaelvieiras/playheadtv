import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'

import { InlineWarning } from '@/components/InlineWarning'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { KIND_LABELS, STRATEGY_LABELS } from '@/lib/filler'
import { api } from '@/lib/http'
import type { Block, Channel, FillerKind, FillerPool, FillerRule, ScheduleItem } from '@/lib/types'

const KIND_ORDER: FillerKind[] = ['preroll', 'midroll', 'postroll', 'tail', 'fallback']

// The filler rules that apply to one channel: its own channel-scoped rules
// plus the block-scoped rules of every block in the channel's schedule.
// Self-contained (fetches what it needs) so the channel page's Fillers
// section can drop it in with just a channel id. "Add rule" opens the rule
// editor prefilled for this channel.
export function ChannelFillerRules({ channelId }: { channelId: string }) {
  const { data: channel } = useQuery({
    queryKey: ['channels', channelId],
    queryFn: () => api.get<Channel>(`/channels/${channelId}`),
  })
  const scheduleId = channel?.schedule_id ?? null
  const { data: scheduleItems } = useQuery({
    queryKey: ['schedules', scheduleId, 'items'],
    queryFn: () => api.get<ScheduleItem[]>(`/schedules/${scheduleId}/items`),
    enabled: !!scheduleId,
  })
  const { data: rules, isLoading, isError } = useQuery({
    queryKey: ['filler-rules'],
    queryFn: () => api.get<FillerRule[]>('/filler-rules'),
  })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })
  const { data: pools } = useQuery({ queryKey: ['filler-pools'], queryFn: () => api.get<FillerPool[]>('/filler-pools') })

  const blockIds = new Set((scheduleItems ?? []).map((i) => i.block_id))
  const blockNames = new Map((blocks ?? []).map((b) => [b.id, b.name]))
  const poolNames = new Map((pools ?? []).map((p) => [p.id, p.name]))

  const applicable = (rules ?? [])
    .filter((r) => (r.scope_type === 'channel' ? r.scope_id === channelId : blockIds.has(r.scope_id)))
    .sort(
      (a, b) =>
        KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind) ||
        (a.scope_type === b.scope_type ? 0 : a.scope_type === 'channel' ? -1 : 1) ||
        a.position - b.position,
    )
  const hasFallback = applicable.some((r) => r.scope_type === 'channel' && r.kind === 'fallback')

  return (
    <div className="flex flex-col gap-3" data-testid="channel-filler-rules">
      <div className="flex justify-end gap-2">
        <Button asChild size="sm" variant="outline">
          <Link to={`/break-templates/new?scope_type=channel&scope_id=${encodeURIComponent(channelId)}`}>Add break template</Link>
        </Button>
        <Button asChild size="sm">
          <Link to={`/filler-rules/new?scope_type=channel&scope_id=${encodeURIComponent(channelId)}`}>Add filler rule</Link>
        </Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load filler rules.</p>}

      {rules && !hasFallback && (
        <InlineWarning title="No fallback filler for this channel">
          Time not covered by the schedule stays empty without a channel-scoped fallback rule.
        </InlineWarning>
      )}

      {rules && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Type</TableHead>
              <TableHead>Applies to</TableHead>
              <TableHead>Filler pool</TableHead>
              <TableHead>Selection</TableHead>
              <TableHead>Position</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {applicable.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  No filler rules apply to this channel.
                </TableCell>
              </TableRow>
            )}
            {applicable.map((rule) => (
              <TableRow key={rule.id} data-testid="channel-filler-rule-row">
                <TableCell>{KIND_LABELS[rule.kind]}</TableCell>
                <TableCell>
                  {rule.scope_type === 'channel' ? (
                    <Badge variant="secondary">Whole channel</Badge>
                  ) : (
                    <span>Block: {blockNames.get(rule.scope_id) ?? rule.scope_id}</span>
                  )}
                </TableCell>
                <TableCell>
                  <Link to={`/filler-pools/${rule.filler_pool_id}`} className="hover:underline">
                    {poolNames.get(rule.filler_pool_id) ?? rule.filler_pool_id}
                  </Link>
                </TableCell>
                <TableCell>{STRATEGY_LABELS[rule.selection_strategy]}</TableCell>
                <TableCell>{rule.position}</TableCell>
                <TableCell className="text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/filler-rules/${rule.id}`}>Edit</Link>
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
