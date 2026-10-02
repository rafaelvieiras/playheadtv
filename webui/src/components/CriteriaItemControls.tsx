import { useState } from 'react'

import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { CRITERIA_ORDER_LABELS, type CriteriaAmount, type CriteriaAmountMode, type CriteriaOrder } from '@/lib/types'

export function CriteriaOrderSelect({
  value,
  onChange,
  disabled,
  testId,
}: {
  value: CriteriaOrder
  onChange: (order: CriteriaOrder) => void
  disabled?: boolean
  testId: string
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as CriteriaOrder)} disabled={disabled}>
      <SelectTrigger className="w-48" aria-label="Playback order" data-testid={testId}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {(Object.keys(CRITERIA_ORDER_LABELS) as CriteriaOrder[]).map((o) => (
          <SelectItem key={o} value={o}>
            {CRITERIA_ORDER_LABELS[o]}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

const MODE_LABELS: Record<CriteriaAmountMode, string> = {
  count: 'A number of items',
  all: 'All matches',
}

// A positive integer, or null for anything else (empty, 0, fractions, text).
function parsedCount(text: string): number | null {
  const n = Number(text)
  return text.trim() !== '' && Number.isInteger(n) && n >= 1 ? n : null
}

// How many matches of the query one visit of the block rotation airs: N of
// them or all of them. Both are an explicit choice (there is no default), so
// the count starts empty for a new item.
//
// With `eager`, every edit is reported (null while the amount is incomplete),
// for a form that is submitted later. Without it, only a complete amount is
// reported: the mode on selection, the count on blur or Enter, for an item
// that is saved on each edit.
export function CriteriaAmountControl({
  value,
  onChange,
  eager,
  disabled,
  testIdPrefix,
}: {
  value: CriteriaAmount | null
  onChange: (amount: CriteriaAmount | null) => void
  eager?: boolean
  disabled?: boolean
  testIdPrefix: string
}) {
  const [mode, setMode] = useState<CriteriaAmountMode>(value?.mode ?? 'count')
  const [draft, setDraft] = useState(value?.mode === 'count' && value.count ? String(value.count) : '')
  const [error, setError] = useState<string | null>(null)
  const committed = value?.mode === 'count' ? String(value.count ?? '') : ''

  function changeMode(next: CriteriaAmountMode) {
    setMode(next)
    setError(null)
    if (next === 'all') {
      onChange({ mode: 'all' })
      return
    }
    const n = parsedCount(draft)
    if (n !== null) onChange({ mode: 'count', count: n })
    else if (eager) onChange(null)
  }

  function change(text: string) {
    setDraft(text)
    if (!eager) return
    const n = parsedCount(text)
    onChange(n === null ? null : { mode: 'count', count: n })
  }

  function commit() {
    if (eager) return
    if (draft === committed) return
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
        <Select value={mode} onValueChange={(v) => changeMode(v as CriteriaAmountMode)} disabled={disabled}>
          <SelectTrigger className="w-48" aria-label="Amount per visit" data-testid={`${testIdPrefix}-amount-mode`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(MODE_LABELS) as CriteriaAmountMode[]).map((m) => (
              <SelectItem key={m} value={m}>
                {MODE_LABELS[m]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {mode === 'count' && (
          <Input
            type="number"
            min={1}
            step={1}
            className="w-24"
            aria-label="Number of items"
            placeholder="N"
            data-testid={`${testIdPrefix}-amount-count`}
            value={draft}
            disabled={disabled}
            onChange={(e) => change(e.target.value)}
            onBlur={commit}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commit()
            }}
          />
        )}
      </div>
      {error && <span className="text-xs text-destructive">{error}</span>}
    </div>
  )
}
