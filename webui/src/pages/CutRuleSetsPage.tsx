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
import { describeRules } from '@/lib/cutRules'
import { SCOPE_LABELS } from '@/lib/filler'
import { api } from '@/lib/http'
import type { Block, Channel, CutRuleSet } from '@/lib/types'

export function CutRuleSetsPage() {
  const queryClient = useQueryClient()
  const { data: sets, isLoading, isError } = useQuery({
    queryKey: ['cut-rule-sets'],
    queryFn: () => api.get<CutRuleSet[]>('/cut-rule-sets'),
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const { data: blocks } = useQuery({ queryKey: ['blocks'], queryFn: () => api.get<Block[]>('/blocks') })

  const channelNames = useMemo(() => new Map((channels ?? []).map((c) => [c.id, `${c.number} — ${c.name}`])), [
    channels,
  ])
  const blockNames = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b.name])), [blocks])

  function scopeName(s: CutRuleSet): string {
    const name = s.scope_type === 'channel' ? channelNames.get(s.scope_id) : blockNames.get(s.scope_id)
    return name ?? s.scope_id
  }

  const [deleting, setDeleting] = useState<CutRuleSet | undefined>(undefined)
  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/cut-rule-sets/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['cut-rule-sets'] })
      toast.success('Cut rule set deleted.')
      setDeleting(undefined)
    },
    onError: () => toast.error('Could not delete the cut rule set.'),
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Cut rules</h1>
        <Button asChild>
          <Link to="/cut-rule-sets/new">New rule set</Link>
        </Button>
      </div>
      <p className="max-w-2xl text-sm text-muted-foreground">
        A cut rule set decides where inside a movie or episode a break goes: an ordered list of rules chosen by the
        item's kind, length and chapters, each cutting every N minutes, at percentages, at fixed times, in phases or at
        chapters. The first matching rule wins. Rule sets are stored and validated; the guide does not apply them yet.
      </p>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load cut rule sets.</p>}

      {sets && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Scope</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Rules</TableHead>
              <TableHead>Position</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sets.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
                  No cut rule sets yet.
                </TableCell>
              </TableRow>
            )}
            {sets.map((s) => (
              <TableRow key={s.id} data-testid="cut-rule-set-row">
                <TableCell>
                  <Link to={`/cut-rule-sets/${s.id}`} className="hover:underline">
                    {SCOPE_LABELS[s.scope_type]}: {scopeName(s)}
                  </Link>
                </TableCell>
                <TableCell>{s.name || '—'}</TableCell>
                <TableCell>
                  <ol className="list-inside list-decimal text-sm">
                    {describeRules(s).map((line, i) => (
                      <li key={i}>{line}</li>
                    ))}
                  </ol>
                </TableCell>
                <TableCell>{s.position}</TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/cut-rule-sets/${s.id}`}>Edit</Link>
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeleting(s)}>
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
            <AlertDialogTitle>Delete cut rule set?</AlertDialogTitle>
            <AlertDialogDescription>This will permanently delete this cut rule set. This action cannot be undone.</AlertDialogDescription>
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
