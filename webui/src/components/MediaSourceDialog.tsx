import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/lib/http'
import type { MediaSource, MediaSourceInput, SourceType } from '@/lib/types'

interface MediaSourceDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Present when editing an existing source, absent when creating one.
  source?: MediaSource
}

const TYPE_LABELS: Record<SourceType, string> = {
  jellyfin: 'Jellyfin',
  plex: 'Plex',
  local_folder: 'Local folder',
}

// Internal form shape. base_url is kept as a plain string (never null) so a
// controlled Input never flips to uncontrolled; buildPayload turns it back
// into null for local_folder before the request goes out.
interface FormState {
  type: SourceType
  name: string
  base_url: string
  credential: string
}

const emptyForm: FormState = { type: 'jellyfin', name: '', base_url: '', credential: '' }

// credential is deliberately never pre-filled from an existing source — the
// API never returns it (see MediaSource's comment in lib/types.ts), so there
// is nothing to show, and leaving it blank means "don't change it" on save.
function sourceToForm(source: MediaSource): FormState {
  return { type: source.type, name: source.name, base_url: source.base_url ?? '', credential: '' }
}

// Each type reaches the backend differently, so one Jellyfin-only sentence
// cannot describe all three: a local folder has no address and no credential
// here (its path lives on the paired client that scans it), and Plex has no
// direct connection in v1.
function dialogDescription(isEditing: boolean, type: SourceType): string {
  if (type === 'local_folder') {
    return 'A local folder is cataloged by a paired Playhead TV client, not by this backend.'
  }
  if (type === 'plex') {
    return isEditing
      ? 'Plex is not supported for a direct connection in v1. A paired client pushes its catalog.'
      : 'Plex is not supported for a direct connection in v1: the source is created in app-side sync mode, and a paired client pushes its catalog.'
  }
  return isEditing
    ? 'Update the source details. Changing the URL of a Jellyfin source repeats the connection test.'
    : 'On save, a Jellyfin source is tested automatically to decide between a direct connection and app-side sync.'
}

export function MediaSourceDialog({ open, onOpenChange, source }: MediaSourceDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [errors, setErrors] = useState<Partial<Record<'name' | 'base_url', string>>>({})
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = source !== undefined

  useEffect(() => {
    if (open) {
      setForm(source ? sourceToForm(source) : emptyForm)
      setErrors({})
      setError(undefined)
    }
  }, [open, source])

  const mutation = useMutation({
    mutationFn: (payload: MediaSourceInput) =>
      isEditing
        ? api.put<MediaSource>(`/media-sources/${source.id}`, payload)
        : api.post<MediaSource>('/media-sources', payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['media-sources'] })
      toast.success(isEditing ? 'Media source updated.' : 'Media source created.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error(
        isEditing ? 'Could not update the media source.' : 'Could not create the media source.',
      )
    },
  })

  function set<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((current) => ({ ...current, [key]: value }))
    if (key === 'name' || key === 'base_url') setErrors((current) => ({ ...current, [key]: undefined }))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    // Every problem at once, each under its own field: one message at a time
    // made fixing the name reveal that the URL was missing too.
    const found: Partial<Record<'name' | 'base_url', string>> = {}
    if (!form.name.trim()) found.name = 'Enter a name.'
    if (form.type !== 'local_folder' && !form.base_url.trim()) found.base_url = 'Enter the base URL.'
    setErrors(found)
    setError(undefined)
    if (Object.keys(found).length > 0) return
    const payload: MediaSourceInput = {
      type: form.type,
      name: form.name,
      base_url: form.type === 'local_folder' ? null : form.base_url,
      // Omit the key entirely when blank — see MediaSourceInput's comment.
      ...(form.credential ? { credential: form.credential } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeOnOutsideClick={false}>
        <form onSubmit={handleSubmit} noValidate>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit media source' : 'New media source'}</DialogTitle>
            <DialogDescription>{dialogDescription(isEditing, form.type)}</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="source-type">Type</FieldLabel>
              <Select value={form.type} onValueChange={(v) => set('type', v as SourceType)}>
                <SelectTrigger id="source-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(TYPE_LABELS) as SourceType[]).map((t) => (
                    <SelectItem key={t} value={t}>
                      {TYPE_LABELS[t]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FieldDescription>
                Only Jellyfin connects directly in v1. Plex and local folder sources rely on a paired client
                to push their catalog.
              </FieldDescription>
            </Field>
            <Field data-invalid={!!errors.name}>
              <FieldLabel htmlFor="source-name">
                Name <span className="text-destructive">*</span>
              </FieldLabel>
              <Input
                id="source-name"
                aria-invalid={!!errors.name}
                aria-describedby={errors.name ? 'source-name-error' : undefined}
                value={form.name}
                onChange={(e) => set('name', e.target.value)}
              />
              <FieldError id="source-name-error">{errors.name}</FieldError>
              {form.type === 'local_folder' && (
                <FieldDescription>
                  Set the folder path on the client that scans it; the backend stores only this source's
                  name. No address or credential is needed here.
                </FieldDescription>
              )}
            </Field>
            {form.type !== 'local_folder' && (
              <Field data-invalid={!!errors.base_url}>
                <FieldLabel htmlFor="source-base-url">
                  Base URL <span className="text-destructive">*</span>
                </FieldLabel>
                <Input
                  id="source-base-url"
                  type="url"
                  placeholder="http://jellyfin.example.com:8096"
                  aria-invalid={!!errors.base_url}
                  aria-describedby={errors.base_url ? 'source-base-url-error' : undefined}
                  value={form.base_url}
                  onChange={(e) => set('base_url', e.target.value)}
                />
                <FieldError id="source-base-url-error">{errors.base_url}</FieldError>
              </Field>
            )}
            {form.type !== 'local_folder' && (
              <Field>
                <FieldLabel htmlFor="source-credential">Credential (optional)</FieldLabel>
                <Input
                  id="source-credential"
                  type="password"
                  autoComplete="new-password"
                  placeholder={isEditing ? 'Fill in only to replace the saved credential' : 'API key or password, if required'}
                  value={form.credential}
                  onChange={(e) => set('credential', e.target.value)}
                />
                <FieldDescription>
                  {isEditing
                    ? 'Never shown again after saving. Leave blank to keep the current credential.'
                    : 'Never shown again after saving.'}
                </FieldDescription>
              </Field>
            )}
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
