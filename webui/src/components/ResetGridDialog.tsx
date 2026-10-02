import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { api } from '@/lib/http'
import type { Channel } from '@/lib/types'

// Confirmation for POST /channels/{id}/reset-epg, shared by the channels list
// and the channel page. `channel` undefined means closed.
export function ResetGridDialog({
  channel,
  onClose,
}: {
  channel: Channel | undefined
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const resetMutation = useMutation({
    mutationFn: (id: string) => api.post(`/channels/${id}/reset-epg`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['epg'] })
      toast.success('Channel grid reset.')
      onClose()
    },
    onError: () => {
      toast.error('Could not reset the channel grid.')
    },
  })

  return (
    <AlertDialog open={channel !== undefined} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Reset the grid of this channel?</AlertDialogTitle>
          <AlertDialogDescription>
            This discards the upcoming programs of "{channel?.name}" and the one on air right now, restarts its
            rotation from the beginning and regenerates immediately. Viewers switch program right away.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault()
              if (channel) resetMutation.mutate(channel.id)
            }}
            disabled={resetMutation.isPending}
          >
            Reset grid
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
