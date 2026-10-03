import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { LIST_MODE_LABELS, type ListMode } from '@/lib/types'

interface BlockListModeFieldProps {
  value: ListMode
  onChange: (value: ListMode) => void
  idPrefix: string
}

// What a manual block does when its item list ends: start again from the first
// item (until the next timed item, or indefinitely under a continuous item), or
// play the list once.
export function BlockListModeField({ value, onChange, idPrefix }: BlockListModeFieldProps) {
  return (
    <Field>
      <FieldLabel htmlFor={`${idPrefix}-list-mode`}>When the list ends</FieldLabel>
      <Select value={value} onValueChange={(v) => onChange(v as ListMode)}>
        <SelectTrigger id={`${idPrefix}-list-mode`} className="w-56" data-testid={`${idPrefix}-list-mode`}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {(Object.keys(LIST_MODE_LABELS) as ListMode[]).map((m) => (
            <SelectItem key={m} value={m}>
              {LIST_MODE_LABELS[m]}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <FieldDescription>
        {value === 'repeat'
          ? 'The list starts again from its first item until the next timed item, or indefinitely under a continuous item.'
          : 'The list airs once; what is left of the slot goes to the tail filler or stays empty.'}
      </FieldDescription>
    </Field>
  )
}
