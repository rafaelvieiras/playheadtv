import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Power, Settings2, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
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
import { PageHeader } from '@/components/PageHeader'
import { Loading, QueryError } from '@/components/QueryError'
import { PluginSourceSettingsDialog } from '@/components/PluginSourceSettingsDialog'
import { formatDateTime, tzLabel } from '@/lib/format'
import { api } from '@/lib/http'
import { describePluginSettings, latestPluginVersion, versionsNewestFirst } from '@/lib/plugins'
import type { InstalledPlugin, PluginList, PluginOffering, PluginSource } from '@/lib/types'

/**
 * Plugins — the admin half of `docs/PLUGINS.md`.
 *
 * A plugin runs on this backend, not on a playback device: it lists a content
 * source and resolves a playable URL, and installing one means downloading a
 * program from the registry and running it here. That is why installing is a
 * session-only call and this page exists: a paired device may turn an installed
 * plugin on or off and remove it, but it can never install one.
 */
export function PluginsPage() {
  const queryClient = useQueryClient()
  const {
    data,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['plugins'],
    queryFn: () => api.get<PluginList>('/plugins'),
  })
  const {
    data: sources,
    isError: sourcesError,
    error: sourcesFailure,
    refetch: refetchSources,
  } = useQuery({
    queryKey: ['plugin-sources'],
    queryFn: () => api.get<PluginSource[]>('/plugin-sources'),
  })

  const [versionChoice, setVersionChoice] = useState<Record<string, string>>({})
  const [removing, setRemoving] = useState<InstalledPlugin | undefined>(undefined)
  const [editing, setEditing] = useState<PluginSource | undefined>(undefined)

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['plugins'] })
    queryClient.invalidateQueries({ queryKey: ['plugin-sources'] })
  }

  const installMutation = useMutation({
    mutationFn: ({ id, version }: { id: string; version: string }) =>
      api.post<InstalledPlugin>(`/plugins/${id}/install`, { version }),
    onSuccess: (plugin) => {
      refresh()
      toast.success(`Installed ${plugin.id} ${plugin.version}.`)
    },
    onError: (error: Error) => toast.error(error.message || 'Could not install the plugin.'),
  })

  const toggleMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      api.put<InstalledPlugin>(`/plugins/${id}`, { enabled }),
    onSuccess: (plugin) => {
      refresh()
      toast.success(`${plugin.id} is now ${plugin.enabled ? 'on' : 'off'}.`)
    },
    onError: (error: Error) => toast.error(error.message || 'Could not change the plugin.'),
  })

  const removeMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/plugins/${id}`),
    onSuccess: () => {
      refresh()
      toast.success('Plugin removed.')
      setRemoving(undefined)
    },
    onError: (error: Error) => toast.error(error.message || 'Could not remove the plugin.'),
  })

  const installed = data?.installed ?? []
  const installedById = new Map(installed.map((plugin) => [plugin.id, plugin]))
  const offerings = data?.plugins ?? []
  const notInstalled = offerings.filter((offering) => !installedById.has(offering.id))
  // A source is only configurable while the plugin that declares the settings is
  // installed: the declaration comes from that plugin's own manifest.
  const configurableSources = (sources ?? []).filter(
    (source) => (installedById.get(source.plugin_id)?.config_fields.length ?? 0) > 0,
  )

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Plugins" />

      <p className="text-sm text-muted-foreground">
        A plugin runs on this backend: it lists a content source and resolves the URL a playback device
        plays. The core only hosts it — install, verify, run (<code>docs/PLUGINS.md</code>).
      </p>

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the plugins"
          error={error}
          onRetry={() => void refetch()}
          stale={data !== undefined}
        />
      )}

      {data && !data.registry_configured && (
        <Alert>
          <AlertTitle>No plugin registry configured</AlertTitle>
          <AlertDescription>
            This backend has no <code className="break-all">PLAYHEADTV_PLUGIN_REGISTRY_URL</code>, so
            nothing can be installed here. A channel whose content comes from a plugin stays hidden until
            one is.
          </AlertDescription>
        </Alert>
      )}

      <section className="flex flex-col gap-3">
        <h2 className="text-base font-medium">Installed</h2>
        {data && (
          <Table label="Installed plugins">
            <TableHeader>
              <TableRow>
                <TableHead>Plugin</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Platform</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Installed</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {installed.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="text-center text-muted-foreground">
                    No plugin installed on this backend.
                  </TableCell>
                </TableRow>
              )}
              {installed.map((plugin) => {
                const offering = offerings.find((entry) => entry.id === plugin.id)
                const latest = latestPluginVersion((offering?.versions ?? []).map((entry) => entry.version))
                return (
                  <TableRow key={plugin.id}>
                    <TableCell>
                      <div className="font-medium">{offering?.name || plugin.id}</div>
                      <div className="text-xs text-muted-foreground">{plugin.capabilities}</div>
                    </TableCell>
                    <TableCell>
                      {plugin.version}
                      {latest !== undefined && latest !== plugin.version && (
                        <span className="ml-2 text-xs text-muted-foreground">({latest} available)</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {plugin.os}/{plugin.arch}
                    </TableCell>
                    <TableCell>
                      <Badge variant={plugin.enabled ? 'default' : 'secondary'}>
                        {plugin.enabled ? 'on' : 'off'}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDateTime(plugin.updated_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-2">
                        {latest !== undefined && latest !== plugin.version && (
                          <Button
                            size="sm"
                            onClick={() => installMutation.mutate({ id: plugin.id, version: latest })}
                            disabled={installMutation.isPending}
                          >
                            <Download className="size-4" />
                            Update to {latest}
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => toggleMutation.mutate({ id: plugin.id, enabled: !plugin.enabled })}
                          disabled={toggleMutation.isPending}
                        >
                          <Power className="size-4" />
                          {plugin.enabled ? 'Turn off' : 'Turn on'}
                        </Button>
                        <Button variant="outline" size="sm" onClick={() => setRemoving(plugin)}>
                          <Trash2 className="size-4" />
                          Remove
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
            <TableCaption>{tzLabel(undefined)}</TableCaption>
          </Table>
        )}
        <p className="text-xs text-muted-foreground">
          A channel that uses a plugin plays only while it is on. Turning it off or removing it is also
          allowed from a paired device; installing is not.
        </p>
      </section>

      {data && (
        <section className="flex flex-col gap-3">
          <h2 className="text-base font-medium">Available from the registry</h2>
          {notInstalled.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {data?.registry_configured
                ? 'Nothing else is offered by the configured registry.'
                : 'No registry is configured, so there is nothing to offer.'}
            </p>
          ) : (
            <Table label="Available plugins">
              <TableHeader>
                <TableRow>
                  <TableHead>Plugin</TableHead>
                  <TableHead>Version</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {notInstalled.map((offering) => (
                  <OfferingRow
                    key={offering.id}
                    offering={offering}
                    choice={versionChoice[offering.id]}
                    onChoice={(version) => setVersionChoice((prev) => ({ ...prev, [offering.id]: version }))}
                    install={(version) => installMutation.mutate({ id: offering.id, version })}
                    busy={installMutation.isPending}
                  />
                ))}
              </TableBody>
            </Table>
          )}
          <p className="text-xs text-muted-foreground">
            Installing downloads the archive, checks its SHA-256 against the registry, unpacks it and starts
            the plugin. A plugin source then lists its catalog on the next sync.
          </p>
        </section>
      )}

      {data && (
        <section className="flex flex-col gap-3">
          <h2 className="text-base font-medium">Source settings</h2>
          {sourcesError ? (
            <QueryError
              subject="the plugin sources"
              error={sourcesFailure}
              onRetry={() => void refetchSources()}
              stale={sources !== undefined}
            />
          ) : configurableSources.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No installed plugin declares settings for its sources.
            </p>
          ) : (
            <Table label="Plugin source settings">
              <TableHeader>
                <TableRow>
                  <TableHead>Source</TableHead>
                  <TableHead>Plugin</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {configurableSources.map((source) => (
                  <TableRow key={source.id}>
                    <TableCell>
                      <div className="font-medium">{source.name || source.id}</div>
                      <div className="text-xs text-muted-foreground">
                        {describePluginSettings(
                          installedById.get(source.plugin_id)?.config_fields ?? [],
                          source.settings,
                        )}
                      </div>
                    </TableCell>
                    <TableCell>{source.plugin_id}</TableCell>
                    <TableCell className="text-right">
                      <Button variant="outline" size="sm" onClick={() => setEditing(source)}>
                        <Settings2 className="size-4" />
                        Edit settings
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </section>
      )}
      {editing && (
        <PluginSourceSettingsDialog
          key={editing.id}
          source={editing}
          fields={installedById.get(editing.plugin_id)?.config_fields ?? []}
          onOpenChange={(open) => {
            if (!open) setEditing(undefined)
          }}
        />
      )}

      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={`Remove the plugin "${removing?.id}"?`}
        description="The plugin is deleted from this backend. A channel whose content comes from it stops being offered until it is installed again."
        confirmLabel="Remove plugin"
        pendingLabel="Removing…"
        onConfirm={() => removing && removeMutation.mutate(removing.id)}
        isPending={removeMutation.isPending}
        error={removeMutation.error}
      />
    </div>
  )
}

interface OfferingRowProps {
  offering: PluginOffering
  choice: string | undefined
  onChoice: (version: string) => void
  install: (version: string) => void
  busy: boolean
}

function OfferingRow({ offering, choice, onChoice, install, busy }: OfferingRowProps) {
  const versions = versionsNewestFirst(offering.versions.map((entry) => entry.version))
  const selected = choice ?? versions[0]

  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{offering.name || offering.id}</div>
        {offering.description && (
          <div className="text-xs text-muted-foreground">{offering.description}</div>
        )}
      </TableCell>
      <TableCell>
        <Select value={selected} onValueChange={onChoice}>
          <SelectTrigger className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {versions.map((version) => (
              <SelectItem key={version} value={version}>
                {version}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </TableCell>
      <TableCell className="text-right">
        <Button size="sm" onClick={() => install(selected)} disabled={busy || !selected}>
          <Download className="size-4" />
          Install
        </Button>
      </TableCell>
    </TableRow>
  )
}
