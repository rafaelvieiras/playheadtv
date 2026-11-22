// Channel logos: one small image per channel, drawn by the playback client in
// its guide and by this admin UI in the channel lists. The rules mirror the
// backend's (`backend/internal/api/channel_logo.go`), which checks the content
// and size again after the upload.

export const CHANNEL_LOGO_MAX_BYTES = 1 << 20
export const CHANNEL_LOGO_TYPES = ['image/png', 'image/jpeg', 'image/webp']

/** Returns a message when the file cannot be uploaded (the backend checks the content again; this only saves a round trip). */
export function validateChannelLogoFile(file: { type: string; size: number }): string | null {
  if (!CHANNEL_LOGO_TYPES.includes(file.type)) return 'The logo must be PNG, JPEG or WebP.'
  if (file.size > CHANNEL_LOGO_MAX_BYTES) return 'The logo must be at most 1 MiB.'
  return null
}

/**
 * Where a channel's logo is served from, or null when it has none. The route
 * needs the caller's token, so this is a path for an authenticated fetch, never
 * something to put straight into an `<img src>`.
 */
export function channelLogoPath(imageId: string | null | undefined): string | null {
  if (!imageId) return null
  return `/channel-logos/${encodeURIComponent(imageId)}/image`
}
