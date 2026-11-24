import { useState } from 'react'
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
import type { Channel } from '@/lib/types'

export function ChannelsPage() {
  const queryClient = useQueryClient()
  const {
    data: channels,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })

  const [dialogOpen, setDialogOpen] = useState(false)
  const [deletingChannel, setDeletingChannel] = useState<Channel | undefined>(undefined)
  const [resettingChannel, setResettingChannel] = useState<Channel | undefined>(undefined)

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
          subject="the channels"
          error={error}
          onRetry={() => void refetch()}
          stale={channels !== undefined}
        />
      )}

      {channels && (
        <Table label="Channels">
          <TableHeader>
            <TableRow>
              <TableHead>Number</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Timezone</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {channels.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  No channels yet.
                </TableCell>
              </TableRow>
            )}
            {channels.map((channel) => (
              <TableRow key={channel.id}>
                <TableCell>{channel.number}</TableCell>
                <TableCell className="whitespace-normal">
                  <Link to={`/channels/${channel.id}`} className="hover:underline">
                    {channel.name}
                  </Link>
                  {/* The loader's warnings used to live in a `title` attribute,
                      i.e. they were unreachable from a keyboard or a touch
                      screen. A channel that cannot air anything says so here. */}
                  {channel.notices && channel.notices.length > 0 && (
                    <span
                      className="mt-1 flex items-start gap-1 text-xs text-destructive"
                      data-testid={`channel-notice-${channel.number}`}
                    >
                      <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
                      <span>{channel.notices.map((n) => n.message).join(' ')}</span>
                    </span>
                  )}
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
            ))}
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
