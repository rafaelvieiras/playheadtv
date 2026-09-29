import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
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

  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })

  const { data: globalRule, isLoading: globalLoading } = useQuery({
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

        <Table>
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
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button variant="outline" size="sm" onClick={() => setEditingChannel(channel)}>
                    Edit
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setClearingChannel(channel)}>
                    Clear
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>

        <div className="flex items-end gap-2 border-t border-border pt-4">
          <div className="flex flex-1 flex-col gap-1.5">
            <span className="text-sm font-medium">Add a channel override</span>
            <Select value={addChannelId} onValueChange={setAddChannelId}>
              <SelectTrigger className="w-full">
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

      <AlertDialog open={clearingChannel !== undefined} onOpenChange={(open) => !open && setClearingChannel(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear channel override?</AlertDialogTitle>
            <AlertDialogDescription>
              This replaces {clearingChannel ? channelLabel(clearingChannel) : 'this channel'}'s override with an
              empty cascade, falling back to the global default. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => clearingChannel && clearMutation.mutate(clearingChannel.id)}
              disabled={clearMutation.isPending}
            >
              Clear
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
