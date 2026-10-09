import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { formatDuration } from '@/lib/criteria'
import { ApiError, api } from '@/lib/http'
import { draftToSlice, EMPTY_SLICE_DRAFT, sliceKey, sliceToDraft, type SliceDraft } from '@/lib/seriesSlice'
import type { SeriesItemView, SeriesSlice, SeriesSlicePreview } from '@/lib/types'

// Editor for the slice of a series block item: which of the series' episodes
// the item airs. Shows a live count of the resulting episodes (the backend's
// POST /series/slice-preview, the same code the materializer runs) and saves
// through `onSave` (an empty slice clears it). Remount it (change its key)
// after a save so the form follows the stored slice.
export function SeriesSliceControl({
  series,
  includeSpecials,
  disabled,
  onSave,
}: {
  series: SeriesItemView
  // The block's include_specials option: the count is the one the block airs.
  includeSpecials: boolean
  disabled?: boolean
  onSave: (slice: SeriesSlice) => void
}) {
  const [draft, setDraft] = useState<SliceDraft>(() => sliceToDraft(series.slice))
  const parsed = useMemo(() => draftToSlice(draft), [draft])
  const dirty = sliceKey(parsed.slice ?? null) !== sliceKey(series.slice ?? {})
  const [debounced, setDebounced] = useState(parsed.slice)
  useEffect(() => {
    const handle = setTimeout(() => setDebounced(parsed.slice), 250)
    return () => clearTimeout(handle)
  }, [parsed.slice])

  const preview = useQuery({
    queryKey: ['series-slice-preview', series.media_source_id, series.series_external_id, series.series_title, includeSpecials, debounced],
    enabled: debounced !== undefined,
    retry: false,
    queryFn: () =>
      api.post<SeriesSlicePreview>('/series/slice-preview', {
        media_source_id: series.media_source_id,
        series_external_id: series.series_external_id,
        series_title: series.series_title,
        slice: debounced,
        include_specials: includeSpecials,
      }),
  })

  const update = (patch: Partial<SliceDraft>) => setDraft((d) => ({ ...d, ...patch }))
  const num = (key: keyof SliceDraft, label: string, testId: string, min = 0) => (
    <label className="flex flex-col gap-1 text-xs text-muted-foreground">
      {label}
      <Input
        type="number"
        min={min}
        step={1}
        className="w-24"
        data-testid={`slice-${testId}`}
        value={draft[key] as string}
        disabled={disabled}
        onChange={(e) => update({ [key]: e.target.value } as Partial<SliceDraft>)}
      />
    </label>
  )

  const serverError = preview.error instanceof ApiError ? preview.error.message : null
  const stale = debounced !== parsed.slice || preview.isFetching
  const result = preview.data
  const canSave = !disabled && dirty && !parsed.error && !serverError && !!result && result.episode_count > 0 && !stale

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3" data-testid="series-slice">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Seasons (list)
          <Input
            className="w-32"
            placeholder="e.g. 1, 2"
            data-testid="slice-seasons"
            value={draft.seasons}
            disabled={disabled}
            onChange={(e) => update({ seasons: e.target.value })}
          />
        </label>
        {num('seasonFrom', 'Season from', 'season-from')}
        {num('seasonTo', 'Season to', 'season-to')}
        <label className="flex items-center gap-2 pb-2 text-sm">
          <Checkbox
            data-testid="slice-exclude-specials"
            checked={draft.excludeSpecials}
            disabled={disabled}
            onCheckedChange={(v) => update({ excludeSpecials: v === true })}
          />
          Exclude specials (season 0)
        </label>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        {num('episodeFrom', 'Episode from', 'episode-from')}
        {num('episodeTo', 'Episode to', 'episode-to')}
        {num('skipFirst', 'Skip first', 'skip-first', 1)}
        {num('skipLast', 'Skip last', 'skip-last', 1)}
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          Then keep
          <Select value={draft.keep} onValueChange={(v) => update({ keep: v as SliceDraft['keep'] })} disabled={disabled}>
            <SelectTrigger className="w-32" aria-label="Keep" data-testid="slice-keep">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All</SelectItem>
              <SelectItem value="first">The first</SelectItem>
              <SelectItem value="last">The last</SelectItem>
            </SelectContent>
          </Select>
        </label>
        {draft.keep !== 'all' && num('keepCount', 'Episodes', 'keep-count', 1)}
        {num('sample', 'Random sample', 'sample', 1)}
      </div>
      <p className="text-xs text-muted-foreground">
        Season and episode ranges are inclusive and go by real numbers. Skip, first, last and sample count files by position in the
        series' original order, and a multi-episode file counts once. They apply in the order shown above. Season 0 is left out
        unless the block includes specials or the slice names season 0 (a season list with 0, or a range starting or ending at 0).
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm" data-testid="slice-count" aria-live="polite">
          {parsed.error ? (
            <span className="text-destructive">{parsed.error}</span>
          ) : serverError ? (
            <span className="text-destructive">{serverError}</span>
          ) : result ? (
            <>
              <strong>{result.episode_count}</strong> of {result.series_episode_count} episode
              {result.series_episode_count === 1 ? '' : 's'}, {formatDuration(result.total_duration_ms)}
              {result.episode_count === 0 && <span className="text-destructive"> (matches nothing, cannot be saved)</span>}
            </>
          ) : (
            'Counting…'
          )}
        </span>
        <Button size="sm" data-testid="slice-save" disabled={!canSave} onClick={() => parsed.slice && onSave(parsed.slice)}>
          Save slice
        </Button>
        <Button
          size="sm"
          variant="outline"
          data-testid="slice-reset"
          disabled={disabled || (!series.slice && !dirty)}
          onClick={() => {
            setDraft(EMPTY_SLICE_DRAFT)
            if (series.slice) onSave({})
          }}
        >
          Clear slice
        </Button>
      </div>
    </div>
  )
}
