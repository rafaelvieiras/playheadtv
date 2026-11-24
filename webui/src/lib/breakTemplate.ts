// Helpers for the break template editor: the draft model (steps edited in
// minutes), validation mirroring the backend limits, and the conversion to the
// request body.
import { MAX_FIT_ATTEMPTS_MAX, MAX_FIT_ATTEMPTS_MIN, validMaxFitAttempts } from './filler.ts'
import type {
  BreakKind,
  BreakStepInput,
  BreakStepType,
  BreakTemplate,
  BreakTemplateInput,
  FillerScope,
  SelectionStrategy,
} from '@/lib/types'

export const BREAK_KIND_LABELS: Record<BreakKind, string> = {
  preroll: 'PreRoll (before a program)',
  midroll: 'MidRoll (between programs)',
  postroll: 'PostRoll (after a program)',
}

export const STEP_TYPE_LABELS: Record<BreakStepType, string> = {
  single: 'Single item',
  fill: 'Fill a length',
}

// Bounds of a break template, mirrored from backend/internal/domain/break_template.go
// and the checks in backend/internal/breaktemplate.
export const MAX_BREAK_STEPS = 10
export const MAX_BREAK_NAME = 100
// domain.MaxBreakTargetMs (one hour), kept in both units the editor needs.
const MAX_BREAK_TARGET_MS = 60 * 60 * 1000
export const MAX_BREAK_TARGET_MINUTES = MAX_BREAK_TARGET_MS / 60000

export interface StepDraft {
  // Stable key for the list, never sent.
  key: string
  step_type: BreakStepType
  filler_pool_id: string
  selection_strategy: SelectionStrategy
  // Fill steps, in minutes (the author thinks in minutes; the API takes ms).
  target_minutes: number
  max_fit_attempts: number
}

export interface TemplateDraft {
  scope_type: FillerScope
  scope_id: string
  kind: BreakKind
  name: string
  position: number
  steps: StepDraft[]
}

let keyCounter = 0
export function newStepKey(): string {
  keyCounter += 1
  return `step-${keyCounter}`
}

export function emptyStep(step_type: BreakStepType = 'single'): StepDraft {
  return {
    key: newStepKey(),
    step_type,
    filler_pool_id: '',
    selection_strategy: 'sequential',
    target_minutes: 4,
    max_fit_attempts: 1,
  }
}

export function emptyDraft(): TemplateDraft {
  return { scope_type: 'channel', scope_id: '', kind: 'midroll', name: '', position: 0, steps: [emptyStep()] }
}

export function templateToDraft(t: BreakTemplate): TemplateDraft {
  return {
    scope_type: t.scope_type,
    scope_id: t.scope_id,
    kind: t.kind,
    name: t.name,
    position: t.position,
    steps: t.steps.map((s) => ({
      key: newStepKey(),
      step_type: s.step_type,
      filler_pool_id: s.filler_pool_id,
      selection_strategy: s.selection_strategy,
      target_minutes: s.step_type === 'fill' ? s.target_ms / 60000 : 4,
      max_fit_attempts: s.max_fit_attempts || 1,
    })),
  }
}

// sameDraft compares two drafts ignoring the step keys (which differ between
// a loaded template and its copy being edited).
export function sameDraft(a: TemplateDraft, b: TemplateDraft): boolean {
  const strip = (d: TemplateDraft) => ({
    ...d,
    // Unused fields of a single step never count as an edit.
    steps: d.steps.map(({ key: _key, ...s }) =>
      s.step_type === 'single' ? { ...s, target_minutes: 0, max_fit_attempts: 1 } : s,
    ),
  })
  return JSON.stringify(strip(a)) === JSON.stringify(strip(b))
}

export function targetMs(minutes: number): number {
  return Math.round(minutes * 60000)
}

// TemplateProblem is one thing the author has to fix. id is the id of the
// control the message belongs under ('' for the template as a whole), so one
// submit can mark every field instead of reporting the first problem at the
// bottom of the page.
export interface TemplateProblem {
  id: string
  message: string
}

// draftProblems reports every problem the backend would reject, each bound to
// the control that has to change.
export function draftProblems(d: TemplateDraft): TemplateProblem[] {
  const problems: TemplateProblem[] = []
  if (!d.scope_id) {
    problems.push({ id: 'bt-scope-target', message: d.scope_type === 'channel' ? 'Select a channel.' : 'Select a block.' })
  }
  if (!Number.isInteger(d.position)) problems.push({ id: 'bt-position', message: 'Position must be a whole number.' })
  if (d.name.length > MAX_BREAK_NAME) {
    problems.push({ id: 'bt-name', message: `The name must be at most ${MAX_BREAK_NAME} characters.` })
  }
  if (d.steps.length === 0) {
    problems.push({ id: 'bt-steps', message: 'Add at least one step.' })
  } else if (d.steps.length > MAX_BREAK_STEPS) {
    problems.push({ id: 'bt-steps', message: `A template holds at most ${MAX_BREAK_STEPS} steps.` })
  }
  for (let i = 0; i < d.steps.length; i++) {
    const s = d.steps[i]
    const id = `bt-step-${i}`
    if (!s.filler_pool_id) problems.push({ id: `${id}-pool`, message: 'Select a filler pool.' })
    if (s.step_type !== 'fill') continue
    const ms = targetMs(s.target_minutes)
    if (!Number.isFinite(ms) || ms < 1 || ms > MAX_BREAK_TARGET_MS) {
      problems.push({
        id: `${id}-target`,
        message: `The length must be more than 0 and at most ${MAX_BREAK_TARGET_MINUTES} minutes.`,
      })
    }
    if (!validMaxFitAttempts(s.max_fit_attempts)) {
      problems.push({
        id: `${id}-attempts`,
        message: `Fit attempts must be a whole number from ${MAX_FIT_ATTEMPTS_MIN} to ${MAX_FIT_ATTEMPTS_MAX}.`,
      })
    }
  }
  return problems
}

