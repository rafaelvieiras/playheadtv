import { QueryBuilder } from '@/components/QueryBuilder'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Switch } from '@/components/ui/switch'
import { emptyQuery, type Query } from '@/lib/query'

interface DynamicCriteriaFieldsProps {
  value: Query | null
  onChange: (value: Query | null) => void
  idPrefix?: string
}

// The "dynamic (automatic criteria)" switch plus, while it is on, the query
// builder: the block or pool is then filled from the catalog by that query.
export function DynamicCriteriaFields({ value, onChange, idPrefix = 'criteria' }: DynamicCriteriaFieldsProps) {
  const enabled = value !== null

  return (
    <>
      <Field orientation="horizontal">
        <FieldLabel htmlFor={`${idPrefix}-dynamic`}>Dynamic (automatic criteria)</FieldLabel>
        <Switch
          id={`${idPrefix}-dynamic`}
          checked={enabled}
          onCheckedChange={(checked) => onChange(checked ? (value ?? emptyQuery()) : null)}
        />
      </Field>

      {enabled && value && (
        <Field>
          <FieldLabel>Query</FieldLabel>
          <QueryBuilder value={value} onChange={onChange} idPrefix={idPrefix} />
          <FieldDescription>
            The catalog items matching this query fill the block or pool automatically, and follow the catalog as it
            syncs.
          </FieldDescription>
        </Field>
      )}
    </>
  )
}
