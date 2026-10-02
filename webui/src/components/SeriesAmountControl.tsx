import { useState } from 'react'

import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SERIES_AMOUNT_LABELS, type SeriesAmount, type SeriesAmountMode } from '@/lib/types'

// How much of a series one visit of the block rotation airs: a mode selector
// plus, for "Number of episodes", a positive integer (default 1). Controlled by
// `value`; every valid change is reported through `onChange`. The count is
// committed on blur/Enter, the mode on selection.
export function SeriesAmountControl({
  value,
  onChange,
  disabled,
  testIdPrefix,
}: {
  value: SeriesAmount
  onChange: (amount: SeriesAmount) => void
  disabled?: boolean
  testIdPrefix: string
}) {
  const current = value.mode === 'count' ? String(value.count ?? 1) : '1'
  const [draft, setDraft] = useState(current)
  const [error, setError] = useState<string | null>(null)

  function changeMode(mode: SeriesAmountMode) {
    setError(null)
    if (mode === 'count') onChange({ mode, count: parsedCount(draft) ?? 1 })
    else onChange({ mode })
  }

  function commitCount() {
    if (draft === current) return
    const n = parsedCount(draft)
    if (n === null) {
      setError('Enter a whole number, 1 or more.')
      return
    }
    setError(null)
    onChange({ mode: 'count', count: n })
  }

  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-col gap-2 sm:flex-row">
        <Select value={value.mode} onValueChange={(v) => changeMode(v as SeriesAmountMode)} disabled={disabled}>
          <SelectTrigger className="w-52" aria-label="Amount per visit" data-testid={`${testIdPrefix}-amount-mode`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(SERIES_AMOUNT_LABELS) as SeriesAmountMode[]).map((m) => (
              <SelectItem key={m} value={m}>
                {SERIES_AMOUNT_LABELS[m]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {value.mode === 'count' && (
          <Input
            type="number"
            min={1}
            step={1}
            className="w-24"
            aria-label="Number of episodes"
            data-testid={`${testIdPrefix}-amount-count`}
            value={draft}
            disabled={disabled}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={commitCount}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitCount()
            }}
          />
        )}
      </div>
      {error && <span className="text-xs text-destructive">{error}</span>}
    </div>
  )
}

// A positive integer, or null for anything else (empty, 0, fractions, text).
function parsedCount(text: string): number | null {
  const n = Number(text)
  return text.trim() !== '' && Number.isInteger(n) && n >= 1 ? n : null
}
