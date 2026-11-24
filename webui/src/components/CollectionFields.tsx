import { useState } from 'react'

import { CriteriaPreviewPanel } from '@/components/CriteriaPreviewPanel'
import { QueryBuilder } from '@/components/QueryBuilder'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { summarizeQuery, validateQuery, type Query } from '@/lib/query'

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
  // The preview follows the last query that was complete. Handing it the
  // current one meant that clearing a value to retype it (which is an invalid
  // query for a keystroke) hid the count, the sample and the zero-match
  // warning, so the panel blinked out of existence while being edited.
  const [lastValid, setLastValid] = useState<Query | undefined>(queryError ? undefined : value.query)
  if (!queryError && lastValid !== value.query) setLastValid(value.query)
  const previewQuery = queryError ? (lastValid ?? null) : value.query

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
        {/* The same sentence the collection list shows for this query, so the
            editor and the list never describe one query in two ways. */}
        <p className="text-sm text-muted-foreground" data-testid={`${idPrefix}-query-summary`}>
          {summarizeQuery(value.query)}
        </p>
        {/* The query's problem belongs to the query, not to a red line at the
            bottom of the form the operator has to trace back. */}
        <FieldError data-testid={`${idPrefix}-query-error`}>{queryError}</FieldError>
        <FieldDescription>
          The catalog items matching this query follow the catalog as it syncs. Playback order, amount and interleave
          settings are chosen where the collection is used.
        </FieldDescription>
      </Field>
      {previewQuery && queryError && (
        <p className="text-xs text-muted-foreground" data-testid={`${idPrefix}-preview-stale`}>
          The preview below shows the last query that was complete.
        </p>
      )}
      <CriteriaPreviewPanel criteria={previewQuery} headingLevel={2} />
    </>
  )
}
