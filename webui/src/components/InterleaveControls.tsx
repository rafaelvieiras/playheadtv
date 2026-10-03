import { useState } from 'react'

import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { parsePerRound, type InterleaveDraft } from '@/lib/interleave'
import {
  ON_EXHAUSTION_HELP,
  ON_EXHAUSTION_LABELS,
  SERIES_ORDER_LABELS,
  type OnExhaustion,
  type SeriesOrder,
} from '@/lib/types'

interface InterleaveControlsProps {
  value: InterleaveDraft
  onChange: (value: InterleaveDraft) => void
  // Report every edit (per_round null while invalid), for a form submitted
  // later. Without it only a complete value is reported: selects at once, the
  // count on blur or Enter, for an item that is saved on each edit.
  eager?: boolean
  disabled?: boolean
  testIdPrefix: string
}

// The three parameters of the interleaved order: episodes per series per
// round, how the series are sequenced within a round, and what happens when a
// series runs out of episodes.
export function InterleaveControls({ value, onChange, eager, disabled, testIdPrefix }: InterleaveControlsProps) {
  const [draft, setDraft] = useState(value.per_round === null ? '' : String(value.per_round))
  const [error, setError] = useState<string | null>(null)
  const committed = value.per_round === null ? '' : String(value.per_round)

  function changeCount(text: string) {
    setDraft(text)
    if (eager) onChange({ ...value, per_round: parsePerRound(text) })
  }

  function commitCount() {
    if (eager || draft === committed) return
    const n = parsePerRound(draft)
    if (n === null) {
      setError('Enter a whole number, 1 or more.')
      return
    }
    setError(null)
    onChange({ ...value, per_round: n })
  }

  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <label className="flex items-center gap-2 text-sm">
          <span className="whitespace-nowrap">Episodes per series per round</span>
          <Input
            type="number"
            min={1}
            step={1}
            className="w-20"
            aria-label="Episodes per series per round"
            data-testid={`${testIdPrefix}-per-round`}
            value={draft}
            disabled={disabled}
            onChange={(e) => changeCount(e.target.value)}
            onBlur={commitCount}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitCount()
            }}
          />
        </label>
        <Select
          value={value.series_order}
          onValueChange={(v) => onChange({ ...value, series_order: v as SeriesOrder })}
          disabled={disabled}
        >
          <SelectTrigger className="w-48" aria-label="Series order within a round" data-testid={`${testIdPrefix}-series-order`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(SERIES_ORDER_LABELS) as SeriesOrder[]).map((o) => (
              <SelectItem key={o} value={o}>
                {SERIES_ORDER_LABELS[o]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={value.on_exhaustion}
          onValueChange={(v) => onChange({ ...value, on_exhaustion: v as OnExhaustion })}
          disabled={disabled}
        >
          <SelectTrigger className="w-56" aria-label="When a series runs out of episodes" data-testid={`${testIdPrefix}-on-exhaustion`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(ON_EXHAUSTION_LABELS) as OnExhaustion[]).map((o) => (
              <SelectItem key={o} value={o}>
                {ON_EXHAUSTION_LABELS[o]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <span className="text-xs text-muted-foreground" data-testid={`${testIdPrefix}-on-exhaustion-help`}>
        {ON_EXHAUSTION_HELP[value.on_exhaustion]}
      </span>
      {error && <span className="text-xs text-destructive">{error}</span>}
    </div>
  )
}
