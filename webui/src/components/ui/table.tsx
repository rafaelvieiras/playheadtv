"use client"

import * as React from "react"
import { cn } from "cn"

// Below `md` (the phone layout, docs/ARCHITECTURE.md "Admin shell") a table is
// wider than the column that holds it, so the operator scrolls right to reach a
// row's actions and loses which row they belong to: six channels all read
// "…grid · Delete". The first column therefore stays pinned on a phone, over an
// opaque card background so the scrolled content cannot show through it. Above
// `md` nothing changes — the treatment is gated on the breakpoint, not on a
// per-table flag, so all nine tables degrade the same way.
const mobileStickyFirstColumn = cn(
  "max-md:[&_th:first-child]:sticky max-md:[&_th:first-child]:left-0 max-md:[&_th:first-child]:z-20 max-md:[&_th:first-child]:bg-card",
  "max-md:[&_td:first-child]:sticky max-md:[&_td:first-child]:left-0 max-md:[&_td:first-child]:z-10 max-md:[&_td:first-child]:bg-card",
  "max-md:[&_th:first-child]:border-r max-md:[&_th:first-child]:border-border",
  "max-md:[&_td:first-child]:border-r max-md:[&_td:first-child]:border-border",
)

function Table({
  className,
  label,
  ...props
}: React.ComponentProps<"table"> & {
  /**
   * Accessible name for the scroll container. Required whenever the table can
   * overflow: a scrollable region that takes focus needs a name to be usable
   * from a keyboard or a screen reader.
   */
  label?: string
}) {
  return (
    <div
      data-slot="table-container"
      className="relative w-full overflow-x-auto focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
      role="region"
      aria-label={label}
      tabIndex={label ? 0 : undefined}
    >
      <table
        data-slot="table"
        className={cn("w-full caption-bottom text-sm", mobileStickyFirstColumn, className)}
        {...props}
      />
    </div>
  )
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      className={cn("[&_tr]:border-b", className)}
      {...props}
    />
  )
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  )
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      className={cn(
        "border-t bg-muted/50 font-medium [&>tr]:last:border-b-0",
        className
      )}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted",
        className
      )}
      {...props}
    />
  )
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "h-10 px-2 text-left align-middle font-medium whitespace-nowrap text-foreground [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      className={cn(
        "p-2 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-4 text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
}
