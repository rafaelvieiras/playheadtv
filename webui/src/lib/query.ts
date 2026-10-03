// The composable catalog query: a versioned JSON condition tree
// (backend/internal/catalogsync/query.go). The same tree is the criteria of a
// dynamic block, of a dynamic filler pool and of a criteria block item.

export type QueryOp = 'equals' | 'in' | 'contains' | 'lt' | 'lte' | 'gt' | 'gte' | 'between'

// One level of the tree: exactly one of all / any / not / a field condition.
export interface QueryNode {
  all?: QueryNode[]
  any?: QueryNode[]
  not?: QueryNode
  field?: string
  op?: QueryOp
  value?: unknown
}

export interface Query extends QueryNode {
  version: 1
}

export type FieldType = 'list' | 'text' | 'enum' | 'number' | 'date'

export interface FieldDef {
  name: string
  label: string
  type: FieldType
  // Shown after the number input ("minutes"), purely informative.
  unit?: string
  // Fixed choices for an enum field with a closed set (kind).
  options?: { value: string; label: string }[]
  // Whole numbers only.
  integer?: boolean
  hint?: string
}

// The queryable fields, in the order the picker lists them. Mirrors
// queryFields in the backend; the backend validates again on save.
export const FIELDS: FieldDef[] = [
  { name: 'kind', label: 'Kind', type: 'enum', options: [{ value: 'movie', label: 'Movie' }, { value: 'episode', label: 'Episode' }] },
  { name: 'genre', label: 'Genre', type: 'list', hint: 'Exact name, ignoring case and accents.' },
  { name: 'studio', label: 'Studio', type: 'list', hint: 'Exact name, ignoring case and accents.' },
  { name: 'tag', label: 'Tag', type: 'list', hint: 'Exact name, ignoring case and accents.' },
  { name: 'audio_language', label: 'Audio language', type: 'list' },
  { name: 'subtitle_language', label: 'Subtitle language', type: 'list' },
  { name: 'title', label: 'Title', type: 'text', hint: 'Also matches the series title.' },
  { name: 'overview', label: 'Overview', type: 'text' },
  { name: 'person', label: 'Person (cast or director)', type: 'text' },
  { name: 'actor', label: 'Actor (cast only)', type: 'text' },
  { name: 'director', label: 'Director (directors only)', type: 'text' },
  { name: 'content_rating', label: 'Content rating', type: 'enum', hint: 'Exact value, for example PG-13.' },
  { name: 'year', label: 'Year', type: 'number', integer: true },
  { name: 'duration', label: 'Duration', type: 'number', unit: 'minutes' },
  { name: 'community_rating', label: 'Community rating', type: 'number' },
  { name: 'critic_rating', label: 'Critic rating', type: 'number' },
  { name: 'season_number', label: 'Season number', type: 'number', integer: true, hint: '0 is specials.' },
  { name: 'episode_number', label: 'Episode number', type: 'number', integer: true },
  { name: 'premiere_date', label: 'Premiere date', type: 'date' },
  { name: 'date_added', label: 'Date added', type: 'date' },
]

const FIELD_BY_NAME = new Map(FIELDS.map((f) => [f.name, f]))

export function fieldDef(name: string | undefined): FieldDef | undefined {
  return name ? FIELD_BY_NAME.get(name) : undefined
}

export const OP_LABELS: Record<QueryOp, string> = {
  equals: 'is',
  in: 'is any of',
  contains: 'contains',
  lt: 'is before / less than',
  lte: 'is at most',
  gt: 'is after / more than',
  gte: 'is at least',
  between: 'is between',
}

export function opsFor(type: FieldType): QueryOp[] {
  switch (type) {
    case 'list':
    case 'enum':
      return ['equals', 'in']
    case 'text':
      return ['contains']
    default:
      return ['equals', 'lt', 'lte', 'gt', 'gte', 'between']
  }
}

// A fresh value for a field and operator.
export function defaultValue(def: FieldDef, op: QueryOp): unknown {
  if (op === 'in') return []
  if (op === 'between') return ['', '']
  if (def.options) return def.options[0].value
  return ''
}

export function emptyCondition(): QueryNode {
  return { field: 'genre', op: 'equals', value: '' }
}

// The query a new dynamic block or pool starts from.
export function emptyQuery(): Query {
  return { version: 1, all: [{ field: 'kind', op: 'equals', value: 'movie' }] }
}

