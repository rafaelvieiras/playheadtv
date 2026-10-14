import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ApiError, api } from '@/lib/http'
import { useAuthStore } from '@/store/auth'
import {
  fileToBase64,
  validateWatermarkFile,
  validateWatermarkOptions,
  WATERMARK_IMAGE_TYPES,
  WATERMARK_MAX_SIZE,
  WATERMARK_MIN_SIZE,
  WATERMARK_MODE_LABELS,
  WATERMARK_POSITION_LABELS,
  type Watermark,
  type WatermarkMode,
  type WatermarkPosition,
} from '@/lib/watermark'

interface WatermarkSectionProps {
  ownerType: 'channel' | 'block'
  ownerId: string
}

// The stored image needs the session token, which an <img src> cannot send, so
// it is fetched as a blob.
function useStoredImage(imageId: string | undefined) {
  const token = useAuthStore((s) => s.token)
  const [url, setUrl] = useState<string | null>(null)
  useEffect(() => {
    if (!imageId) {
      setUrl(null)
      return
    }
    let revoked: string | null = null
    let cancelled = false
    fetch(`/watermarks/${imageId}/image`, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
      .then((r) => (r.ok ? r.blob() : null))
      .then((blob) => {
        if (!blob || cancelled) return
        revoked = URL.createObjectURL(blob)
        setUrl(revoked)
      })
      .catch(() => setUrl(null))
    return () => {
      cancelled = true
      if (revoked) URL.revokeObjectURL(revoked)
    }
  }, [imageId, token])
  return url
}

// A small, self-contained section (its own requests, like the schedule
// library): off by default, and the image and options only appear once the
// owner uses its own watermark. A channel is simply on or off; a block can also
// inherit the channel's or switch it off while it is on air.
export function WatermarkSection({ ownerType, ownerId }: WatermarkSectionProps) {
  const queryClient = useQueryClient()
  const path = `/${ownerType}s/${ownerId}/watermark`
  const prefix = `${ownerType}-watermark`
  const { data: stored } = useQuery({ queryKey: ['watermark', ownerType, ownerId], queryFn: () => api.get<Watermark>(path) })

  const [mode, setMode] = useState<WatermarkMode>(ownerType === 'block' ? 'inherit' : 'disable')
  const [position, setPosition] = useState<WatermarkPosition>('bottom_right')
  const [opacity, setOpacity] = useState(70)
  const [size, setSize] = useState(8)
  const [file, setFile] = useState<File | null>(null)
  const [filePreview, setFilePreview] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!stored) return
    setMode(stored.mode)
    setPosition(stored.position)
    setOpacity(stored.opacity_pct)
    setSize(stored.size_pct)
    setFile(null)
    setError(null)
  }, [stored])

  useEffect(() => {
    if (!file) {
      setFilePreview(null)
      return
    }
    const url = URL.createObjectURL(file)
    setFilePreview(url)
    return () => URL.revokeObjectURL(url)
  }, [file])

  const storedImage = useStoredImage(stored?.image_id)
  const preview = filePreview ?? storedImage
  const hasImage = file !== null || !!stored?.image_id

  const save = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { mode, position, opacity_pct: opacity, size_pct: size }
      if (file) body.image_base64 = await fileToBase64(file)
      return api.put<Watermark>(path, body)
    },
    onSuccess: (saved) => {
      queryClient.setQueryData(['watermark', ownerType, ownerId], saved)
      toast.success('Watermark saved.')
      setError(null)
    },
    onError: (err) =>
      setError(err instanceof ApiError ? `Could not save the watermark (${err.message})` : 'Could not save the watermark.'),
  })

  const clear = useMutation({
    mutationFn: () => api.delete(path),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['watermark', ownerType, ownerId] })
      toast.success('Watermark removed.')
    },
    onError: () => toast.error('Could not remove the watermark.'),
  })

  function submit() {
    const problem = validateWatermarkOptions(opacity, size)
    if (problem) return setError(problem)
    if (mode === 'override' && !hasImage) return setError('Choose an image first.')
    setError(null)
    save.mutate()
  }

  const own = mode === 'override'
  const configured = !!stored && (stored.image_id || stored.mode !== (ownerType === 'block' ? 'inherit' : 'disable'))

  return (
    <section className="flex flex-col gap-4" data-testid={prefix}>
      <h2 className="text-sm font-medium">Watermark</h2>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor={`${prefix}-mode`}>
            {ownerType === 'block' ? 'Watermark while this block is on air' : 'Show a watermark'}
          </FieldLabel>
          <Select value={mode} onValueChange={(v) => setMode(v as WatermarkMode)}>
            <SelectTrigger id={`${prefix}-mode`} className="w-56" data-testid={`${prefix}-mode`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ownerType === 'block' ? (
                (['inherit', 'disable', 'override'] as WatermarkMode[]).map((m) => (
                  <SelectItem key={m} value={m}>
                    {WATERMARK_MODE_LABELS[m]}
                  </SelectItem>
                ))
              ) : (
                <>
                  <SelectItem value="disable">Off</SelectItem>
                  <SelectItem value="override">On</SelectItem>
                </>
              )}
            </SelectContent>
          </Select>
          <FieldDescription>
            {ownerType === 'block'
              ? "Drawn by the player over the video; the channel's applies unless this block turns it off or uses its own. Filler that decorates the block follows it."
              : 'A small image drawn by the player over the video. A block can turn it off or use its own.'}
          </FieldDescription>
        </Field>

        {own && (
          <>
            <Field>
              <FieldLabel htmlFor={`${prefix}-image`}>Image (PNG, JPEG or WebP, up to 1 MiB)</FieldLabel>
              <Input
                id={`${prefix}-image`}
                data-testid={`${prefix}-image`}
                type="file"
                accept={WATERMARK_IMAGE_TYPES.join(',')}
                onChange={(e) => {
                  const f = e.target.files?.[0] ?? null
                  const problem = f ? validateWatermarkFile(f) : null
                  if (problem) {
                    setError(problem)
                    e.target.value = ''
                    return
                  }
                  setError(null)
                  setFile(f)
                }}
              />
              {preview && (
                <div className="mt-2 rounded-md bg-black p-3">
                  <img src={preview} alt="Watermark preview" className="max-h-24 object-contain" data-testid={`${prefix}-preview`} />
                </div>
              )}
            </Field>
            <Field>
              <FieldLabel htmlFor={`${prefix}-position`}>Corner</FieldLabel>
              <Select value={position} onValueChange={(v) => setPosition(v as WatermarkPosition)}>
                <SelectTrigger id={`${prefix}-position`} className="w-56" data-testid={`${prefix}-position`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(WATERMARK_POSITION_LABELS) as WatermarkPosition[]).map((p) => (
                    <SelectItem key={p} value={p}>
                      {WATERMARK_POSITION_LABELS[p]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor={`${prefix}-opacity`}>Opacity (%)</FieldLabel>
              <Input
                id={`${prefix}-opacity`}
                data-testid={`${prefix}-opacity`}
                type="number"
                min={0}
                max={100}
                className="w-28"
                value={Number.isNaN(opacity) ? '' : opacity}
                onChange={(e) => setOpacity(e.target.value === '' ? NaN : Number(e.target.value))}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={`${prefix}-size`}>Size (% of the video height)</FieldLabel>
              <Input
                id={`${prefix}-size`}
                data-testid={`${prefix}-size`}
                type="number"
                min={WATERMARK_MIN_SIZE}
                max={WATERMARK_MAX_SIZE}
                className="w-28"
                value={Number.isNaN(size) ? '' : size}
                onChange={(e) => setSize(e.target.value === '' ? NaN : Number(e.target.value))}
              />
            </Field>
          </>
        )}
      </FieldGroup>
      {error && (
        <p className="text-sm text-destructive" data-testid={`${prefix}-error`}>
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="button" data-testid={`${prefix}-save`} disabled={save.isPending} onClick={submit}>
          {save.isPending ? 'Saving…' : 'Save watermark'}
        </Button>
        {configured && (
          <Button
            type="button"
            variant="outline"
            data-testid={`${prefix}-clear`}
            disabled={clear.isPending}
            onClick={() => clear.mutate()}
          >
            Remove
          </Button>
        )}
      </div>
    </section>
  )
}
