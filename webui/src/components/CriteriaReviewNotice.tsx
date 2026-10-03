import { InlineWarning } from '@/components/InlineWarning'
import { Button } from '@/components/ui/button'
import { toast } from 'sonner'
import { useCriteriaReview } from '@/lib/authoring'
import { formatCatalogTitle } from '@/lib/catalog'
import { useApplyReviewProposal } from '@/lib/collections'
import { ApiError } from '@/lib/http'
import { summarizeQuery } from '@/lib/query'
import type { CriteriaReviewRef } from '@/lib/types'

const LIST_LIMIT = 8

function titles(refs: CriteriaReviewRef[]): string {
  const shown = refs.slice(0, LIST_LIMIT).map((r) => formatCatalogTitle({ title: r.title, series_title: r.series_title }))
  return refs.length > LIST_LIMIT ? `${shown.join('; ')}; and ${refs.length - LIST_LIMIT} more` : shown.join('; ')
}

// Shown in place of the collection picker while a block, pool or criteria item
// is held for review: its old query (the old fixed criteria format, or a query
// that could not be moved into a collection unchanged) keeps working. The
// proposed query would match the same items except for the differences listed
// here, so the move is an explicit choice: apply the proposal (which creates a
// collection, or reuses an identical one), or pick an existing collection.
export function CriteriaReviewNotice({
  ownerType,
  ownerId,
  onChooseCollection,
}: {
  ownerType: 'block' | 'filler_pool' | 'block_item'
  ownerId: string
  onChooseCollection: () => void
}) {
  const { data } = useCriteriaReview()
  const apply = useApplyReviewProposal()
  const entry = data?.find((r) => r.owner_type === ownerType && r.owner_id === ownerId)
  const noun = ownerType === 'block' ? 'block' : ownerType === 'filler_pool' ? 'pool' : 'item'

  function applyProposal() {
    apply.mutate(
      { owner_type: ownerType, owner_id: ownerId },
      {
        onSuccess: (res) =>
          toast.success(res.created ? `Collection "${res.collection.name}" created.` : `Now using the collection "${res.collection.name}".`),
        onError: (err) => toast.error(err instanceof ApiError ? err.message : 'Could not apply the proposal.'),
      },
    )
  }

  return (
    <div className="flex flex-col gap-3" data-testid="criteria-review">
      <InlineWarning title={`This ${noun} is held for review`}>
        It keeps playing its old query as it always did. Moving it to a collection changes what it matches only as
        described below (genres are compared by exact name, ignoring case and accents, instead of as a piece of the name).
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
          <Button type="button" size="sm" data-testid="criteria-review-apply" disabled={apply.isPending} onClick={applyProposal}>
            Apply the proposal as a collection
          </Button>
        )}
        <Button type="button" size="sm" variant="outline" data-testid="criteria-review-new" onClick={onChooseCollection}>
          Choose another collection
        </Button>
      </div>
    </div>
  )
}
