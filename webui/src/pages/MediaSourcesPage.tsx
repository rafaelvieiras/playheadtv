import { Fragment, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, RefreshCw } from 'lucide-react'
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { CreateDeviceDialog } from '@/components/CreateDeviceDialog'
import { MediaSourceDialog } from '@/components/MediaSourceDialog'
import { api } from '@/lib/http'
import type { MediaSource, PullSyncResponse, SourceType } from '@/lib/types'

const TYPE_LABELS: Record<SourceType, string> = {
  jellyfin: 'Jellyfin',
  plex: 'Plex',
  local_folder: 'Local folder',
}

function StatusBadge({ source }: { source: MediaSource }) {
  if (source.sync_mode === 'pull') {
    return (
      <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
        Directly connected
      </span>
    )
  }
  return (
    <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
      Requires app-side sync
    </span>
  )
}

// Explains *why* this source is in push mode: a failed Jellyfin probe reads
// differently from a type that never attempts a direct connection at all.
function formatLastSynced(ms: number | null): string {
  if (ms === null) return '—'
  return new Date(ms).toLocaleString('en-US')
}

function pairingReason(source: MediaSource): string {
  if (source.type === 'jellyfin' && source.last_probe_ok === false) {
    return 'Could not connect directly to the given Jellyfin server: the connection failed or timed out.'
  }
  return 'This source type does not connect directly to the backend.'
}

export function MediaSourcesPage() {
  const queryClient = useQueryClient()
  const { data: sources, isLoading, isError } = useQuery({
    queryKey: ['media-sources'],
    queryFn: () => api.get<MediaSource[]>('/media-sources'),
  })

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingSource, setEditingSource] = useState<MediaSource | undefined>(undefined)
  const [deletingSource, setDeletingSource] = useState<MediaSource | undefined>(undefined)
  const [expandedId, setExpandedId] = useState<string | undefined>(undefined)
  const [deviceDialogOpen, setDeviceDialogOpen] = useState(false)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/media-sources/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['media-sources'] })
      toast.success('Media source deleted.')
      setDeletingSource(undefined)
    },
    onError: () => {
      toast.error('Could not delete the media source.')
    },
  })

  const pullSyncMutation = useMutation({
    mutationFn: (id: string) => api.post<PullSyncResponse>(`/media-sources/${id}/pull-sync`),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['media-sources'] })
      toast.success(
        `Synced: ${data.result.items_upserted} item(s) updated, ${data.result.items_deleted} removed.`,
      )
    },
    onError: () => {
      toast.error('Could not sync with Jellyfin.')
    },
  })

  function openCreateDialog() {
    setEditingSource(undefined)
    setDialogOpen(true)
  }

  function openEditDialog(source: MediaSource) {
    setEditingSource(source)
    setDialogOpen(true)
  }

  function toggleExpanded(id: string) {
    setExpandedId((current) => (current === id ? undefined : id))
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Media sources</h1>
        <Button onClick={openCreateDialog}>New source</Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load media sources.</p>}

      {sources && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Type</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Address</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Last synced</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sources.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  No media sources yet.
                </TableCell>
              </TableRow>
            )}
            {sources.map((source) => {
              const expanded = expandedId === source.id
              return (
                <Fragment key={source.id}>
                  <TableRow>
                    <TableCell>{TYPE_LABELS[source.type]}</TableCell>
                    <TableCell>{source.name}</TableCell>
                    <TableCell className="text-muted-foreground">{source.base_url ?? '—'}</TableCell>
                    <TableCell>
                      <StatusBadge source={source} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">{formatLastSynced(source.last_synced_at)}</TableCell>
                    <TableCell className="flex justify-end gap-2 text-right">
                      {source.type === 'jellyfin' && source.sync_mode === 'pull' && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => pullSyncMutation.mutate(source.id)}
                          disabled={pullSyncMutation.isPending}
                        >
                          <RefreshCw className="size-4" />
                          Sync now
                        </Button>
                      )}
                      {source.sync_mode === 'push' && (
                        <Button variant="ghost" size="sm" onClick={() => toggleExpanded(source.id)}>
                          {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                          Pair
                        </Button>
                      )}
                      <Button variant="outline" size="sm" onClick={() => openEditDialog(source)}>
                        Edit
                      </Button>
                      <Button variant="destructive" size="sm" onClick={() => setDeletingSource(source)}>
                        Delete
                      </Button>
                    </TableCell>
                  </TableRow>
                  {source.sync_mode === 'push' && expanded && (
                    <TableRow>
                      <TableCell colSpan={6} className="bg-muted/30">
                        <div className="flex flex-col gap-3 p-2 text-sm">
                          <p className="text-muted-foreground">
                            {pairingReason(source)} To sync this source's catalog, open a Playhead TV client on a device
                            that is on the same network as the source and pair a device below. The catalog is then
                            pushed from the app to this backend automatically, with no further action here.
                          </p>
                          <div>
                            <Button type="button" variant="outline" size="sm" onClick={() => setDeviceDialogOpen(true)}>
                              Create device
                            </Button>
                          </div>
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              )
            })}
          </TableBody>
        </Table>
      )}

      <MediaSourceDialog open={dialogOpen} onOpenChange={setDialogOpen} source={editingSource} />
      <CreateDeviceDialog open={deviceDialogOpen} onOpenChange={setDeviceDialogOpen} />

      <AlertDialog open={deletingSource !== undefined} onOpenChange={(open) => !open && setDeletingSource(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete media source?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete the source "{deletingSource?.name}". This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingSource && deleteMutation.mutate(deletingSource.id)}
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
