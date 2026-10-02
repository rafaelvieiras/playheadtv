import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link, useNavigate } from 'react-router-dom'

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
import { FillerPoolDialog } from '@/components/FillerPoolDialog'
import { api } from '@/lib/http'
import { summarizeStoredCriteria } from '@/lib/query'
import type { FillerPool } from '@/lib/types'

function DynamicBadge({ dynamic }: { dynamic: boolean }) {
  if (dynamic) {
    return (
      <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
        Dynamic
      </span>
    )
  }
  return (
    <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
      Manual
    </span>
  )
}

export function FillerPoolsPage() {
  const queryClient = useQueryClient()
  const { data: pools, isLoading, isError } = useQuery({
    queryKey: ['filler-pools'],
    queryFn: () => api.get<FillerPool[]>('/filler-pools'),
  })

  const navigate = useNavigate()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [deletingPool, setDeletingPool] = useState<FillerPool | undefined>(undefined)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/filler-pools/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['filler-pools'] })
      toast.success('Filler pool deleted.')
      setDeletingPool(undefined)
    },
    onError: () => {
      toast.error('Could not delete the filler pool.')
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Filler pools</h1>
        <Button onClick={() => setDialogOpen(true)}>New pool</Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load filler pools.</p>}

      {pools && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Criteria</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {pools.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  No filler pools yet.
                </TableCell>
              </TableRow>
            )}
            {pools.map((pool) => (
              <TableRow key={pool.id}>
                <TableCell>
                  <Link to={`/filler-pools/${pool.id}`} className="hover:underline">
                    {pool.name}
                  </Link>
                </TableCell>
                <TableCell>
                  <DynamicBadge dynamic={pool.criteria != null} />
                </TableCell>
                <TableCell className="max-w-96 truncate text-muted-foreground">
                  {pool.criteria ? summarizeStoredCriteria(pool.criteria) : '—'}
                </TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/filler-pools/${pool.id}`}>Edit</Link>
                  </Button>
                  <Button variant="destructive" size="sm" onClick={() => setDeletingPool(pool)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <FillerPoolDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreated={(pool) => navigate(`/filler-pools/${pool.id}`)}
      />

      <AlertDialog open={deletingPool !== undefined} onOpenChange={(open) => !open && setDeletingPool(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete filler pool?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete the filler pool "{deletingPool?.name}" and its items. This action
              cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deletingPool && deleteMutation.mutate(deletingPool.id)}
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
