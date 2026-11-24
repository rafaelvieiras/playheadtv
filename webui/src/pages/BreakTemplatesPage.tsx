import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Loading, QueryError } from '@/components/QueryError'
import { BREAK_KIND_LABELS, describeSteps } from '@/lib/breakTemplate'
import { SCOPE_LABELS } from '@/lib/filler'
import { api } from '@/lib/http'
import type { Block, BreakTemplate, Channel } from '@/lib/types'

export function BreakTemplatesPage() {
  const queryClient = useQueryClient()
  const {
    data: templates,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['break-templates'],
    queryFn: () => api.get<BreakTemplate[]>('/break-templates'),
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })

  const channelNames = useMemo(() => new Map((channels ?? []).map((c) => [c.id, `${c.number} — ${c.name}`])), [
    channels,
  ])
  const blockNames = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b.name])), [blocks])

  function scopeName(t: BreakTemplate): string {
    const name = t.scope_type === 'channel' ? channelNames.get(t.scope_id) : blockNames.get(t.scope_id)
    return name ?? t.scope_id
  }

  const [deleting, setDeleting] = useState<BreakTemplate | undefined>(undefined)
  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/break-templates/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['break-templates'] })
      toast.success('Break template deleted.')
      setDeleting(undefined)
    },
    onError: () => toast.error('Could not delete the break template.'),
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Break templates</h1>
        <Button asChild>
          <Link to="/break-templates/new">New template</Link>
        </Button>
      </div>
      <p className="max-w-2xl text-sm text-muted-foreground">
        A break template replaces the single filler item a PreRoll, MidRoll or PostRoll rule inserts at a program
        boundary with an ordered list of steps: single items and fills of a set length. Where one applies it wins over
        a plain filler rule of the same type at the same level (block over channel).
      </p>

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the break templates"
          error={error}
          onRetry={() => void refetch()}
          stale={templates !== undefined}
        />
      )}

      {templates && (
        <Table label="Break templates">
          <TableHeader>
            <TableRow>
              <TableHead>Scope</TableHead>
              <TableHead>Boundary</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Steps</TableHead>
              <TableHead>Position</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {templates.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  No break templates yet.
                </TableCell>
              </TableRow>
            )}
            {templates.map((t) => (
              <TableRow key={t.id} data-testid="break-template-row">
                <TableCell>
                  <Link to={`/break-templates/${t.id}`} className="hover:underline">
                    {SCOPE_LABELS[t.scope_type]}: {scopeName(t)}
                  </Link>
                </TableCell>
                <TableCell>{BREAK_KIND_LABELS[t.kind]}</TableCell>
                <TableCell>{t.name || '—'}</TableCell>
                <TableCell>{describeSteps(t)}</TableCell>
                <TableCell>{t.position}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    <Button asChild variant="outline" size="sm">
                      <Link to={`/break-templates/${t.id}`}>Edit</Link>
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      aria-label={`Delete the break template for ${scopeName(t)}`}
                      onClick={() => setDeleting(t)}
                    >
                      Delete
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <ConfirmDialog
        open={deleting !== undefined}
        onClose={() => setDeleting(undefined)}
        title={
          deleting
            ? `Delete the ${BREAK_KIND_LABELS[deleting.kind].toLowerCase()} break template for ${scopeName(deleting)}?`
            : 'Delete break template?'
        }
        description="The plain filler rules of that boundary and level (if any) apply again. This action cannot be undone."
        confirmLabel="Delete template"
        pendingLabel="Deleting…"
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
