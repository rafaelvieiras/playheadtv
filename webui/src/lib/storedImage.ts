import { useEffect, useState } from 'react'

import { useAuthStore } from '@/store/auth'

// An image the API serves only to an authenticated caller. An `<img src>` cannot
// send the session token, so the bytes are fetched here and handed back as an
// object URL. Two things worth knowing:
//
// - The URL is derived from the path it was fetched for, so a new path never
//   shows the previous image for a frame.
// - It is revoked when the path changes or the component goes away: a guide with
//   a logo per channel would otherwise leak one blob per row on every refresh.
export function useAuthedImageUrl(path: string | null): string | null {
  const token = useAuthStore((s) => s.token)
  const [fetched, setFetched] = useState<{ path: string; url: string } | null>(null)
  const url = fetched && fetched.path === path ? fetched.url : null

  useEffect(() => {
    if (!path) return
    let cancelled = false
    let created: string | null = null
    fetch(path, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
      .then((r) => (r.ok ? r.blob() : null))
      .then((blob) => {
        if (!blob || cancelled) return
        created = URL.createObjectURL(blob)
        setFetched({ path, url: created })
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
      if (created) URL.revokeObjectURL(created)
    }
  }, [path, token])

  return url
}
