import { useEffect, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  FIELDS,
  OP_LABELS,
  defaultValue,
  emptyCondition,
  fieldDef,
  isGroup,
  opsFor,
  type FieldDef,
  type Query,
  type QueryNode,
  type QueryOp,
} from '@/lib/query'

// Visual editor of a condition tree: groups that match all or any of their
// conditions, field / operator / value rows, and NOT around any row or group.
// Controlled by `value`; every edit reports a new tree (never mutates). The
// backend validates the tree again on save.
export function QueryBuilder({
  value,
  onChange,
  idPrefix = 'query',
}: {
  value: Query
  onChange: (value: Query) => void
  idPrefix?: string
}) {
  return (
    <div className="flex flex-col gap-2" data-testid={`${idPrefix}-builder`}>
      <NodeEditor
        node={withoutVersion(value)}
        path={idPrefix}
        isRoot
        onChange={(next) => onChange({ ...(next as QueryNode), version: 1 } as Query)}
      />
    </div>
  )
}

function withoutVersion(q: Query): QueryNode {
  const { version: _version, ...node } = q
  void _version
  return node
}

function NodeEditor({
  node,
  path,
  isRoot,
  onChange,
  onRemove,
}: {
  node: QueryNode
  path: string
  isRoot?: boolean
  onChange: (next: QueryNode) => void
  onRemove?: () => void
}) {
  if (node.not !== undefined) {
    return (
      <div className="flex flex-col gap-2 rounded-md border border-dashed border-destructive/50 p-2" data-testid={`${path}-not`}>
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold tracking-wide text-destructive uppercase">Not</span>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            data-testid={`${path}-unwrap-not`}
            onClick={() => onChange(node.not as QueryNode)}
          >
            Remove NOT
          </Button>
        </div>
        <NodeEditor node={node.not} path={`${path}.not`} onChange={(next) => onChange({ not: next })} onRemove={onRemove} />
      </div>
    )
  }
  if (isGroup(node)) {
    return <GroupEditor node={node} path={path} isRoot={isRoot} onChange={onChange} onRemove={onRemove} />
  }
  return <ConditionRow node={node} path={path} onChange={onChange} onRemove={onRemove} />
}

function GroupEditor({
  node,
  path,
  isRoot,
  onChange,
  onRemove,
}: {
  node: QueryNode
  path: string
  isRoot?: boolean
  onChange: (next: QueryNode) => void
  onRemove?: () => void
}) {
  const mode: 'all' | 'any' = node.all !== undefined ? 'all' : 'any'
  const children = (node.all ?? node.any) as QueryNode[]

  function setChildren(next: QueryNode[]) {
    onChange(mode === 'all' ? { all: next } : { any: next })
  }

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-2" data-testid={`${path}-group`}>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={mode}
          onValueChange={(v) => onChange(v === 'all' ? { all: children } : { any: children })}
        >
          <SelectTrigger className="w-56" size="sm" aria-label="Group match mode" data-testid={`${path}-mode`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Match all of these (AND)</SelectItem>
            <SelectItem value="any">Match any of these (OR)</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid={`${path}-add-condition`}
          onClick={() => setChildren([...children, emptyCondition()])}
        >
          <Plus /> Condition
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid={`${path}-add-group`}
          onClick={() => setChildren([...children, { any: [emptyCondition()] }])}
        >
          <Plus /> Group
        </Button>
        <Button type="button" variant="outline" size="sm" data-testid={`${path}-wrap-not`} onClick={() => onChange({ not: node })}>
          Not
        </Button>
        {!isRoot && onRemove && (
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Remove group" onClick={onRemove}>
            <Trash2 />
          </Button>
        )}
      </div>
      <div className="flex flex-col gap-2 border-l-2 border-border pl-3">
        {children.length === 0 && <p className="text-sm text-muted-foreground">Add a condition.</p>}
        {children.map((child, i) => (
          <NodeEditor
            key={i}
            node={child}
            path={`${path}.${mode}${i}`}
            onChange={(next) => setChildren(children.map((c, j) => (j === i ? next : c)))}
            onRemove={() => setChildren(children.filter((_, j) => j !== i))}
          />
        ))}
      </div>
    </div>
  )
}

