import { CriteriaPreviewPanel } from '@/components/CriteriaPreviewPanel'
import { QueryBuilder } from '@/components/QueryBuilder'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
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
  nameError,
}: {
  value: CollectionDraft
  onChange: (value: CollectionDraft) => void
  idPrefix: string
  /** Reported by the caller after a save attempt, never before the first one. */
  nameError?: string
}) {
  const queryError = validateQuery(value.query)
  return (
    <>
      <Field data-invalid={!!nameError}>
        <FieldLabel htmlFor={`${idPrefix}-name`}>
          Name <span className="text-destructive">*</span>
        </FieldLabel>
        <Input
          id={`${idPrefix}-name`}
          aria-invalid={!!nameError}
          aria-describedby={nameError ? `${idPrefix}-name-error` : undefined}
          value={value.name}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
        <FieldError id={`${idPrefix}-name-error`}>{nameError}</FieldError>
      </Field>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-description`}>Description (optional)</FieldLabel>
        <Textarea
          id={`${idPrefix}-description`}
          value={value.description}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </Field>
      <Field data-invalid={!!queryError}>
        <FieldLabel>Query</FieldLabel>
        <QueryBuilder value={value.query} onChange={(query) => onChange({ ...value, query })} idPrefix={`${idPrefix}-query`} />
        {/* The query's problem belongs to the query, not to a red line at the
            bottom of the form the operator has to trace back. */}
        <FieldError data-testid={`${idPrefix}-query-error`}>{queryError}</FieldError>
        <FieldDescription>
          The catalog items matching this query follow the catalog as it syncs. Playback order, amount and interleave
          settings are chosen where the collection is used.
        </FieldDescription>
      </Field>
      <CriteriaPreviewPanel criteria={queryError ? null : value.query} />
    </>
  )
}
