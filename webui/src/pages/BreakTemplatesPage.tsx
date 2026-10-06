import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { BREAK_KIND_LABELS, describeSteps } from '@/lib/breakTemplate'
import { SCOPE_LABELS } from '@/lib/filler'
import { api } from '@/lib/http'
import type { Block, BreakTemplate, Channel } from '@/lib/types'

export function BreakTemplatesPage() {
  const queryClient = useQueryClient()
  const { data: templates, isLoading, isError } = useQuery({
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

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load break templates.</p>}

      {templates && (
        <Table>
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
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/break-templates/${t.id}`}>Edit</Link>
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeleting(t)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <AlertDialog open={deleting !== undefined} onOpenChange={(open) => !open && setDeleting(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete break template?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete this break template; the plain filler rules of that type (if any) apply
              again. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deleting && deleteMutation.mutate(deleting.id)}
              disabled={deleteMutation.isPending}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
