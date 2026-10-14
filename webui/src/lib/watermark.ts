// Watermark settings of a channel or a block (docs/ARCHITECTURE.md
// "Watermark"). The image is sent inside the JSON body as base64.

export type WatermarkMode = 'inherit' | 'disable' | 'override'
export type WatermarkPosition = 'top_left' | 'top_right' | 'bottom_left' | 'bottom_right'

export interface Watermark {
  owner_type: 'channel' | 'block'
  owner_id: string
  mode: WatermarkMode
  image_id?: string
  content_type?: string
  position: WatermarkPosition
  opacity_pct: number
  size_pct: number
  updated_at: number
}

export const WATERMARK_POSITION_LABELS: Record<WatermarkPosition, string> = {
  top_left: 'Top left',
  top_right: 'Top right',
  bottom_left: 'Bottom left',
  bottom_right: 'Bottom right',
}

export const WATERMARK_MODE_LABELS: Record<WatermarkMode, string> = {
  inherit: "Inherit the channel's",
  disable: 'No watermark',
  override: 'Use its own',
}

export const WATERMARK_MAX_IMAGE_BYTES = 1 << 20
export const WATERMARK_MIN_SIZE = 2
export const WATERMARK_MAX_SIZE = 40
export const WATERMARK_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/webp']

// Returns a message when the file cannot be uploaded (the backend checks the
// content again; this only saves a round trip).
export function validateWatermarkFile(file: { type: string; size: number }): string | null {
  if (!WATERMARK_IMAGE_TYPES.includes(file.type)) return 'The image must be PNG, JPEG or WebP.'
  if (file.size > WATERMARK_MAX_IMAGE_BYTES) return 'The image must be at most 1 MiB.'
  return null
}

export function validateWatermarkOptions(opacity: number, size: number): string | null {
  if (!Number.isInteger(opacity) || opacity < 0 || opacity > 100) return 'Opacity must be between 0 and 100.'
  if (!Number.isInteger(size) || size < WATERMARK_MIN_SIZE || size > WATERMARK_MAX_SIZE) {
    return `Size must be between ${WATERMARK_MIN_SIZE} and ${WATERMARK_MAX_SIZE}.`
  }
  return null
}

export function fileToBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error)
    reader.onload = () => {
      const url = String(reader.result)
      resolve(url.slice(url.indexOf(',') + 1))
    }
    reader.readAsDataURL(file)
  })
}
