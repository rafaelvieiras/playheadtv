import { InterleaveControls } from '@/components/InterleaveControls'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { InterleaveDraft } from '@/lib/interleave'
import { BLOCK_PLAYBACK_ORDER_LABELS, type BlockPlaybackOrder } from '@/lib/types'

export interface BlockPlaybackDraft {
  order: BlockPlaybackOrder
  interleave: InterleaveDraft
}

interface BlockPlaybackFieldsProps {
  value: BlockPlaybackDraft
  onChange: (value: BlockPlaybackDraft) => void
  idPrefix: string
}

// Playback order of a dynamic block: every match by title, looped (the
// default), or round-robin across series with its parameters.
export function BlockPlaybackFields({ value, onChange, idPrefix }: BlockPlaybackFieldsProps) {
  return (
    <Field>
      <FieldLabel htmlFor={`${idPrefix}-playback-order`}>Playback order</FieldLabel>
      <Select value={value.order} onValueChange={(v) => onChange({ ...value, order: v as BlockPlaybackOrder })}>
        <SelectTrigger id={`${idPrefix}-playback-order`} className="w-56" data-testid={`${idPrefix}-playback-order`}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {(Object.keys(BLOCK_PLAYBACK_ORDER_LABELS) as BlockPlaybackOrder[]).map((o) => (
            <SelectItem key={o} value={o}>
              {BLOCK_PLAYBACK_ORDER_LABELS[o]}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {value.order === 'interleaved' ? (
        <>
          <InterleaveControls
            value={value.interleave}
            onChange={(interleave) => onChange({ ...value, interleave })}
            eager
            testIdPrefix={`${idPrefix}-interleave`}
          />
          <FieldDescription>
            Each series airs its episodes in order, taking turns: the number above per series per round. Movies and other
            items without a series count as a one-item series. A series that runs out of episodes either restarts on its own
            and keeps its turn, or leaves the rounds for good while the others continue.
          </FieldDescription>
        </>
      ) : (
        <FieldDescription>All matches by title, looped.</FieldDescription>
      )}
    </Field>
  )
}
