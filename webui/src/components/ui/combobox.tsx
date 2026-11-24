import * as React from "react"
import { cn } from "cn"
import { CheckIcon, ChevronDownIcon } from "lucide-react"

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

interface ComboboxOption {
  value: string
  label: string
}

interface ComboboxProps {
  options: ComboboxOption[]
  value: string | null
  onValueChange: (value: string) => void
  placeholder?: string
  searchPlaceholder?: string
  emptyMessage?: string
  disabled?: boolean
  className?: string
  id?: string
  // Server-side search: when set, options are not filtered locally and the
  // typed text is reported here so the caller can refetch.
  onSearchChange?: (search: string) => void
}

// Searchable single-select built on Popover (no extra dependency): a trigger
// styled like SelectTrigger, a filter input and a listbox. Arrow keys move
// the highlight, Enter picks, Escape closes (Popover default).
function Combobox({
  options,
  value,
  onValueChange,
  placeholder = "Select…",
  searchPlaceholder = "Search…",
  emptyMessage = "No results.",
  disabled,
  className,
  id,
  onSearchChange,
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false)
  const [search, setSearch] = React.useState("")
  const [active, setActive] = React.useState(0)

  const filtered = React.useMemo(() => {
    if (onSearchChange) return options
    const needle = search.trim().toLowerCase()
    return needle ? options.filter((o) => o.label.toLowerCase().includes(needle)) : options
  }, [options, search, onSearchChange])

  const selected = options.find((o) => o.value === value)

  function handleOpenChange(next: boolean) {
    setOpen(next)
    if (!next) {
      setSearch("")
      onSearchChange?.("")
    }
    setActive(0)
  }

  function pick(option: ComboboxOption) {
    onValueChange(option.value)
    handleOpenChange(false)
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault()
      setActive((i) => Math.min(i + 1, filtered.length - 1))
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setActive((i) => Math.max(i - 1, 0))
    } else if (e.key === "Enter") {
      e.preventDefault()
      const option = filtered[active]
      if (option) pick(option)
    }
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          data-slot="combobox-trigger"
          data-placeholder={selected ? undefined : ""}
          className={cn(
            "flex h-8 w-full items-center justify-between max-sm:h-11 gap-1.5 rounded-lg border border-input bg-transparent py-2 pr-2 pl-2.5 text-sm whitespace-nowrap transition-colors outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive data-placeholder:text-muted-foreground dark:bg-input/30 dark:hover:bg-input/50",
            className
          )}
        >
          <span className="truncate">{selected ? selected.label : placeholder}</span>
          <ChevronDownIcon className="size-4 shrink-0 text-muted-foreground" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-56 p-1">
        <input
          autoFocus
          data-slot="combobox-input"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value)
            setActive(0)
            onSearchChange?.(e.target.value)
          }}
          onKeyDown={handleKeyDown}
          placeholder={searchPlaceholder}
          className="mb-1 h-8 w-full rounded-md border-b border-border bg-transparent px-2 text-sm outline-none placeholder:text-muted-foreground"
        />
        <div role="listbox" className="max-h-60 overflow-y-auto">
          {filtered.length === 0 && (
            <p className="px-2 py-1.5 text-sm text-muted-foreground">{emptyMessage}</p>
          )}
          {filtered.map((option, i) => (
            <div
              key={option.value}
              role="option"
              aria-selected={option.value === value}
              data-active={i === active ? "" : undefined}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(option)}
              className="relative flex cursor-default items-center rounded-md py-1 pr-8 pl-2 text-sm outline-hidden select-none data-active:bg-accent data-active:text-accent-foreground"
            >
              <span className="truncate">{option.label}</span>
              {option.value === value && (
                <CheckIcon className="absolute right-2 size-4" />
              )}
            </div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
}

export { Combobox }
export type { ComboboxOption }
