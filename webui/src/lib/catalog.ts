import type { CatalogItem } from '@/lib/types'

// The one place a catalog item's display name is built: episodes lead with
// their series ("Series — Episode"); everything else is just the title.
export function formatCatalogTitle(item: Pick<CatalogItem, 'title' | 'series_title'>): string {
  const series = item.series_title?.trim()
  return series ? `${series} — ${item.title}` : item.title
}
