import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Copy } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/lib/http'
import type { CreateDeviceResponse, DeviceInput, Platform } from '@/lib/types'

interface CreateDeviceDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export const PLATFORM_LABELS: Record<Platform, string> = {
  linux: 'Linux',
  windows: 'Windows',
  android: 'Android',
  android_tv: 'Android TV',
  ios: 'iOS',
  tvos: 'tvOS',
}

const emptyForm: DeviceInput = { name: '', platform: 'linux' }

// Shared by MediaSourcesPage (opened from a push-mode source's pairing
// panel) and DevicesPage (opened standalone) — POST /devices only ever
// returns the raw token in this one response, so both entry points need the
// exact same "show it once, make it easy to copy" flow.
export function CreateDeviceDialog({ open, onOpenChange }: CreateDeviceDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<DeviceInput>(emptyForm)
  const [result, setResult] = useState<CreateDeviceResponse | undefined>(undefined)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (open) {
      setForm(emptyForm)
      setResult(undefined)
      setCopied(false)
    }
  }, [open])

  const mutation = useMutation({
    mutationFn: () => api.post<CreateDeviceResponse>('/devices', form),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['devices'] })
      toast.success('Device created.')
      setResult(data)
    },
    onError: () => {
      toast.error('Could not create the device.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    mutation.mutate()
  }

  async function handleCopy() {
    if (!result) return
    try {
      await navigator.clipboard.writeText(result.token)
      setCopied(true)
      toast.success('Token copied.')
    } catch {
      toast.error('Could not copy automatically. Select the text in the field and copy it manually.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {result ? (
          <>
            <DialogHeader>
              <DialogTitle>Device created</DialogTitle>
              <DialogDescription>
                "{result.device.name}" ({PLATFORM_LABELS[result.device.platform]}) was paired successfully.
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-3 py-4">
              <p className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
                Copy the token now: it is shown only once and the API will never return it again. Configure it in
                the Playhead TV client to finish pairing.
              </p>
              <Field>
                <FieldLabel htmlFor="device-token">Device token</FieldLabel>
                <div className="flex gap-2">
                  <Input
                    id="device-token"
                    readOnly
                    value={result.token}
                    onFocus={(e) => e.currentTarget.select()}
                    className="font-mono"
                  />
                  <Button type="button" variant="outline" onClick={handleCopy}>
                    <Copy className="size-4" />
                    {copied ? 'Copied' : 'Copy'}
                  </Button>
                </div>
              </Field>
            </div>
            <DialogFooter>
              <Button type="button" onClick={() => onOpenChange(false)}>
                Done
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={handleSubmit}>
            <DialogHeader>
              <DialogTitle>Create device</DialogTitle>
              <DialogDescription>
                Generates a pairing token for a Playhead TV client. The token is shown only once, right after
                creation.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-4">
              <Field>
                <FieldLabel htmlFor="device-name">Name</FieldLabel>
                <Input
                  id="device-name"
                  required
                  value={form.name}
                  onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="device-platform">Platform</FieldLabel>
                <Select
                  value={form.platform}
                  onValueChange={(v) => setForm((f) => ({ ...f, platform: v as Platform }))}
                >
                  <SelectTrigger id="device-platform" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(PLATFORM_LABELS) as Platform[]).map((p) => (
                      <SelectItem key={p} value={p}>
                        {PLATFORM_LABELS[p]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending ? 'Creating…' : 'Create device'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
