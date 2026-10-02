import { Button } from '@/components/ui/button'

// Sticky bar that appears while a form has unsaved edits. The editor owns the
// dirty computation (compare draft against the loaded value) and the save /
// discard handlers; this only renders the affordance.
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
  if (!dirty) return null
  return (
    <div
      data-testid="save-bar"
      className="sticky bottom-4 z-10 flex items-center justify-between gap-4 rounded-lg border border-border bg-popover px-4 py-2 text-popover-foreground shadow-md ring-1 ring-foreground/10"
    >
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
  )
}
