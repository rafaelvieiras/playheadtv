import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Loading, QueryError } from '@/components/QueryError'
import { KIND_LABELS, SCOPE_LABELS, STRATEGY_LABELS } from '@/lib/filler'
import { api } from '@/lib/http'
import type { Block, Channel, FillerPool, FillerRule } from '@/lib/types'

export function FillerRulesPage() {
  const queryClient = useQueryClient()
  const {
    data: rules,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['filler-rules'],
    queryFn: () => api.get<FillerRule[]>('/filler-rules'),
  })
  const { data: channels } = useQuery({
    queryKey: ['channels'],
    queryFn: () => api.get<Channel[]>('/channels'),
  })
  const { data: blocks } = useQuery({
    queryKey: ['blocks'],
    queryFn: () => api.get<Block[]>('/blocks'),
  })
  const { data: pools } = useQuery({
    queryKey: ['filler-pools'],
    queryFn: () => api.get<FillerPool[]>('/filler-pools'),
  })

  const channelNames = useMemo(() => new Map((channels ?? []).map((c) => [c.id, `${c.number} — ${c.name}`])), [
    channels,
  ])
  const blockNames = useMemo(() => new Map((blocks ?? []).map((b) => [b.id, b.name])), [blocks])
  const poolNames = useMemo(() => new Map((pools ?? []).map((p) => [p.id, p.name])), [pools])

  function scopeName(rule: FillerRule): string {
    const name = rule.scope_type === 'channel' ? channelNames.get(rule.scope_id) : blockNames.get(rule.scope_id)
    return name ?? rule.scope_id
  }

  const [deletingRule, setDeletingRule] = useState<FillerRule | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/filler-rules/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['filler-rules'] })
      toast.success('Filler rule deleted.')
      setDeletingRule(undefined)
    },
    onError: () => {
      toast.error('Could not delete the filler rule.')
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Filler rules</h1>
        <Button asChild>
          <Link to="/filler-rules/new">New rule</Link>
        </Button>
      </div>

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the filler rules"
          error={error}
          onRetry={() => void refetch()}
          stale={rules !== undefined}
        />
      )}

      {rules && (
        <Table label="Filler rules">
          <TableHeader>
            <TableRow>
              <TableHead>Scope</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Filler pool</TableHead>
              <TableHead>Selection</TableHead>
              <TableHead>Position</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rules.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  No filler rules yet.
                </TableCell>
              </TableRow>
            )}
            {rules.map((rule) => (
              <TableRow key={rule.id}>
                <TableCell>
                  <Link to={`/filler-rules/${rule.id}`} className="hover:underline">
                    {SCOPE_LABELS[rule.scope_type]}: {scopeName(rule)}
                  </Link>
                </TableCell>
                <TableCell>{KIND_LABELS[rule.kind]}</TableCell>
                <TableCell>{poolNames.get(rule.filler_pool_id) ?? rule.filler_pool_id}</TableCell>
                <TableCell>{STRATEGY_LABELS[rule.selection_strategy]}</TableCell>
                <TableCell>{rule.position}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    <Button asChild variant="outline" size="sm">
                      <Link to={`/filler-rules/${rule.id}`}>Edit</Link>
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      aria-label={`Delete the ${KIND_LABELS[rule.kind].toLowerCase()} rule for ${scopeName(rule)}`}
                      onClick={() => setDeletingRule(rule)}
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
        open={deletingRule !== undefined}
        onClose={() => setDeletingRule(undefined)}
        title={
          deletingRule
            ? `Delete the ${KIND_LABELS[deletingRule.kind].toLowerCase()} rule for ${scopeName(deletingRule)}?`
            : 'Delete filler rule?'
        }
        description="The rule stops inserting its filler. The pool and the items it plays are not deleted. This action cannot be undone."
        confirmLabel="Delete rule"
        pendingLabel="Deleting…"
        onConfirm={() => deletingRule && deleteMutation.mutate(deletingRule.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
