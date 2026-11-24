import { useEffect } from 'react'
import { useBlocker } from 'react-router-dom'

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
import { Button } from '@/components/ui/button'

// The bar that appears while a form has unsaved edits. The editor owns the
// dirty computation (compare draft against the loaded value) and the save /
// discard handlers; this owns the affordance AND the two guards that keep the
// operator from losing the draft (docs/ARCHITECTURE.md "Admin shell"):
//
// - a fixed bar over the bottom edge, because `sticky bottom-*` only pins while
//   its natural position is already below the fold — six editors place the bar
//   mid-page, where it scrolled out of reach exactly when it was needed;
// - one `useBlocker` for in-app navigation and one `beforeunload` for a reload
//   or a closed tab, so no path discards the draft without asking.
export function SaveBar({
  dirty,
  saving,
  onSave,
  onDiscard,
  error,
}: {
  dirty: boolean
  saving?: boolean
  onSave: () => void
  onDiscard: () => void
  error?: string | null
}) {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty && currentLocation.pathname !== nextLocation.pathname,
  )

  useEffect(() => {
    if (!dirty) return
    function onBeforeUnload(event: BeforeUnloadEvent) {
      event.preventDefault()
      // Chrome and Safari need returnValue set; Firefox uses it as the string.
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])

  if (!dirty) return null

  return (
    <>
      {/* Reserves the bar's height in the page flow, so it never covers the
          last control of a form. */}
      <div className="h-20" aria-hidden="true" />
      <div
        data-testid="save-bar"
        className="fixed inset-x-0 bottom-0 z-30 border-t border-border bg-popover/95 px-4 py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] text-popover-foreground shadow-lg backdrop-blur md:left-56"
      >
        <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-2">
          <span className={error ? 'text-sm text-destructive' : 'text-sm text-muted-foreground'}>
            {error ?? 'You have unsaved changes.'}
          </span>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={onDiscard} disabled={saving}>
              Discard
            </Button>
            <Button size="sm" onClick={onSave} disabled={saving}>
              {saving ? 'Saving…' : 'Save changes'}
            </Button>
          </div>
        </div>
      </div>

      {/* Open state is the blocker's, so there is no second source of truth:
          cancelling (button, Esc or backdrop) releases the blocked navigation. */}
      <AlertDialog
        open={blocker.state === 'blocked'}
        onOpenChange={(open) => {
          if (!open) blocker.reset?.()
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Leave without saving?</AlertDialogTitle>
            <AlertDialogDescription>
              This form has unsaved changes. Leaving this page discards them.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => blocker.reset?.()}>Stay on this page</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => blocker.proceed?.()}>
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
