import { ChevronRight, ArrowLeft } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'

export interface BreadcrumbItem {
  label: string
  // Omit `to` for the current page (last crumb).
  to?: string
}

// Header shared by the authoring detail pages: breadcrumb trail, a back link
// to the parent list (the first crumb with a target) and an optional action
// slot on the right.
export function PageHeader({
  title,
  breadcrumb = [],
  actions,
}: {
  title: ReactNode
  // A top-level page has no trail: an empty breadcrumb is not rendered, so the
  // page's own name never appears twice.
  breadcrumb?: BreadcrumbItem[]
  actions?: ReactNode
}) {
  const back = breadcrumb.find((c) => c.to)
  return (
    <div className="flex flex-col gap-2" data-testid="page-header">
      <nav
        aria-label="Breadcrumb"
        className={`flex items-center gap-1 text-sm text-muted-foreground ${breadcrumb.length === 0 ? 'hidden' : ''}`}
      >
        {back?.to && (
          <Button asChild variant="ghost" size="icon-xs" aria-label={`Back to ${back.label}`}>
            <Link to={back.to}>
              <ArrowLeft />
            </Link>
          </Button>
        )}
        {breadcrumb.map((crumb, i) => (
          <span key={`${crumb.label}-${i}`} className="flex items-center gap-1">
            {i > 0 && <ChevronRight className="size-3.5" />}
            {crumb.to ? (
              <Link to={crumb.to} className="inline-flex min-h-9 items-center hover:text-foreground hover:underline max-sm:min-h-11">
                {crumb.label}
              </Link>
            ) : (
              <span aria-current="page" className="text-foreground">
                {crumb.label}
              </span>
            )}
          </span>
        ))}
      </nav>
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-xl font-semibold">{title}</h1>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
    </div>
  )
}
