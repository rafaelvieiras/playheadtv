import type { CatalogItem } from '@/lib/types'

type TitleFields = Pick<CatalogItem, 'title' | 'series_title'> &
  Partial<Pick<CatalogItem, 'season_number' | 'episode_number' | 'episode_number_end'>>

const pad2 = (n: number) => String(n).padStart(2, '0')

// "S01E03" (or "S01E03-E04" for a multi-episode file), only when the item
// carries an episode number. A missing season alone yields "E03"; season 0
// is specials ("S00E01"). Returns '' when there is nothing to show.
export function formatEpisodeLabel(item: TitleFields): string {
  const ep = item.episode_number
  if (ep == null) return ''
  const season = item.season_number
  let label = season != null ? `S${pad2(season)}E${pad2(ep)}` : `E${pad2(ep)}`
  const end = item.episode_number_end
  if (end != null && end > ep) label += `-E${pad2(end)}`
  return label
}

// The one place a catalog item's display name is built: episodes lead with
// their series ("Series S01E03 — Episode", the label only when numbers
// exist); everything else is just the title.
export function formatCatalogTitle(item: TitleFields): string {
  const series = item.series_title?.trim()
  if (!series) return item.title
  const label = formatEpisodeLabel(item)
  return `${series}${label ? ` ${label}` : ''} — ${item.title}`
}
