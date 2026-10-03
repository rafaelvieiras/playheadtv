import { CriteriaPreviewPanel } from '@/components/CriteriaPreviewPanel'
import { QueryBuilder } from '@/components/QueryBuilder'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { validateQuery, type Query } from '@/lib/query'

export interface CollectionDraft {
  name: string
  description: string
  query: Query
}

// Name, description and the query builder of a collection, with the live match
// preview. The one place in the app where a catalog query is edited.
export function CollectionFields({
  value,
  onChange,
  idPrefix,
}: {
  value: CollectionDraft
  onChange: (value: CollectionDraft) => void
  idPrefix: string
}) {
  const queryError = validateQuery(value.query)
  return (
    <>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-name`}>Name</FieldLabel>
        <Input
          id={`${idPrefix}-name`}
          required
          value={value.name}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
      </Field>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-description`}>Description (optional)</FieldLabel>
        <Textarea
          id={`${idPrefix}-description`}
          value={value.description}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </Field>
      <Field>
        <FieldLabel>Query</FieldLabel>
        <QueryBuilder value={value.query} onChange={(query) => onChange({ ...value, query })} idPrefix={`${idPrefix}-query`} />
        <FieldDescription>
          The catalog items matching this query follow the catalog as it syncs. Playback order, amount and interleave
          settings are chosen where the collection is used.
        </FieldDescription>
      </Field>
      {queryError && (
        <p className="text-sm text-destructive" data-testid={`${idPrefix}-query-error`}>
          {queryError}
        </p>
      )}
      <CriteriaPreviewPanel criteria={queryError ? null : value.query} />
    </>
  )
}
