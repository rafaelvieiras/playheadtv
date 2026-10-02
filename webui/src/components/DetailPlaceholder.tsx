import { useParams } from 'react-router-dom'

import { PageHeader } from '@/components/PageHeader'

// Stand-in for a detail page that a later slice will build. Shows the id from
// the URL so the route wiring is observable.
export function DetailPlaceholder({
  listLabel,
  listPath,
  entity,
}: {
  listLabel: string
  listPath: string
  entity: string
}) {
  const { id } = useParams()
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={`${entity} ${id ?? ''}`}
        breadcrumb={[{ label: listLabel, to: listPath }, { label: entity }]}
      />
      <p className="text-sm text-muted-foreground" data-testid="detail-placeholder">
        The {entity.toLowerCase()} editor is not built yet.
      </p>
    </div>
  )
}
