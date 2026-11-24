import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Link, useNavigate } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { FillerPoolDialog } from '@/components/FillerPoolDialog'
import { Loading, QueryError } from '@/components/QueryError'
import { api } from '@/lib/http'
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
  const {
    data: pools,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
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

      {isLoading && <Loading />}
      {isError && (
        <QueryError
          subject="the filler pools"
          error={error}
          onRetry={() => void refetch()}
          stale={pools !== undefined}
        />
      )}

      {pools && (
        <Table label="Filler pools">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Collection</TableHead>
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
                  <DynamicBadge dynamic={pool.dynamic} />
                </TableCell>
                <TableCell className="max-w-96 truncate text-muted-foreground">
                  {pool.dynamic ? pool.collection_name ?? 'Held for review (old query)' : '—'}
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    <Button asChild variant="outline" size="sm">
                      <Link to={`/filler-pools/${pool.id}`}>Edit</Link>
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      aria-label={`Delete ${pool.name}`}
                      onClick={() => setDeletingPool(pool)}
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

      <FillerPoolDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreated={(pool) => navigate(`/filler-pools/${pool.id}`)}
      />

      <ConfirmDialog
        open={deletingPool !== undefined}
        onClose={() => setDeletingPool(undefined)}
        title={`Delete filler pool "${deletingPool?.name}"?`}
        description="The pool and its items are deleted. A filler rule that still points at it is refused by the backend."
        confirmLabel="Delete pool"
        pendingLabel="Deleting…"
        onConfirm={() => deletingPool && deleteMutation.mutate(deletingPool.id)}
        isPending={deleteMutation.isPending}
        error={deleteMutation.error}
      />
    </div>
  )
}