// problemsById indexes the problems by control id (the first message per
// control wins), the shape the fields read.
export function problemsById(problems: TemplateProblem[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of problems) {
    if (p.id && !(p.id in out)) out[p.id] = p.message
  }
  return out
}

// mapTemplateMessage binds a refusal from the server ("step 2: target_ms must
// be between 1 and 3600000") back to the control that has to change, in
// minutes. null when the message is about the template as a whole or unknown.
export function mapTemplateMessage(message: string): TemplateProblem | null {
  const text = message.trim()
  const step = /^step (\d+): (.*)$/.exec(text)
  if (step) return mapStepMessage(Number(step[1]) - 1, step[2])
  if (text.startsWith('steps ')) {
    return { id: 'bt-steps', message: `A template holds 1 to ${MAX_BREAK_STEPS} steps.` }
  }
  if (text.startsWith('name ')) return { id: 'bt-name', message: `The name must be at most ${MAX_BREAK_NAME} characters.` }
  if (text.startsWith('kind ')) return { id: 'bt-kind', message: 'Pick a boundary.' }
  if (text.startsWith('scope_')) {
    return { id: 'bt-scope-target', message: 'Pick the channel or block this template applies to.' }
  }
  return null
}

function mapStepMessage(index: number, body: string): TemplateProblem {
  const id = `bt-step-${index}`
  if (body.startsWith('target_ms')) {
    return { id: `${id}-target`, message: `The length must be more than 0 and at most ${MAX_BREAK_TARGET_MINUTES} minutes.` }
  }
  if (body.startsWith('max_fit_attempts') || body.includes('only apply to fill steps')) {
    return {
      id: `${id}-attempts`,
      message: `Fit attempts must be a whole number from ${MAX_FIT_ATTEMPTS_MIN} to ${MAX_FIT_ATTEMPTS_MAX}.`,
    }
  }
  if (body.startsWith('filler_pool_id')) return { id: `${id}-pool`, message: 'Select a filler pool.' }
  if (body.includes('does not exist')) {
    return { id: `${id}-pool`, message: 'This filler pool no longer exists; pick another one.' }
  }
  if (body.startsWith('selection_strategy')) return { id: `${id}-selection`, message: 'Pick a selection strategy.' }
  if (body.startsWith('step_type')) return { id: `${id}-type`, message: 'Pick a step type.' }
  return { id: `${id}-pool`, message: 'This step cannot be saved as entered.' }
}

// validateDraft returns the first problem the backend would reject, or null.
export function validateDraft(d: TemplateDraft): string | null {
  const problems = draftProblems(d)
  return problems.length > 0 ? problems[0].message : null
}

export function draftToInput(d: TemplateDraft): BreakTemplateInput {
  const steps: BreakStepInput[] = d.steps.map((s) =>
    s.step_type === 'fill'
      ? {
          step_type: 'fill',
          filler_pool_id: s.filler_pool_id,
          selection_strategy: s.selection_strategy,
          target_ms: targetMs(s.target_minutes),
          max_fit_attempts: s.max_fit_attempts,
        }
      : { step_type: 'single', filler_pool_id: s.filler_pool_id, selection_strategy: s.selection_strategy },
  )
  return { scope_type: d.scope_type, scope_id: d.scope_id, kind: d.kind, name: d.name.trim(), position: d.position, steps }
}

// moveStep returns the steps with the one at index shifted by delta (-1 up,
// +1 down); out-of-range moves return the list unchanged.
export function moveStep<T>(steps: T[], index: number, delta: number): T[] {
  const to = index + delta
  if (index < 0 || index >= steps.length || to < 0 || to >= steps.length) return steps
  const next = steps.slice()
  ;[next[index], next[to]] = [next[to], next[index]]
  return next
}

// minutesLabel renders a length in minutes without float noise ("4", "1.5").
function minutesLabel(ms: number): string {
  const minutes = Math.round((ms / 60000) * 10) / 10
  return Number.isInteger(minutes) ? String(minutes) : minutes.toFixed(1)
}

// summarizeDraft is the read-only summary of what the steps air: how many
// whole items, plus the exact total of the fills. Only those two are known
// without reading a pool, so it never claims a total running time.
export function summarizeDraft(d: TemplateDraft): string {
  const count = d.steps.length
  if (count === 0) return 'No steps yet.'
  let items = 0
  let fillMs = 0
  let broken = false
  for (const s of d.steps) {
    if (s.step_type === 'single') {
      items += 1
      continue
    }
    const ms = targetMs(s.target_minutes)
    if (!Number.isFinite(ms) || ms < 1) broken = true
    else fillMs += ms
  }
  const parts: string[] = []
  if (fillMs > 0) parts.push(`~${minutesLabel(fillMs)} min of fills`)
  if (items > 0) parts.push(`${items} item${items === 1 ? '' : 's'}`)
  if (broken) parts.push('a fill length still to fix')
  const body = parts.length > 0 ? parts.join(' + ') : 'nothing'
  return `${count} step${count === 1 ? '' : 's'}: ${body}.`
}

// describeSteps is the one-line summary of a template for lists:
// "1 item, 4 min fill, 1 item".
export function describeSteps(t: Pick<BreakTemplate, 'steps'>): string {
  return t.steps
    .map((s) => {
      if (s.step_type === 'single') return '1 item'
      const minutes = s.target_ms / 60000
      return `${Number.isInteger(minutes) ? minutes : minutes.toFixed(1)} min fill`
    })
    .join(' → ')
}
