import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Plus } from 'lucide-react'

import { CollectionDialog } from '@/components/CollectionDialog'
import { Button } from '@/components/ui/button'
import { QueryError } from '@/components/QueryError'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCollections } from '@/lib/collections'
import { summarizeQuery } from '@/lib/query'

// Chooses the collection a dynamic block, filler pool or criteria item plays.
// The query itself is never edited here (that is the collection's own editor,
// where its users are shown first): the picker only shows what the chosen one
// matches by definition and how widely it is shared. "New collection" creates
// one in a dialog and selects it.
export function CollectionPicker({
  value,
  onChange,
  idPrefix,
  disabled,
}: {
  value: string // '' while nothing is chosen
  onChange: (id: string) => void
  idPrefix: string
  disabled?: boolean
}) {
  const { data: collections, isLoading, isError, error, refetch } = useCollections()
  const [creating, setCreating] = useState(false)
  const selected = collections?.find((c) => c.id === value)
  // A list that never arrived leaves nothing to choose from: an empty select
  // would claim there are no collections, which is an answer the app does not
  // have. Only a failed refresh with an earlier answer on screen keeps it.
  const listMissing = isError && collections === undefined

  return (
    <div className="flex flex-col gap-2" data-testid={`${idPrefix}-picker`}>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
        {listMissing ? (
          <div className="sm:flex-1">
            <QueryError
              subject="the collections"
              error={error}
              onRetry={() => void refetch()}
              testId={`${idPrefix}-collections-error`}
            />
          </div>
        ) : (
          <Select
            value={value}
            // Radix reports '' when the selected id has no option yet (a
            // collection created a moment ago, before the list refetches):
            // ignore it rather than clearing the choice.
            onValueChange={(v) => v && onChange(v)}
            disabled={disabled || isLoading}
          >
            <SelectTrigger className="w-full sm:w-80" aria-label="Collection" data-testid={`${idPrefix}-select`}>
              <SelectValue placeholder={isLoading ? 'Loading…' : 'Choose a collection'} />
            </SelectTrigger>
            <SelectContent>
              {(collections ?? []).map((c) => (
                <SelectItem key={c.id} value={c.id} data-testid={`${idPrefix}-option`}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={disabled}
          data-testid={`${idPrefix}-new`}
          onClick={() => setCreating(true)}
        >
          <Plus className="size-4" />
          New collection
        </Button>
      </div>
      {isError && !listMissing && (
        <QueryError
          subject="the collections"
          error={error}
          onRetry={() => void refetch()}
          stale
          testId={`${idPrefix}-collections-error`}
        />
      )}
      {!isLoading && !isError && (collections ?? []).length === 0 && (
        <p className="text-sm text-muted-foreground">There are no collections yet. Create one to continue.</p>
      )}
      {selected && (
        <div className="flex flex-col gap-1 text-sm" data-testid={`${idPrefix}-summary`}>
          <span className="text-muted-foreground">Query: {summarizeQuery(selected.query)}</span>
          <span className="text-muted-foreground">
            Used by {selected.usage_count} {selected.usage_count === 1 ? 'user' : 'users'} (blocks, pools and items).
            Editing the collection changes all of them.{' '}
            <Link className="underline" to={`/collections/${selected.id}`} data-testid={`${idPrefix}-edit-link`}>
              Edit this collection
            </Link>
          </span>
        </div>
      )}
      <CollectionDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(c) => {
          onChange(c.id)
        }}
      />
    </div>
  )
}
