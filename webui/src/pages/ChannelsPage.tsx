import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'
import { TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ChannelDialog } from '@/components/ChannelDialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Loading, QueryError } from '@/components/QueryError'
import { ResetGridDialog } from '@/components/ResetGridDialog'
import { api } from '@/lib/http'
import type { Channel, Schedule } from '@/lib/types'

// What airs a channel: the schedule it is assigned, or null for "nothing does".
function scheduleLabel(id: string | null, names: Map<string, string>): string | null {
  if (!id) return null
  // A schedule that is not in the list was deleted between the two reads; say
  // so instead of printing a bare id or claiming there is none.
  return names.get(id) ?? 'Unknown schedule'
}

// The materializer's warnings used to live in a `title` attribute (unreachable
// on a touch screen) and then in a paragraph under every affected row (a ~155
// character reason buries the rows next to it). A badge says how many there are;
// pressing it opens the reasons. The text comes with GET /channels — asking
// /channels/{id}/epg-warnings instead expands a week of programming per channel
// and costs about half a minute each.
function ChannelNoticeBadge({
  channel,
  open,
  onToggle,
}: {
  channel: Channel
  open: boolean
  onToggle: () => void
}) {
  const notices = channel.notices ?? []
  if (notices.length === 0) return null
  const panelId = `channel-notice-text-${channel.id}`
  return (
    <div className="mt-1 flex flex-col items-start gap-1">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        data-testid={`channel-notice-${channel.number}`}
        className="inline-flex w-fit items-center gap-1 rounded-full border border-destructive/40 bg-destructive/10 px-2 py-0.5 text-xs font-medium text-destructive max-sm:min-h-11"
      >
        <TriangleAlert className="size-3 shrink-0" aria-hidden="true" />
        {notices.length === 1 ? '1 warning' : `${notices.length} warnings`}
      </button>
      {open && (
        <ul id={panelId} className="max-w-md list-disc space-y-1 pl-4 text-xs break-words text-destructive">
          {notices.map((notice, index) => (
            <li key={`${notice.block_id}-${notice.block_item_id ?? notice.kind}-${index}`}>{notice.message}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

export function ChannelsPage() {
  const queryClient = useQueryClient()
  const channelsQuery = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })
  // The Schedule column needs the names, and the table is only complete with
  // both answers: a failed read is one error panel, never a row claiming
  // "No schedule" because the list of schedules did not arrive.
  const schedulesQuery = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<Schedule[]>('/schedules'),
  })
  const channels = channelsQuery.data
  const schedules = schedulesQuery.data
  const isLoading = channelsQuery.isLoading || schedulesQuery.isLoading
  const isError = channelsQuery.isError || schedulesQuery.isError
  const error = channelsQuery.error ?? schedulesQuery.error

  const scheduleNames = useMemo(
    () => new Map((schedules ?? []).map((schedule) => [schedule.id, schedule.name])),
    [schedules],
  )

  const [dialogOpen, setDialogOpen] = useState(false)
  const [deletingChannel, setDeletingChannel] = useState<Channel | undefined>(undefined)
  const [resettingChannel, setResettingChannel] = useState<Channel | undefined>(undefined)
  const [expandedNotices, setExpandedNotices] = useState<ReadonlySet<string>>(new Set())

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/channels/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Channel deleted.')
      setDeletingChannel(undefined)
    },
    onError: () => {
      toast.error('Could not delete the channel.')
    },
  })

  function toggleNotices(id: string) {
    setExpandedNotices((current) => {
      const next = new Set(current)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  function retry() {
    void channelsQuery.refetch()
    void schedulesQuery.refetch()
  }

  function openCreateDialog() {
    setDialogOpen(true)
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Channels</h1>
        <Button onClick={openCreateDialog}>New channel</Button>
      </div>

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject={channelsQuery.isError ? 'the channels' : 'the schedules'}
          error={error}
          onRetry={retry}
        />
      )}

      {/* Both answers, or nothing: a table drawn while the schedule names are
          still in flight would label every assigned schedule "Unknown". */}
      {channels && schedules && !isError && (
        <Table label="Channels">
          <TableHeader>
            <TableRow>
              <TableHead>Number</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Schedule</TableHead>
              <TableHead>Timezone</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {channels.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
                  No channels yet.
                </TableCell>
              </TableRow>
            )}
            {channels.map((channel) => {
              const airing = scheduleLabel(channel.schedule_id, scheduleNames)
              const gaps = scheduleLabel(channel.base_schedule_id, scheduleNames)
              return (
                <TableRow key={channel.id}>
                  <TableCell>{channel.number}</TableCell>
                  <TableCell className="whitespace-normal">
                    <Link to={`/channels/${channel.id}`} className="hover:underline">
                      {channel.name}
                    </Link>
                    <ChannelNoticeBadge
                      channel={channel}
                      open={expandedNotices.has(channel.id)}
                      onToggle={() => toggleNotices(channel.id)}
                    />
                  </TableCell>
                  <TableCell className="whitespace-normal">
                    {airing ?? <span className="text-muted-foreground">No schedule</span>}
                    {gaps && <span className="block text-xs text-muted-foreground">Gaps: {gaps}</span>}
                  </TableCell>
                  <TableCell>{channel.timezone}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-2">
                      <Button asChild variant="outline" size="sm">
                        <Link to={`/channels/${channel.id}`}>Open</Link>
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        data-testid={`reset-grid-${channel.number}`}
                        aria-label={`Reset grid of ${channel.name}`}
                        onClick={() => setResettingChannel(channel)}
                      >
                        Reset grid
                      </Button>
                      <Button
                        variant="destructive"
                        size="sm"
                        aria-label={`Delete ${channel.name}`}
                        onClick={() => setDeletingChannel(channel)}
                      >
                        Delete
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      <ChannelDialog open={dialogOpen} onOpenChange={setDialogOpen} />

      <ResetGridDialog channel={resettingChannel} onClose={() => setResettingChannel(undefined)} />

      <ConfirmDialog
        open={deletingChannel !== undefined}
        onClose={() => setDeletingChannel(undefined)}
        title={`Delete channel "${deletingChannel?.name}"?`}
        description={
          <>
            This deletes channel {deletingChannel?.number} and its programming. The blocks and collections it
            points at are not deleted. This action cannot be undone.
          </>
        }
        confirmLabel="Delete channel"
        pendingLabel="Deleting…"
        onConfirm={() => deletingChannel && deleteMutation.mutate(deletingChannel.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
