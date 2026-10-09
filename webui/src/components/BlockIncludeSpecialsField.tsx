import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldDescription } from '@/components/ui/field'

interface BlockIncludeSpecialsFieldProps {
  value: boolean
  onChange: (value: boolean) => void
  idPrefix: string
}

// Whether the episodes of season 0 (specials, extras, short clips) of a series
// can air from this block. Off by default; it applies to everything season-based
// the block selects: a dynamic block's matches, criteria items and series items.
export function BlockIncludeSpecialsField({ value, onChange, idPrefix }: BlockIncludeSpecialsFieldProps) {
  return (
    <Field>
      <label className="flex items-center gap-2 text-sm font-medium" htmlFor={`${idPrefix}-include-specials`}>
        <Checkbox
          id={`${idPrefix}-include-specials`}
          data-testid={`${idPrefix}-include-specials`}
          checked={value}
          onCheckedChange={(v) => onChange(v === true)}
        />
        Include season 0 (specials)
      </label>
      <FieldDescription>
        Off, the specials of a series never air from this block, whatever picks its episodes. A series item whose slice
        names season 0 still airs them. Movies and episodes without a season number are not affected.
      </FieldDescription>
    </Field>
  )
}
