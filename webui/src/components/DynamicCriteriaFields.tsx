import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import type { DynamicCriteria } from '@/lib/types'

interface DynamicCriteriaFieldsProps {
  value: DynamicCriteria | null
  onChange: (value: DynamicCriteria | null) => void
  // Both pools and blocks may target either kind — this stays open by
  // default (not narrowed per caller) since a block for a "movie" channel
  // could in principle still filter episodes, and locking it down isn't
  // part of the current spec.
  kindOptions?: { value: DynamicCriteria['kind']; label: string }[]
  idPrefix?: string
}

const DEFAULT_KIND_OPTIONS: { value: DynamicCriteria['kind']; label: string }[] = [
  { value: 'movie', label: 'Movie' },
  { value: 'episode', label: 'Episode' },
]

const emptyCriteria: DynamicCriteria = {
  kind: 'movie',
  genres: [],
  year_from: null,
  year_to: null,
}

export function DynamicCriteriaFields({
  value,
  onChange,
  kindOptions = DEFAULT_KIND_OPTIONS,
  idPrefix = 'criteria',
}: DynamicCriteriaFieldsProps) {
  const enabled = value !== null

  function toggle(checked: boolean) {
    onChange(checked ? (value ?? emptyCriteria) : null)
  }

  function update(patch: Partial<DynamicCriteria>) {
    if (!value) return
    onChange({ ...value, ...patch })
  }

  function handleGenresChange(raw: string) {
    const genres = raw
      .split(',')
      .map((g) => g.trim())
      .filter((g) => g.length > 0)
    update({ genres })
  }

  return (
    <>
      <Field orientation="horizontal">
        <FieldLabel htmlFor={`${idPrefix}-dynamic`}>Dynamic (automatic criteria)</FieldLabel>
        <Switch id={`${idPrefix}-dynamic`} checked={enabled} onCheckedChange={toggle} />
      </Field>

      {enabled && value && (
        <>
          <Field>
            <FieldLabel htmlFor={`${idPrefix}-kind`}>Item type</FieldLabel>
            <Select value={value.kind} onValueChange={(v) => update({ kind: v as DynamicCriteria['kind'] })}>
              <SelectTrigger id={`${idPrefix}-kind`} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {kindOptions.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field>
            <FieldLabel htmlFor={`${idPrefix}-genres`}>Genres</FieldLabel>
            <Input
              id={`${idPrefix}-genres`}
              placeholder="Comedy, Komedie"
              value={(value.genres ?? []).join(', ')}
              onChange={(e) => handleGenresChange(e.target.value)}
            />
            <FieldDescription>
              Comma-separated list. Matches with OR: include the same genre in more than one language (e.g.
              "Comedy, Komedie") since the catalog may mix languages.
            </FieldDescription>
          </Field>

          <Field orientation="responsive">
            <Field>
              <FieldLabel htmlFor={`${idPrefix}-year-from`}>Year from (optional)</FieldLabel>
              <Input
                id={`${idPrefix}-year-from`}
                type="number"
                value={value.year_from ?? ''}
                onChange={(e) => update({ year_from: e.target.value === '' ? null : Number(e.target.value) })}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={`${idPrefix}-year-to`}>Year to (optional)</FieldLabel>
              <Input
                id={`${idPrefix}-year-to`}
                type="number"
                value={value.year_to ?? ''}
                onChange={(e) => update({ year_to: e.target.value === '' ? null : Number(e.target.value) })}
              />
            </Field>
          </Field>
        </>
      )}
    </>
  )
}
