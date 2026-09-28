import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

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
import { FillerRuleDialog, KIND_LABELS, SCOPE_LABELS, STRATEGY_LABELS } from '@/components/FillerRuleDialog'
import { api } from '@/lib/http'
import type { Block, Channel, FillerPool, FillerRule } from '@/lib/types'

export function FillerRulesPage() {
  const queryClient = useQueryClient()
  const { data: rules, isLoading, isError } = useQuery({
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

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<FillerRule | undefined>(undefined)
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

  function openCreateDialog() {
    setEditingRule(undefined)
    setDialogOpen(true)
  }

  function openEditDialog(rule: FillerRule) {
    setEditingRule(rule)
    setDialogOpen(true)
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Filler rules</h1>
        <Button onClick={openCreateDialog}>New rule</Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load filler rules.</p>}

      {rules && (
        <Table>
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
                  {SCOPE_LABELS[rule.scope_type]}: {scopeName(rule)}
                </TableCell>
                <TableCell>{KIND_LABELS[rule.kind]}</TableCell>
                <TableCell>{poolNames.get(rule.filler_pool_id) ?? rule.filler_pool_id}</TableCell>
                <TableCell>{STRATEGY_LABELS[rule.selection_strategy]}</TableCell>
                <TableCell>{rule.position}</TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button variant="outline" size="sm" onClick={() => openEditDialog(rule)}>
                    Edit
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeletingRule(rule)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <FillerRuleDialog open={dialogOpen} onOpenChange={setDialogOpen} rule={editingRule} />

      <AlertDialog open={deletingRule !== undefined} onOpenChange={(open) => !open && setDeletingRule(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete filler rule?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete this filler rule. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingRule && deleteMutation.mutate(deletingRule.id)}
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
