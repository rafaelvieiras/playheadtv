import type { ReactNode } from 'react'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { ApiError } from '@/lib/http'

// One confirm dialog for every destructive action, so they cannot drift apart
// the way they did: the row button that opened them was red and the button that
// actually deleted was the neutral primary (indistinguishable from Save), none of
// them said what it was deleting, none changed its label while the request ran,
// and a stray click on the backdrop dismissed the dialog.
//
// Conventions this encodes:
// - `title` names the target: 'Delete block "Trailers"?'.
// - `confirmLabel` carries the verb: 'Delete block', not 'OK'.
// - the action stays open while the request runs (pending label, server error
//   shown in place), so a refusal from the backend is read where the click was.
// - the backdrop does not dismiss it; Cancel, Escape and the action do.
export function ConfirmDialog({
  open,
  onClose,
  title,
  description,
  confirmLabel = 'Delete',
  pendingLabel,
  onConfirm,
  isPending = false,
  confirmDisabled = false,
  error,
  children,
  testId,
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  description?: ReactNode
  confirmLabel?: string
  /** Label while the request is in flight, e.g. 'Deleting…'. */
  pendingLabel?: string
  onConfirm: () => void
  isPending?: boolean
  /** Blocks the action without the pending label (e.g. the thing is in use). */
  confirmDisabled?: boolean
  /** Error of the mutation: an ApiError's message is shown verbatim. */
  error?: unknown
  /** Extra body above the actions (a usage warning, a file name, …). */
  children?: ReactNode
  testId?: string
}) {
  const message =
    error instanceof ApiError ? error.message : error instanceof Error ? error.message : undefined

  return (
    <AlertDialog open={open} onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent data-testid={testId}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription>{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        {children}
        {message && (
          <p role="alert" className="text-sm text-destructive" data-testid="confirm-error">
            {message}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isPending}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={(event) => {
              event.preventDefault()
              onConfirm()
            }}
            disabled={isPending || confirmDisabled}
            data-testid={testId ? `${testId}-confirm` : undefined}
          >
            {isPending ? (pendingLabel ?? `${confirmLabel}…`) : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
