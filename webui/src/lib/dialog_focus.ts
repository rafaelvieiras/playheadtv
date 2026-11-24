// Where focus should go when a dialog closes.
//
// Radix returns focus to its `DialogTrigger`, but most dialogs here are opened
// from a plain `<Button onClick>` (a table row cannot have a trigger without a
// trigger per row), so `triggerRef` is null and every close left
// `document.activeElement` on `<body>`: a keyboard user restarted at the top of
// the twelve-link sidebar after cancelling anything.
//
// Reading `document.activeElement` when the dialog renders is not enough — by
// then Radix has already moved focus into the content, so the capture records
// `<body>`. The element is tracked at the document instead: the last thing
// focused outside any dialog is, in practice, the control that opened it.
let lastFocusedOutsideDialog: HTMLElement | null = null

if (typeof document !== 'undefined') {
  document.addEventListener(
    'focusin',
    (event) => {
      const target = event.target as HTMLElement | null
      if (!target || target === document.body) return
      if (target.closest('[role="dialog"],[role="alertdialog"]')) return
      lastFocusedOutsideDialog = target
    },
    true,
  )
}

/** `onCloseAutoFocus` handler: give focus back to what opened the dialog. */
export function returnFocusToOpener(event: { preventDefault: () => void }) {
  const target = lastFocusedOutsideDialog
  if (!target || !target.isConnected) return
  event.preventDefault()
  // Radix's own FocusScope cleanup runs after this handler and would move focus
  // again; focusing on the next task puts this one last.
  setTimeout(() => {
    if (target.isConnected) target.focus()
  }, 0)
}
