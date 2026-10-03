import { useEffect, useState } from 'react'
import { toast } from 'sonner'

import { CollectionUsageList } from '@/components/CollectionUsageList'
import { InlineWarning } from '@/components/InlineWarning'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
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
    <AlertDialog open={collection !== undefined} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent data-testid="delete-collection-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>Delete collection?</AlertDialogTitle>
          <AlertDialogDescription>
            This will permanently delete the collection "{collection?.name}". This action cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {usage.data && inUse && (
          <InlineWarning title="This collection is in use and cannot be deleted">
            Point these at another collection first:
            <CollectionUsageList usage={usage.data} testId="delete-collection-usage" />
          </InlineWarning>
        )}
        {error && (
          <p className="text-sm text-destructive" data-testid="delete-collection-error">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <Button variant="destructive" onClick={confirm} disabled={del.isPending} data-testid="delete-collection-confirm">
            Delete
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
