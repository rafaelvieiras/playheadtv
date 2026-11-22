import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { CHANNEL_LOGO_TYPES, channelLogoPath, validateChannelLogoFile } from '@/lib/channelLogo'
import { ApiError, api } from '@/lib/http'
import { useAuthedImageUrl } from '@/lib/storedImage'
import { fileToBase64 } from '@/lib/watermark'
import type { Channel, ChannelLogo } from '@/lib/types'

// The channel's logo: one image, drawn by the playback client in its guide and
// by the guide here in the channel column. A channel without a logo is not a
// problem — the client falls back to the channel name — so this is a single
// optional control, not a form.
export function ChannelLogoSection({ channel }: { channel: Channel }) {
  const queryClient = useQueryClient()
  const inputRef = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File | null>(null)
  const [error, setError] = useState<string | null>(null)

  // The picked file previews from memory; the object URL is derived from the
  // file and released when it changes, so no state has to be reset on unmount.
  const filePreview = useMemo(() => (file ? URL.createObjectURL(file) : null), [file])
  useEffect(() => {
    return () => {
      if (filePreview) URL.revokeObjectURL(filePreview)
    }
  }, [filePreview])

  const stored = useAuthedImageUrl(channelLogoPath(channel.logo_image_id))
  const preview = filePreview ?? stored
  const hasStored = !!channel.logo_image_id

  const save = useMutation({
    mutationFn: async (picked: File) =>
      api.put<ChannelLogo>(`/channels/${channel.id}/logo`, { image_base64: await fileToBase64(picked) }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Logo saved.')
      setError(null)
      setFile(null)
      if (inputRef.current) inputRef.current.value = ''
    },
    onError: (err) =>
      setError(err instanceof ApiError ? `Could not save the logo (${err.message})` : 'Could not save the logo.'),
  })

  const remove = useMutation({
    mutationFn: () => api.delete(`/channels/${channel.id}/logo`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['channels'] })
      toast.success('Logo removed.')
      setError(null)
    },
    onError: () => toast.error('Could not remove the logo.'),
  })

  function pick(picked: File | null) {
    if (!picked) return
    const problem = validateChannelLogoFile(picked)
    if (problem) {
      setError(problem)
      return
    }
    setError(null)
    setFile(picked)
  }

  return (
    <section className="flex flex-col gap-4" data-testid="deco-logo">
      <h2 className="text-sm font-medium">Logo</h2>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="deco-logo-file">Logo image</FieldLabel>
          <div className="flex flex-wrap items-center gap-4">
            <div
              className="flex h-16 w-40 items-center justify-center rounded-md border border-border bg-muted/40 p-2"
              data-testid="deco-logo-preview"
            >
              {preview ? (
                <img src={preview} alt={`${channel.name} logo`} className="max-h-full max-w-full object-contain" />
              ) : (
                <span className="text-xs text-muted-foreground">No logo</span>
              )}
            </div>
            <input
              ref={inputRef}
              id="deco-logo-file"
              type="file"
              accept={CHANNEL_LOGO_TYPES.join(',')}
              className="text-sm"
              data-testid="deco-logo-file"
              onChange={(e) => pick(e.target.files?.[0] ?? null)}
            />
            <Button
              type="button"
              disabled={!file || save.isPending}
              data-testid="deco-logo-save"
              onClick={() => file && save.mutate(file)}
            >
              {hasStored && file ? 'Replace logo' : 'Save logo'}
            </Button>
            {hasStored && (
              <Button
                type="button"
                variant="outline"
                disabled={remove.isPending}
                data-testid="deco-logo-remove"
                onClick={() => remove.mutate()}
              >
                Remove
              </Button>
            )}
          </div>
          <FieldDescription data-testid="deco-logo-help">
            PNG, JPEG or WebP, at most 1 MiB. The client shows it in the guide and on the channel card; without one it
            shows the channel name instead.
          </FieldDescription>
          {error && (
            <p className="text-sm text-destructive" data-testid="deco-logo-error">
              {error}
            </p>
          )}
        </Field>
      </FieldGroup>
    </section>
  )
}
