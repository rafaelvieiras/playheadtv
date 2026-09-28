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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
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

export function MediaSourceDialog({ open, onOpenChange, source }: MediaSourceDialogProps) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(emptyForm)
  const [error, setError] = useState<string | undefined>(undefined)
  const isEditing = source !== undefined

  useEffect(() => {
    if (open) {
      setForm(source ? sourceToForm(source) : emptyForm)
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

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!form.name) {
      setError('Enter a name.')
      return
    }
    if (form.type !== 'local_folder' && !form.base_url) {
      setError('Enter the base URL.')
      return
    }
    setError(undefined)
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
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{isEditing ? 'Edit media source' : 'New media source'}</DialogTitle>
            <DialogDescription>
              {isEditing
                ? 'Update the source details. Changing the URL of a Jellyfin source repeats the connection test.'
                : 'On save, a Jellyfin source is tested automatically to decide between a direct connection and app-side sync.'}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="source-type">Type</FieldLabel>
              <Select value={form.type} onValueChange={(v) => setForm((f) => ({ ...f, type: v as SourceType }))}>
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
            </Field>
            <Field>
              <FieldLabel htmlFor="source-name">Name</FieldLabel>
              <Input
                id="source-name"
                required
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </Field>
            {form.type !== 'local_folder' && (
              <Field>
                <FieldLabel htmlFor="source-base-url">Base URL</FieldLabel>
                <Input
                  id="source-base-url"
                  type="url"
                  placeholder="http://jellyfin.example.com:8096"
                  required
                  value={form.base_url}
                  onChange={(e) => setForm((f) => ({ ...f, base_url: e.target.value }))}
                />
              </Field>
            )}
            <Field>
              <FieldLabel htmlFor="source-credential">Credential (optional)</FieldLabel>
              <Input
                id="source-credential"
                type="password"
                autoComplete="new-password"
                placeholder={isEditing ? 'Fill in only to replace the saved credential' : 'API key or password, if required'}
                value={form.credential}
                onChange={(e) => setForm((f) => ({ ...f, credential: e.target.value }))}
              />
              <FieldDescription>
                {isEditing
                  ? 'Never shown again after saving. Leave blank to keep the current credential.'
                  : 'Never shown again after saving.'}
              </FieldDescription>
            </Field>
            {error && <p className="text-sm text-destructive">{error}</p>}
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
