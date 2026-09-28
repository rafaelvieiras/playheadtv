import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
import { CreateDeviceDialog, PLATFORM_LABELS } from '@/components/CreateDeviceDialog'
import { api } from '@/lib/http'
import type { Device } from '@/lib/types'

function formatLastSeen(ms: number | null): string {
  if (ms === null) return 'never'
  return new Date(ms).toLocaleString('en-US')
}

export function DevicesPage() {
  const queryClient = useQueryClient()
  const { data: devices, isLoading, isError } = useQuery({
    queryKey: ['devices'],
    queryFn: () => api.get<Device[]>('/devices'),
  })

  const [createOpen, setCreateOpen] = useState(false)
  const [deletingDevice, setDeletingDevice] = useState<Device | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/devices/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['devices'] })
      toast.success('Device deleted.')
      setDeletingDevice(undefined)
    },
    onError: () => {
      toast.error('Could not delete the device.')
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Devices</h1>
        <Button onClick={() => setCreateOpen(true)}>Create device</Button>
      </div>

      <p className="text-sm text-muted-foreground">
        Paired devices can sync the catalog of push-mode media sources (
        <code>POST /media-sources/{'{id}'}/catalog-sync</code>) and resolve the programming guide to play channels.
      </p>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load devices.</p>}

      {devices && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Platform</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Last seen</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {devices.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
                  No paired devices yet.
                </TableCell>
              </TableRow>
            )}
            {devices.map((device) => (
              <TableRow key={device.id}>
                <TableCell>{device.name}</TableCell>
                <TableCell>{PLATFORM_LABELS[device.platform]}</TableCell>
                <TableCell>{new Date(device.created_at).toLocaleString('en-US')}</TableCell>
                <TableCell>{formatLastSeen(device.last_seen_at)}</TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button variant="destructive" size="sm" onClick={() => setDeletingDevice(device)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <CreateDeviceDialog open={createOpen} onOpenChange={setCreateOpen} />

      <AlertDialog open={deletingDevice !== undefined} onOpenChange={(open) => !open && setDeletingDevice(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete device?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently revoke the token of "{deletingDevice?.name}"; the device loses access to the
              API immediately. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingDevice && deleteMutation.mutate(deletingDevice.id)}
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
