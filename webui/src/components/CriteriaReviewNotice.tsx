import { InlineWarning } from '@/components/InlineWarning'
import { Button } from '@/components/ui/button'
import { useCriteriaReview } from '@/lib/authoring'
import { formatCatalogTitle } from '@/lib/catalog'
import { summarizeQuery, type Query } from '@/lib/query'
import type { CriteriaReviewRef } from '@/lib/types'

const LIST_LIMIT = 8

function titles(refs: CriteriaReviewRef[]): string {
  const shown = refs.slice(0, LIST_LIMIT).map((r) => formatCatalogTitle({ title: r.title, series_title: r.series_title }))
  return refs.length > LIST_LIMIT ? `${shown.join('; ')}; and ${refs.length - LIST_LIMIT} more` : shown.join('; ')
}

// Shown in place of the query builder while a block or pool still holds the
// old fixed criteria (genres as substrings, years). The old criteria keeps
// working; the proposed query would match exactly the same items except for the
// differences listed here, so the switch is an explicit choice: apply the
// proposal, or write a query from scratch.
export function CriteriaReviewNotice({
  ownerType,
  ownerId,
  onUseProposed,
  onWriteNew,
}: {
  ownerType: 'block' | 'filler_pool'
  ownerId: string
  onUseProposed: (query: Query) => void
  onWriteNew: () => void
}) {
  const { data } = useCriteriaReview()
  const entry = data?.find((r) => r.owner_type === ownerType && r.owner_id === ownerId)
  const noun = ownerType === 'block' ? 'block' : 'pool'

  return (
    <div className="flex flex-col gap-3" data-testid="criteria-review">
      <InlineWarning title={`This ${noun} still uses the old criteria format`}>
        It keeps working as it always did. Switching to a query changes how genres match: they are compared by exact name
        (ignoring case and accents) instead of as a piece of the name.
      </InlineWarning>
      {entry && (
        <div className="flex flex-col gap-2 text-sm">
          <p data-testid="criteria-review-counts">
            Matches {entry.before_count} item{entry.before_count === 1 ? '' : 's'} today. The proposed query would match{' '}
            {entry.after_count}.
          </p>
          {entry.proposed_query && (
            <p className="text-muted-foreground" data-testid="criteria-review-proposal">
              Proposed query: {summarizeQuery(entry.proposed_query)}
            </p>
          )}
          {entry.problem && <p className="text-destructive">{entry.problem}</p>}
          {entry.only_before.length > 0 && (
            <p data-testid="criteria-review-dropped">
              <span className="font-medium">Would stop matching:</span> {titles(entry.only_before)}
            </p>
          )}
          {entry.only_after.length > 0 && (
            <p data-testid="criteria-review-added">
              <span className="font-medium">Would start matching:</span> {titles(entry.only_after)}
            </p>
          )}
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {entry?.proposed_query && (
          <Button type="button" size="sm" data-testid="criteria-review-apply" onClick={() => onUseProposed(entry.proposed_query as Query)}>
            Use the proposed query
          </Button>
        )}
        <Button type="button" size="sm" variant="outline" data-testid="criteria-review-new" onClick={onWriteNew}>
          Write a new query
        </Button>
      </div>
    </div>
  )
}
