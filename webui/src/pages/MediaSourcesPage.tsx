import { Fragment, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, CircleAlert, CircleCheck, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { CreateDeviceDialog } from '@/components/CreateDeviceDialog'
import { MediaSourceDialog } from '@/components/MediaSourceDialog'
import { PageHeader } from '@/components/PageHeader'
import { Loading, QueryError } from '@/components/QueryError'
import { formatDateTime, formatMaybeDateTime, tzLabel } from '@/lib/format'
import { api } from '@/lib/http'
import type { MediaSource, PullSyncResponse, SourceType } from '@/lib/types'

const TYPE_LABELS: Record<SourceType, string> = {
  jellyfin: 'Jellyfin',
  plex: 'Plex',
  local_folder: 'Local folder',
}

const RELATIVE_TIME = new Intl.RelativeTimeFormat('en-US', { numeric: 'auto' })
const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 60 * 60 * 1000],
  ['month', 30 * 24 * 60 * 60 * 1000],
  ['week', 7 * 24 * 60 * 60 * 1000],
  ['day', 24 * 60 * 60 * 1000],
  ['hour', 60 * 60 * 1000],
  ['minute', 60 * 1000],
]

// "3 minutes ago" — the column answers "is this stale?" before the operator
// has to compare a raw timestamp with the clock in their head. The exact time
// stays available in the cell's title.
function relativeAge(ms: number): string {
  const delta = ms - Date.now()
  const magnitude = Math.abs(delta)
  if (magnitude < 60 * 1000) return 'just now'
  for (const [unit, size] of RELATIVE_UNITS) {
    if (magnitude >= size) return RELATIVE_TIME.format(Math.round(delta / size), unit)
  }
  return 'just now'
}

function probeAge(source: MediaSource): string {
  if (source.last_probe_at === null) return 'Not probed yet'
  return relativeAge(source.last_probe_at)
}

// The probe result used to live only inside the collapsed "Pair" panel, so a
// Jellyfin server that could not be reached looked exactly like a healthy one.
// A failed probe is the one state that changes what the operator can do next,
// so it gets the destructive color; a source type that never probes says so
// instead of borrowing the healthy neutral pill.
function HealthBadge({ source }: { source: MediaSource }) {
  if (source.last_probe_ok === false) {
    return (
      <Badge variant="destructive">
        <CircleAlert />
        Probe failed
      </Badge>
    )
  }
  if (source.last_probe_ok === true) {
    return (
      <Badge variant="default">
        <CircleCheck />
        Reachable
      </Badge>
    )
  }
  return <Badge variant="secondary">Not probed</Badge>
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
function pairingReason(source: MediaSource): string {
  if (source.type === 'jellyfin' && source.last_probe_ok === false) {
    return 'Could not connect directly to the given Jellyfin server: the connection failed or timed out.'
  }
  return 'This source type does not connect directly to the backend.'
}

export function MediaSourcesPage() {
  const queryClient = useQueryClient()
  const {
    data: sources,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
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
      <PageHeader
        title="Media sources"
        actions={<Button onClick={openCreateDialog}>New source</Button>}
      />

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the media sources"
          error={error}
          onRetry={() => void refetch()}
          stale={sources !== undefined}
        />
      )}

      {sources && (
        <Table label="Media sources">
          <TableHeader>
            <TableRow>
              <TableHead>Type</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Health</TableHead>
              <TableHead>Address</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Last synced</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sources.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  No media sources yet.
                </TableCell>
              </TableRow>
            )}
            {sources.map((source) => {
              const expanded = expandedId === source.id
              const pairPanelId = `source-pair-${source.id}`
              return (
                <Fragment key={source.id}>
                  <TableRow>
                    <TableCell>{TYPE_LABELS[source.type]}</TableCell>
                    <TableCell>{source.name}</TableCell>
                    <TableCell>
                      <div className="flex flex-col items-start gap-1">
                        <HealthBadge source={source} />
                        <span
                          className="text-xs text-muted-foreground"
                          title={
                            source.last_probe_at === null
                              ? undefined
                              : formatDateTime(source.last_probe_at)
                          }
                        >
                          {probeAge(source)}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{source.base_url ?? '—'}</TableCell>
                    <TableCell>
                      <StatusBadge source={source} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatMaybeDateTime(source.last_synced_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap justify-end gap-2">
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
                        {/* The disclosure stays inside the cell: it used to be
                            a plain flex child, which made the actions cell the
                            widest thing on the page and pushed the table to
                            1929 px on a desktop. */}
                        {source.sync_mode === 'push' && (
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-expanded={expanded}
                            aria-controls={expanded ? pairPanelId : undefined}
                            onClick={() => toggleExpanded(source.id)}
                          >
                            {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                            Pair
                          </Button>
                        )}
                        <Button variant="outline" size="sm" aria-label={`Edit ${source.name}`} onClick={() => openEditDialog(source)}>
                          Edit
                        </Button>
                        <Button
                          variant="destructive"
                          size="sm"
                          aria-label={`Delete ${source.name}`}
                          onClick={() => setDeletingSource(source)}
                        >
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                  {source.sync_mode === 'push' && expanded && (
                    <TableRow>
                      {/* whitespace-normal: TableCell is nowrap, and one
                          unbroken 1897 px line is what widened the table and
                          pushed every action off screen. */}
                      <TableCell colSpan={7} className="bg-muted/30 whitespace-normal">
                        <div id={pairPanelId} className="flex max-w-prose flex-col gap-3 p-2 text-sm">
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
          <TableCaption>{tzLabel(undefined)}</TableCaption>
        </Table>
      )}

      <MediaSourceDialog open={dialogOpen} onOpenChange={setDialogOpen} source={editingSource} />
      <CreateDeviceDialog open={deviceDialogOpen} onOpenChange={setDeviceDialogOpen} />

      <ConfirmDialog
        open={deletingSource !== undefined}
        onClose={() => setDeletingSource(undefined)}
        title={`Delete media source "${deletingSource?.name}"?`}
        description="Its catalog items and the channels that play them go with it. This action cannot be undone."
        confirmLabel="Delete source"
        pendingLabel="Deleting…"
        onConfirm={() => deletingSource && deleteMutation.mutate(deletingSource.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