function ConditionRow({
  node,
  path,
  onChange,
  onRemove,
}: {
  node: QueryNode
  path: string
  onChange: (next: QueryNode) => void
  onRemove?: () => void
}) {
  // Never substitute a known field for the one the saved query names: an
  // unknown field used to render as Kind, and any edit then rewrote the
  // condition to kind=movie, silently changing what a collection plays.
  const def = fieldDef(node.field)
  // A field definition with a closed option list still renders a control, so
  // an operator can pick one; an operator that is not in the list is kept as
  // saved rather than replaced.
  const supportedOps = def ? opsFor(def.type) : []
  const ops: QueryOp[] = node.op && !supportedOps.includes(node.op) ? [node.op, ...supportedOps] : supportedOps
  const op: QueryOp | undefined = node.op ?? supportedOps[0]

  function changeField(name: string) {
    const nextDef = fieldDef(name)
    // Only picking a field rewrites the row; a select that reports something
    // else (the unknown field it is already showing) is not an edit.
    if (!nextDef) return
    const nextOps = opsFor(nextDef.type)
    const nextOp = node.op && nextOps.includes(node.op) ? node.op : nextOps[0]
    onChange({ field: name, op: nextOp, value: defaultValue(nextDef, nextOp) })
  }

  function changeOp(next: QueryOp) {
    if (!def || !op) return
    onChange({ ...node, op: next, value: convertValue(def, op, next, node.value) })
  }

  return (
    <div className="flex flex-col gap-1" data-testid={`${path}-condition`}>
      <div className="flex flex-wrap items-start gap-2">
        <Select value={node.field ?? ''} onValueChange={changeField}>
          <SelectTrigger className="w-52" size="sm" aria-label="Field" data-testid={`${path}-field`}>
            <SelectValue placeholder="Choose a field" />
          </SelectTrigger>
          <SelectContent>
            {/* Listed so the row reads what the query actually says, and
                disabled so it cannot be picked as if it were supported. */}
            {!def && node.field && (
              <SelectItem value={node.field} disabled>
                {node.field} (not editable here)
              </SelectItem>
            )}
            {FIELDS.map((f) => (
              <SelectItem key={f.name} value={f.name}>
                {f.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {def && op && ops.length > 1 && (
          <Select value={op} onValueChange={(v) => changeOp(v as QueryOp)}>
            <SelectTrigger className="w-44" size="sm" aria-label="Operator" data-testid={`${path}-op`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ops.map((o) => (
                <SelectItem key={o} value={o}>
                  {opLabel(def, o)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {def && op && ops.length <= 1 && (
          <span className="flex h-7 items-center px-1 text-sm text-muted-foreground">{opLabel(def, op)}</span>
        )}
        {!def && node.field && (
          <span className="flex h-7 items-center px-1 text-sm text-muted-foreground" data-testid={`${path}-saved`}>
            {node.op ? (OP_LABELS[node.op] ?? node.op) : 'is'} {rawValueText(node.value)}
          </span>
        )}
        {def && op && (
          <ValueInput def={def} op={op} value={node.value} path={path} onChange={(value) => onChange({ ...node, op, value })} />
        )}
        <Button type="button" variant="outline" size="sm" data-testid={`${path}-wrap-not`} onClick={() => onChange({ not: node })}>
          Not
        </Button>
        {onRemove && (
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Remove condition" data-testid={`${path}-remove`} onClick={onRemove}>
            <Trash2 />
          </Button>
        )}
      </div>
      {def?.hint && <span className="pl-1 text-xs text-muted-foreground">{def.hint}</span>}
      {!node.field && (
        <span className="pl-1 text-xs text-muted-foreground">Pick a field to finish this condition.</span>
      )}
      {!def && node.field && (
        <span className="pl-1 text-xs text-muted-foreground">
          This field is not one the editor knows, so it is shown as saved. Choose a field to replace it.
        </span>
      )}
    </div>
  )
}

// What an unknown field's value reads as, without pretending to know its type.
function rawValueText(value: unknown): string {
  if (value === undefined || value === null) return ''
  if (Array.isArray(value)) return value.map((v) => rawValueText(v)).join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function opLabel(def: FieldDef, op: QueryOp): string {
  if (def.type === 'date') {
    return { equals: 'is on', lt: 'is before', lte: 'is on or before', gt: 'is after', gte: 'is on or after', between: 'is between', in: OP_LABELS.in, contains: OP_LABELS.contains }[op]
  }
  if (def.type === 'number') {
    return { equals: 'equals', lt: 'less than', lte: 'at most', gt: 'more than', gte: 'at least', between: 'between', in: OP_LABELS.in, contains: OP_LABELS.contains }[op]
  }
  return OP_LABELS[op]
}

// Carries the entered value over when the operator changes shape.
function convertValue(def: FieldDef, from: QueryOp, to: QueryOp, value: unknown): unknown {
  if (from === to) return value
  const first = Array.isArray(value) ? (value[0] ?? '') : (value ?? '')
  if (to === 'in') return first === '' ? [] : [first]
  if (to === 'between') return [first, '']
  if (Array.isArray(value)) return first === '' && def.options ? def.options[0].value : first
  return value
}

function ValueInput({
  def,
  op,
  value,
  path,
  onChange,
}: {
  def: FieldDef
  op: QueryOp
  value: unknown
  path: string
  onChange: (value: unknown) => void
}) {
  const testId = `${path}-value`
  if (op === 'in') {
    return <ListInput value={Array.isArray(value) ? (value as string[]) : []} onChange={onChange} testId={testId} placeholder="one, two, three" />
  }
  if (op === 'between') {
    const pair = Array.isArray(value) ? value : ['', '']
    return (
      <div className="flex items-center gap-2">
        <ScalarInput def={def} value={pair[0]} onChange={(v) => onChange([v, pair[1] ?? ''])} testId={`${testId}-from`} />
        <span className="text-sm text-muted-foreground">and</span>
        <ScalarInput def={def} value={pair[1]} onChange={(v) => onChange([pair[0] ?? '', v])} testId={`${testId}-to`} />
      </div>
    )
  }
  return <ScalarInput def={def} value={value} onChange={onChange} testId={testId} />
}

function ScalarInput({
  def,
  value,
  onChange,
  testId,
}: {
  def: FieldDef
  value: unknown
  onChange: (value: unknown) => void
  testId: string
}) {
  if (def.options) {
    return (
      <Select value={String(value || def.options[0].value)} onValueChange={onChange}>
        <SelectTrigger className="w-36" size="sm" aria-label={def.label} data-testid={testId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {def.options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }
  if (def.type === 'number') {
    return (
      <div className="flex items-center gap-1">
        <Input
          type="number"
          step={def.integer ? 1 : 'any'}
          className="h-7 w-28"
          aria-label={def.label}
          data-testid={testId}
          value={value === '' || value === undefined || value === null ? '' : String(value)}
          onChange={(e) => onChange(e.target.value === '' ? '' : Number(e.target.value))}
        />
        {def.unit && <span className="text-xs text-muted-foreground">{def.unit}</span>}
      </div>
    )
  }
  if (def.type === 'date') {
    return (
      <Input
        type="date"
        className="h-7 w-40"
        aria-label={def.label}
        data-testid={testId}
        value={typeof value === 'string' ? value : ''}
        onChange={(e) => onChange(e.target.value)}
      />
    )
  }
  return (
    <Input
      className="h-7 w-52"
      aria-label={def.label}
      data-testid={testId}
      value={typeof value === 'string' ? value : ''}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

// Comma-separated list input: keeps its own text so typing a comma is not
// swallowed by the parse, and re-syncs when the tree changes from outside.
function ListInput({
  value,
  onChange,
  testId,
  placeholder,
}: {
  value: string[]
  onChange: (value: string[]) => void
  testId: string
  placeholder?: string
}) {
  const [text, setText] = useState(value.join(', '))
  useEffect(() => {
    if (JSON.stringify(parseList(text)) !== JSON.stringify(value)) setText(value.join(', '))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(value)])
  return (
    <Input
      className="h-7 w-64"
      aria-label="Values, comma separated"
      data-testid={testId}
      placeholder={placeholder}
      value={text}
      onChange={(e) => {
        setText(e.target.value)
        onChange(parseList(e.target.value))
      }}
    />
  )
}

function parseList(text: string): string[] {
  return text
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}
