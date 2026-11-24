import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { QueryError } from '@/components/QueryError'
import { StreamCascadeDialog } from '@/components/StreamCascadeDialog'
import { api } from '@/lib/http'
import type { Channel, StreamPreferenceRule } from '@/lib/types'

function channelLabel(channel: Channel): string {
  return `${channel.number} — ${channel.name}`
}

function stepsSummary(rule: StreamPreferenceRule | undefined): string {
  if (!rule) return 'Loading…'
  const count = (rule.rules.steps ?? []).length
  if (count === 0) return 'No preference configured'
  return count === 1 ? '1 step configured' : `${count} steps configured`
}

export function StreamSelectorPage() {
  const queryClient = useQueryClient()

  const {
    data: channels,
    isError: channelsError,
    error: channelsFailure,
    refetch: refetchChannels,
  } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })

  const {
    data: globalRule,
    isLoading: globalLoading,
    isError: globalError,
    error: globalFailure,
    refetch: refetchGlobal,
  } = useQuery({
    queryKey: ['stream-preference-rule', 'global', ''],
    queryFn: () => api.get<StreamPreferenceRule>('/stream-preferences/config?scope_type=global'),
  })

  const channelRuleQueries = useQueries({
    queries: (channels ?? []).map((channel) => ({
      queryKey: ['stream-preference-rule', 'channel', channel.id],
      queryFn: () =>
        api.get<StreamPreferenceRule>(
          `/stream-preferences/config?scope_type=channel&scope_id=${encodeURIComponent(channel.id)}`,
        ),
      enabled: !!channels,
    })),
  })

  const channelRows = useMemo(() => {
    return (channels ?? [])
      .map((channel, index) => ({ channel, rule: channelRuleQueries[index]?.data }))
      .filter((row) => row.rule && (row.rule.rules.steps ?? []).length > 0)
  }, [channels, channelRuleQueries])

  const configuredChannelIds = useMemo(() => new Set(channelRows.map((row) => row.channel.id)), [channelRows])
  const availableChannels = useMemo(
    () => (channels ?? []).filter((c) => !configuredChannelIds.has(c.id)),
    [channels, configuredChannelIds],
  )

  const [globalDialogOpen, setGlobalDialogOpen] = useState(false)
  const [editingChannel, setEditingChannel] = useState<Channel | undefined>(undefined)
  const [addChannelId, setAddChannelId] = useState<string>('')
  const [clearingChannel, setClearingChannel] = useState<Channel | undefined>(undefined)

  const clearMutation = useMutation({
    mutationFn: (channelId: string) =>
      api.put<StreamPreferenceRule>(
        `/stream-preferences/config?scope_type=channel&scope_id=${encodeURIComponent(channelId)}`,
        { rules: { steps: [] } },
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stream-preference-rule'] })
      toast.success('Channel override cleared.')
      setClearingChannel(undefined)
    },
    onError: () => {
      toast.error('Could not clear the channel override.')
    },
  })

  function openAddDialog() {
    const channel = (channels ?? []).find((c) => c.id === addChannelId)
    if (!channel) return
    setEditingChannel(channel)
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-xl font-semibold">Stream selector</h1>
        <p className="text-sm text-muted-foreground">
          Configure the audio/subtitle selection cascade used when a channel has no user-level preference: a
          tenant-wide default, and optional per-channel overrides.
        </p>
      </div>

      {globalError && (
        <QueryError
          subject="the global default"
          error={globalFailure}
          onRetry={() => void refetchGlobal()}
          stale={globalRule !== undefined}
        />
      )}

      <Card>
        <CardHeader>
          <CardTitle>Global default</CardTitle>
          <CardDescription>Applies to every channel that has no override of its own.</CardDescription>
        </CardHeader>
        <CardContent className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">{stepsSummary(globalRule)}</span>
          <Button variant="outline" size="sm" onClick={() => setGlobalDialogOpen(true)} disabled={globalLoading}>
            Edit
          </Button>
        </CardContent>
      </Card>

      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-medium">Channel overrides</h2>
        </div>

        {channelsError && (
          <QueryError
            subject="the channels"
            error={channelsFailure}
            onRetry={() => void refetchChannels()}
            stale={channels !== undefined}
          />
        )}

        <Table label="Channel overrides">
          <TableHeader>
            <TableRow>
              <TableHead>Channel</TableHead>
              <TableHead>Steps</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {channelRows.length === 0 && (
              <TableRow>
                <TableCell colSpan={3} className="text-center text-muted-foreground">
                  No channel overrides yet.
                </TableCell>
              </TableRow>
            )}
            {channelRows.map(({ channel, rule }) => (
              <TableRow key={channel.id}>
                <TableCell>{channelLabel(channel)}</TableCell>
                <TableCell>{rule?.rules.steps?.length ?? 0}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      aria-label={`Edit the override of ${channelLabel(channel)}`}
                      onClick={() => setEditingChannel(channel)}
                    >
                      Edit
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      aria-label={`Clear the override of ${channelLabel(channel)}`}
                      onClick={() => setClearingChannel(channel)}
                    >
                      Clear
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>

        <div className="flex items-end gap-2 border-t border-border pt-4">
          <div className="flex flex-1 flex-col gap-1.5">
            <span className="text-sm font-medium">Add a channel override</span>
            <Select value={addChannelId} onValueChange={setAddChannelId}>
              <SelectTrigger className="w-full" aria-label="Channel to override">
                <SelectValue placeholder="Select a channel" />
              </SelectTrigger>
              <SelectContent>
                {availableChannels.map((channel) => (
                  <SelectItem key={channel.id} value={channel.id}>
                    {channelLabel(channel)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button onClick={openAddDialog} disabled={!addChannelId}>
            Add override
          </Button>
        </div>
      </div>

      <StreamCascadeDialog
        open={globalDialogOpen}
        onOpenChange={setGlobalDialogOpen}
        scopeType="global"
        scopeId=""
        scopeLabel="Global default"
      />

      <StreamCascadeDialog
        open={editingChannel !== undefined}
        onOpenChange={(open) => {
          if (!open) {
            setEditingChannel(undefined)
            setAddChannelId('')
          }
        }}
        scopeType="channel"
        scopeId={editingChannel?.id ?? ''}
        scopeLabel={editingChannel ? channelLabel(editingChannel) : ''}
      />

      <ConfirmDialog
        open={clearingChannel !== undefined}
        onClose={() => setClearingChannel(undefined)}
        title={`Clear the override of ${clearingChannel ? channelLabel(clearingChannel) : 'this channel'}?`}
        description="The channel falls back to the global default. The global cascade is not touched."
        confirmLabel="Clear override"
        pendingLabel="Clearing…"
        onConfirm={() => clearingChannel && clearMutation.mutate(clearingChannel.id)}
        isPending={clearMutation.isPending}
        error={clearMutation.error}
      />
    </div>
  )
}
