import { CircleAlert, RotateCw } from 'lucide-react'
import type { ReactNode } from 'react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ApiError } from '@/lib/http'

// The one error panel a page shows when a request fails.
//
// Every list page used to render a bare red sentence with no action and no
// announcement, and the page body still drew itself from the failed data: the
// guide printed "Could not load the programming guide." above six rows claiming
// "No programming", which is indistinguishable from a materialiser that
// produced nothing — the one thing an operator must not confuse. So this panel
// (a) is a live region (`Alert` carries `role="alert"`), (b) names the request
// that failed and says why, (c) offers the retry, and (d) says out loud when
// what is on screen is the previous answer rather than the current one.
export function QueryError({
  subject,
  error,
  onRetry,
  stale = false,
  children,
  testId = 'query-error',
}: {
  /** What could not be loaded, as it reads in the heading: "the channels". */
  subject: string
  error?: unknown
  onRetry?: () => void
  /** True when an earlier answer is still on screen (a failed refresh). */
  stale?: boolean
  children?: ReactNode
  testId?: string
}) {
  const detail =
    error instanceof ApiError
      ? `${error.status}: ${error.message}`
      : error instanceof Error
        ? error.message
        : undefined

  return (
    <Alert variant="destructive" data-testid={testId}>
      <CircleAlert />
      <div className="flex flex-col gap-2">
        <AlertTitle>Could not load {subject}.</AlertTitle>
        <AlertDescription>
          <div className="flex flex-col gap-1">
            {detail && <span className="font-mono text-xs">{detail}</span>}
            <span>
              {stale
                ? 'What is on screen is the last copy that loaded, so it may be out of date.'
                : 'The backend did not answer, or answered with an error.'}
            </span>
            {children}
          </div>
        </AlertDescription>
        {onRetry && (
          <Button variant="outline" size="sm" className="w-fit" onClick={onRetry}>
            <RotateCw className="size-4" />
            Retry
          </Button>
        )}
      </div>
    </Alert>
  )
}

/** The loading line the pages show while a request is in flight. */
export function Loading({ children = 'Loading…' }: { children?: ReactNode }) {
  return (
    <p role="status" className="text-sm text-muted-foreground">
      {children}
    </p>
  )
}