export function isGroup(n: QueryNode): boolean {
  return n.all !== undefined || n.any !== undefined
}

function isBlank(v: unknown): boolean {
  return v === '' || v === null || v === undefined || (typeof v === 'string' && v.trim() === '')
}

function validateCondition(n: QueryNode, path: string): string | undefined {
  const def = fieldDef(n.field)
  if (!def) return `${path}: choose a field.`
  if (!n.op || !opsFor(def.type).includes(n.op)) return `${path}: choose an operator for ${def.label}.`
  const label = `${path} (${def.label})`
  if (n.op === 'in') {
    const list = Array.isArray(n.value) ? n.value : []
    if (list.length === 0 || list.some(isBlank)) return `${label}: enter at least one value.`
    return undefined
  }
  if (n.op === 'between') {
    const pair = Array.isArray(n.value) ? n.value : []
    if (pair.length !== 2 || isBlank(pair[0]) || isBlank(pair[1])) return `${label}: enter both ends of the range.`
    if (def.type === 'number' && Number(pair[0]) > Number(pair[1])) return `${label}: the range start is after its end.`
    if (def.type === 'date' && String(pair[0]) > String(pair[1])) return `${label}: the range start is after its end.`
    return undefined
  }
  if (isBlank(n.value)) return `${label}: enter a value.`
  if (def.type === 'number') {
    const x = Number(n.value)
    if (!Number.isFinite(x)) return `${label}: enter a number.`
    if (def.integer && !Number.isInteger(x)) return `${label}: enter a whole number.`
  }
  return undefined
}

function validateNode(n: QueryNode, path: string): string | undefined {
  if (n.all !== undefined || n.any !== undefined) {
    const kind = n.all !== undefined ? 'all' : 'any'
    const children = (n.all ?? n.any) as QueryNode[]
    if (children.length === 0) return `${path}: a group needs at least one condition.`
    for (let i = 0; i < children.length; i++) {
      const err = validateNode(children[i], `${path} › ${kind === 'all' ? 'all' : 'any'} #${i + 1}`)
      if (err) return err
    }
    return undefined
  }
  if (n.not !== undefined) return validateNode(n.not, `${path} › not`)
  return validateCondition(n, path)
}

// A message for the first problem in the draft (the backend repeats these
// checks), or undefined when the query can be saved. null (no query) is valid.
export function validateQuery(q: Query | null): string | undefined {
  if (!q) return undefined
  return validateNode(q, 'Query')
}

function formatValue(def: FieldDef | undefined, v: unknown): string {
  if (Array.isArray(v)) return v.map((x) => formatValue(def, x)).join(', ')
  if (def?.options) return def.options.find((o) => o.value === v)?.label ?? String(v)
  return String(v)
}

function describeCondition(n: QueryNode): string {
  const def = fieldDef(n.field)
  const name = def?.label.toLowerCase() ?? n.field ?? '?'
  const unit = def?.unit ? ` ${def.unit}` : ''
  const v = formatValue(def, n.value)
  switch (n.op) {
    case 'equals':
      return def?.type === 'number' || def?.type === 'date' ? `${name} = ${v}${unit}` : `${name}: ${v}`
    case 'in':
      return `${name}: ${v}`
    case 'contains':
      return `${name} contains "${v}"`
    case 'lt':
      return `${name} < ${v}${unit}`
    case 'lte':
      return `${name} ≤ ${v}${unit}`
    case 'gt':
      return `${name} > ${v}${unit}`
    case 'gte':
      return `${name} ≥ ${v}${unit}`
    case 'between': {
      const pair = Array.isArray(n.value) ? n.value : []
      return `${name} ${pair[0]}–${pair[1]}${unit}`
    }
  }
  return name
}

function describeNode(n: QueryNode, nested: boolean): string {
  if (n.all !== undefined || n.any !== undefined) {
    const kids = (n.all ?? n.any) as QueryNode[]
    const text = kids.map((k) => describeNode(k, true)).join(n.all !== undefined ? ' and ' : ' or ')
    return nested && kids.length > 1 ? `(${text})` : text
  }
  if (n.not !== undefined) return `not ${describeNode(n.not, true)}`
  return describeCondition(n)
}

// A one-line description of a query for lists ("kind: Movie and genre: Horror
// and duration < 90 minutes").
export function summarizeQuery(q: Query | QueryNode): string {
  return describeNode(q, false)
}

export function queryKey(q: Query | null): string {
  return q ? JSON.stringify(q) : ''
}
