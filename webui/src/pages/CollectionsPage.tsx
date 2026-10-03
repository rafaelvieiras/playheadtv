import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import { CollectionDialog } from '@/components/CollectionDialog'
import { DeleteCollectionDialog } from '@/components/DeleteCollectionDialog'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCollections } from '@/lib/collections'
import { summarizeQuery } from '@/lib/query'
import type { Collection } from '@/lib/types'

export function CollectionsPage() {
  const navigate = useNavigate()
  const { data: collections, isLoading, isError } = useCollections()
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Collection | undefined>(undefined)

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Collections</h1>
          <p className="text-sm text-muted-foreground">
            Named catalog queries that dynamic blocks, filler pools and criteria items play. Edit one place, every user follows.
          </p>
        </div>
        <Button onClick={() => setCreating(true)} data-testid="new-collection-button">
          New collection
        </Button>
      </div>

      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load collections.</p>}

      {collections && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Query</TableHead>
              <TableHead>Used by</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {collections.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  No collections yet.
                </TableCell>
              </TableRow>
            )}
            {collections.map((c) => (
              <TableRow key={c.id} data-testid="collection-row">
                <TableCell>
                  <Link to={`/collections/${c.id}`} className="hover:underline" data-testid="collection-name">
                    {c.name}
                  </Link>
                  {c.description && <div className="max-w-64 truncate text-xs text-muted-foreground">{c.description}</div>}
                </TableCell>
                <TableCell className="max-w-96 truncate text-muted-foreground">{summarizeQuery(c.query)}</TableCell>
                <TableCell data-testid="collection-usage-count">
                  {c.usage_count === 0 ? '—' : `${c.usage_count} ${c.usage_count === 1 ? 'user' : 'users'}`}
                </TableCell>
                <TableCell className="flex justify-end gap-2 text-right">
                  <Button asChild variant="outline" size="sm">
                    <Link to={`/collections/${c.id}`}>Edit</Link>
                  </Button>
                  <Button variant="destructive" size="sm" data-testid="collection-delete" onClick={() => setDeleting(c)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <CollectionDialog open={creating} onOpenChange={setCreating} onCreated={(c) => navigate(`/collections/${c.id}`)} />
      <DeleteCollectionDialog collection={deleting} onClose={() => setDeleting(undefined)} />
    </div>
  )
}
