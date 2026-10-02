import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'

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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ChannelDialog } from '@/components/ChannelDialog'
import { ResetGridDialog } from '@/components/ResetGridDialog'
import { api } from '@/lib/http'
import type { Channel } from '@/lib/types'

export function ChannelsPage() {
  const queryClient = useQueryClient()
  const { data: channels, isLoading, isError } = useQuery({
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

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load channels.</p>}

      {channels && (
        <Table>
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
                <TableCell>
                  <Link to={`/channels/${channel.id}`} className="hover:underline">
                    {channel.name}
                  </Link>
                </TableCell>
                <TableCell>{channel.timezone}</TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/channels/${channel.id}`}>Open</Link>
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    data-testid={`reset-grid-${channel.number}`}
                    onClick={() => setResettingChannel(channel)}
                  >
                    Reset grid
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeletingChannel(channel)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <ChannelDialog open={dialogOpen} onOpenChange={setDialogOpen} />

      <ResetGridDialog channel={resettingChannel} onClose={() => setResettingChannel(undefined)} />

      <AlertDialog open={deletingChannel !== undefined} onOpenChange={(open) => !open && setDeletingChannel(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete channel?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete the channel "{deletingChannel?.name}". This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingChannel && deleteMutation.mutate(deletingChannel.id)}
              disabled={deleteMutation.isPending}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
