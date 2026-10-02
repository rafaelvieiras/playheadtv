import { TriangleAlert } from 'lucide-react'
import type { ReactNode } from 'react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

// Inline warning shown next to the thing it is about (an empty block, a gap,
// criteria that match nothing), not as a toast.
export function InlineWarning({ title, children }: { title?: ReactNode; children?: ReactNode }) {
  return (
    <Alert variant="warning" data-testid="inline-warning">
      <TriangleAlert />
      <div className="flex flex-col gap-0.5">
        {title && <AlertTitle>{title}</AlertTitle>}
        {children && <AlertDescription>{children}</AlertDescription>}
      </div>
    </Alert>
  )
}
