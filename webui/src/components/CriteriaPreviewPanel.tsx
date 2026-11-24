import { formatCatalogTitle } from '@/lib/catalog'
import { useEffect, useRef } from 'react'

import { InlineWarning } from '@/components/InlineWarning'
import { QueryError } from '@/components/QueryError'
import { usePreviewCriteria } from '@/lib/authoring'
import { formatDurationMs } from '@/lib/filler'
import type { Query } from '@/lib/query'

const DEBOUNCE_MS = 400

// Live match preview for a draft criteria object: debounced POST to
// /catalog-items/preview-criteria. `criteria` must already be valid (the
// caller passes the last valid one while its own validation fails); null shows
// nothing.
export function CriteriaPreviewPanel({
  criteria,
  headingLevel = 3,
}: {
  criteria: Query | null
  // The page it sits on decides: a preview inside a page's own sections is a
  // section (h2), one nested in a dialog keeps the builder's level.
  headingLevel?: 2 | 3
}) {
  const preview = usePreviewCriteria()
  const mutate = useRef(preview.mutate)
  mutate.current = preview.mutate
  const reset = useRef(preview.reset)
  reset.current = preview.reset
  const key = criteria ? JSON.stringify(criteria) : ''

  useEffect(() => {
    if (!key) {
      reset.current()
      return
    }
    const handle = setTimeout(() => mutate.current(JSON.parse(key) as Query), DEBOUNCE_MS)
    return () => clearTimeout(handle)
  }, [key])

  if (!criteria) return null
  const data = preview.data
  const Heading = headingLevel === 2 ? 'h2' : 'h3'

  return (
    <div className="flex flex-col gap-3" data-testid="criteria-preview">
      <Heading className="text-sm font-medium">Preview</Heading>
      {preview.isPending && <p className="text-sm text-muted-foreground">Checking the catalog…</p>}
      {preview.isError && (
        <QueryError
          subject="the preview of this query"
          error={preview.error}
          onRetry={() => mutate.current(JSON.parse(key) as Query)}
          stale={data !== undefined}
          testId="criteria-preview-error"
        />
      )}
      {data && data.match_count === 0 && (
        <InlineWarning title="No catalog items match this query">
          A dynamic block or pool with no matches stays empty.
        </InlineWarning>
      )}
      {data && data.match_count > 0 && (
        <>
          <p className="text-sm" data-testid="criteria-preview-summary">
            {data.match_count} matching {data.match_count === 1 ? 'item' : 'items'},{' '}
            {formatDurationMs(data.total_duration_ms)} in total
          </p>
          <ul className="flex flex-col gap-1 text-sm" data-testid="criteria-preview-sample">
            {data.sample.map((item) => (
              <li key={item.id} className="flex justify-between gap-2 rounded-md border border-border px-2 py-1">
                <span className="truncate">{formatCatalogTitle(item)}</span>
                <span className="shrink-0 text-muted-foreground">{formatDurationMs(item.duration_ms)}</span>
              </li>
            ))}
          </ul>
          {data.match_count > data.sample.length && (
            <p className="text-xs text-muted-foreground">Showing the first {data.sample.length} by title.</p>
          )}
        </>
      )}
    </div>
  )
}
