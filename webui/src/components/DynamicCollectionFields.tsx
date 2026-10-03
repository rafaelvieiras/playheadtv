import { BlockPlaybackFields, type BlockPlaybackDraft } from '@/components/BlockPlaybackFields'
import { CollectionPicker } from '@/components/CollectionPicker'
import { CriteriaPreviewPanel } from '@/components/CriteriaPreviewPanel'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Switch } from '@/components/ui/switch'
import { useCollections } from '@/lib/collections'

// value is null for a manual block or pool, '' for a dynamic one with no
// collection chosen yet, else the collection id.
export type CollectionChoice = string | null

export function validateCollectionChoice(value: CollectionChoice): string | undefined {
  return value === '' ? 'Choose a collection, or turn dynamic off.' : undefined
}

interface DynamicCollectionFieldsProps {
  value: CollectionChoice
  onChange: (value: CollectionChoice) => void
  idPrefix?: string
  // Blocks only (not filler pools): the playback order of the dynamic block.
  playback?: { value: BlockPlaybackDraft; onChange: (value: BlockPlaybackDraft) => void }
  // Show what the chosen collection matches right now (the editor pages do).
  preview?: boolean
}

// The "dynamic" switch plus, while it is on, the collection picker: the block or
// pool is then filled from the catalog by that collection's query.
export function DynamicCollectionFields({
  value,
  onChange,
  idPrefix = 'collection',
  playback,
  preview,
}: DynamicCollectionFieldsProps) {
  const enabled = value !== null
  const { data: collections } = useCollections()
  const selected = value ? collections?.find((c) => c.id === value) : undefined

  return (
    <>
      <Field orientation="horizontal">
        <FieldLabel htmlFor={`${idPrefix}-dynamic`}>Dynamic (plays a collection)</FieldLabel>
        <Switch
          id={`${idPrefix}-dynamic`}
          checked={enabled}
          onCheckedChange={(checked) => onChange(checked ? (value ?? '') : null)}
        />
      </Field>

      {enabled && (
        <Field>
          <FieldLabel>Collection</FieldLabel>
          <CollectionPicker value={value} onChange={onChange} idPrefix={idPrefix} />
          <FieldDescription>
            The catalog items matching the collection's query fill the block or pool automatically, and follow the
            catalog as it syncs.
          </FieldDescription>
        </Field>
      )}

      {enabled && playback && (
        <BlockPlaybackFields value={playback.value} onChange={playback.onChange} idPrefix={idPrefix} />
      )}

      {enabled && preview && selected && <CriteriaPreviewPanel criteria={selected.query} />}
    </>
  )
}
