import { useEffect, useState } from 'react'
import { toast } from 'sonner'

import { CollectionUsageList } from '@/components/CollectionUsageList'
import { InlineWarning } from '@/components/InlineWarning'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { useCollectionUsage, useDeleteCollection, usageCount } from '@/lib/collections'
import { ApiError } from '@/lib/http'
import type { Collection } from '@/lib/types'

// Confirms deleting a collection. A collection that is in use cannot be deleted
// (the server answers 409): the users are listed up front, and if the delete
// is tried anyway the server's message is shown here and the dialog stays open.
export function DeleteCollectionDialog({
  collection,
  onClose,
  onDeleted,
}: {
  collection: Collection | undefined
  onClose: () => void
  onDeleted?: () => void
}) {
  const usage = useCollectionUsage(collection?.id, collection !== undefined)
  const del = useDeleteCollection()
  const [error, setError] = useState<string | undefined>(undefined)
  const inUse = usageCount(usage.data) > 0

  useEffect(() => {
    setError(undefined)
  }, [collection?.id])

  function confirm() {
    if (!collection) return
    del.mutate(collection.id, {
      onSuccess: () => {
        toast.success('Collection deleted.')
        onClose()
        onDeleted?.()
      },
      onError: (err) => {
        setError(err instanceof ApiError ? err.message : 'Could not delete the collection.')
        usage.refetch()
      },
    })
  }

  return (
    <ConfirmDialog
      open={collection !== undefined}
      onClose={onClose}
      title={`Delete collection "${collection?.name}"?`}
      description="This action cannot be undone."
      confirmLabel="Delete collection"
      pendingLabel="Deleting…"
      onConfirm={confirm}
      isPending={del.isPending}
      // The button is really disabled while the collection is in use: it used
      // to look available and only the 409 explained why it was not.
      confirmDisabled={inUse}
      error={error}
      testId="delete-collection-dialog"
    >
      {usage.isError && (
        <InlineWarning title="Could not check who uses this collection">
          The delete may still be refused by the backend. Retry by opening this dialog again.
        </InlineWarning>
      )}
      {usage.data && inUse && (
        <InlineWarning title="This collection is in use and cannot be deleted">
          Point these at another collection first:
          <CollectionUsageList usage={usage.data} testId="delete-collection-usage" />
        </InlineWarning>
      )}
    </ConfirmDialog>
  )
}
