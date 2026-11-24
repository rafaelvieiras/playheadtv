import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { toast } from 'sonner'

import { CollectionFields, type CollectionDraft } from '@/components/CollectionFields'
import { CollectionUsageList } from '@/components/CollectionUsageList'
import { DeleteCollectionDialog } from '@/components/DeleteCollectionDialog'
import { InlineWarning } from '@/components/InlineWarning'
import { PageHeader } from '@/components/PageHeader'
import { Loading, QueryError } from '@/components/QueryError'
import { SaveBar } from '@/components/SaveBar'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { useCollection, useCollectionUsage, useUpdateCollection, usageCount } from '@/lib/collections'
import { ApiError } from '@/lib/http'
import { queryKey, validateQuery } from '@/lib/query'
import type { Collection } from '@/lib/types'

export function CollectionEditorPage() {
  const { id = '' } = useParams()
  const { data: collection, isLoading, error, refetch } = useCollection(id)
  const breadcrumb = [{ label: 'Collections', to: '/collections' }, { label: collection?.name ?? 'Collection' }]

  if (error instanceof ApiError && error.status === 404) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Collection not found" breadcrumb={breadcrumb} />
        <p className="text-sm text-muted-foreground">This collection does not exist (it may have been deleted).</p>
      </div>
    )
  }
  if (isLoading || !collection) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Collection" breadcrumb={breadcrumb} />
        {error ? (
          <QueryError subject="the collection" error={error} onRetry={() => void refetch()} />
        ) : (
          <Loading />
        )}
      </div>
    )
  }
  // Remounted after a save (updated_at changes) so the draft resets.
  return <CollectionEditor key={`${collection.id}-${collection.updated_at}`} collection={collection} breadcrumb={breadcrumb} />
}

function toDraft(c: Collection): CollectionDraft {
  return { name: c.name, description: c.description ?? '', query: c.query }
}

function CollectionEditor({ collection, breadcrumb }: { collection: Collection; breadcrumb: { label: string; to?: string }[] }) {
  const navigate = useNavigate()
  const loaded = toDraft(collection)
  const [draft, setDraft] = useState<CollectionDraft>(loaded)
  const [nameError, setNameError] = useState<string | undefined>(undefined)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const update = useUpdateCollection(collection.id)
  const usage = useCollectionUsage(collection.id)
  const users = usageCount(usage.data)

  const queryChanged = queryKey(draft.query) !== queryKey(loaded.query)
  const dirty = draft.name !== loaded.name || draft.description !== loaded.description || queryChanged

  function save() {
    // Both problems at once, each one reported under its own field: the form
    // used to answer "Name is required." only, and fixing it revealed the query.
    const missingName = draft.name.trim() === '' ? 'Name is required.' : undefined
    const badQuery = validateQuery(draft.query)
    setNameError(missingName)
    setError(badQuery ?? null)
    if (missingName || badQuery) return
    // A changed query applies to everything that uses the collection: show who
    // that is and ask first.
    if (queryChanged && users > 0) {
      setConfirming(true)
      return
    }
    doSave()
  }

  function doSave() {
    update.mutate(
      { name: draft.name.trim(), description: draft.description.trim() || null, query: draft.query },
      {
        onSuccess: () => {
          setConfirming(false)
          toast.success('Collection saved.')
        },
        onError: (err) => {
          setConfirming(false)
          setError(err instanceof ApiError ? err.message : 'Could not save the collection.')
        },
      },
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {collection.name}
            <Badge variant="secondary" data-testid="collection-users-badge">
              {/* A failed usage request must not claim "Unused": that is an
                  answer, and the app does not have one. */}
              {usage.isError
                ? 'Usage unknown'
                : users === 0
                  ? 'Unused'
                  : `${users} ${users === 1 ? 'user' : 'users'}`}
            </Badge>
          </span>
        }
        breadcrumb={breadcrumb}
        actions={
          <Button variant="destructive" size="sm" data-testid="collection-editor-delete" onClick={() => setDeleting(true)}>
            Delete
          </Button>
        }
      />

      <FieldGroup>
        <CollectionFields
          value={draft}
          onChange={(d) => {
            setDraft(d)
            setNameError(undefined)
            setError(null)
          }}
          idPrefix="collection"
          nameError={nameError}
        />
      </FieldGroup>

      <section className="flex flex-col gap-2 rounded-lg border border-border p-4" data-testid="collection-usage-panel">
        <h2 className="text-sm font-medium">Used by</h2>
        {usage.isError ? (
          <QueryError
            subject="who uses this collection"
            error={usage.error}
            onRetry={() => void usage.refetch()}
            stale={usage.data !== undefined}
            testId="collection-usage-error"
          />
        ) : usage.data ? (
          <CollectionUsageList usage={usage.data} />
        ) : (
          <Loading />
        )}
        {users > 0 && (
          <p className="text-xs text-muted-foreground">
            A change to the query applies to all of them at once.
          </p>
        )}
      </section>

      <SaveBar
        dirty={dirty}
        saving={update.isPending}
        error={error}
        onSave={save}
        onDiscard={() => {
          setDraft(loaded)
          setNameError(undefined)
          setError(null)
        }}
      />

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent data-testid="collection-save-confirm">
          <AlertDialogHeader>
            <AlertDialogTitle>Change a shared collection?</AlertDialogTitle>
            <AlertDialogDescription>
              Saving the new query changes what every one of these plays. Dynamic blocks and pools refill right away.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {usage.data && (
            <InlineWarning title={`${users} ${users === 1 ? 'user' : 'users'} of this collection`}>
              <CollectionUsageList usage={usage.data} testId="collection-confirm-usage" />
            </InlineWarning>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button onClick={doSave} disabled={update.isPending} data-testid="collection-save-confirm-button">
              Save for all
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <DeleteCollectionDialog
        collection={deleting ? collection : undefined}
        onClose={() => setDeleting(false)}
        onDeleted={() => navigate('/collections')}
      />
    </div>
  )
}
