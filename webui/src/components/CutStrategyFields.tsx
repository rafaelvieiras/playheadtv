import { Plus, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  CUT_FALLBACK_TYPES,
  CUT_ROLE_LABELS,
  CUT_STRATEGY_LABELS,
  MAX_CUT_INTERVAL_MINUTES,
  MAX_CUT_OFFSETS,
  MAX_CUT_PERCENTAGES,
  MAX_CUT_PHASES,
  MIN_CUT_INTERVAL_MINUTES,
  emptyPhase,
  withType,
  type PhaseBound,
  type StrategyDraft,
} from '@/lib/cutRules'
import type { CutChapterRole, CutStrategyType } from '@/lib/types'

const ALL_TYPES = Object.keys(CUT_STRATEGY_LABELS) as CutStrategyType[]

// CutStrategyFields edits one strategy draft: the type and the fields of that
// type. A chapter strategy nests a second instance for its fallback (which
// cannot itself be a chapter strategy).
export function CutStrategyFields({
  id,
  value,
  onChange,
  types = ALL_TYPES,
  errors,
}: {
  // Prefix of every element id / test id inside.
  id: string
  value: StrategyDraft
  onChange: (next: StrategyDraft) => void
  types?: CutStrategyType[]
  // Problems by element id, from the last submit (lib/cutRules draftProblems).
  errors?: Record<string, string>
}) {
  function patch(next: Partial<StrategyDraft>) {
    onChange({ ...value, ...next })
  }
  const phases = value.phases
  // error reads the message of one control inside this strategy.
  const error = (suffix: string): string | undefined => errors?.[`${id}-${suffix}`]
  // fieldError is the aria wiring of one control, keyed by the same suffix.
  const fieldError = (suffix: string) => {
    const message = error(suffix)
    return {
      invalid: !!message,
      id: `${id}-${suffix}-error`,
      props: {
        'aria-invalid': !!message,
        'aria-describedby': message ? `${id}-${suffix}-error` : undefined,
      },
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <Field data-invalid={fieldError('type').invalid}>
        <FieldLabel htmlFor={`${id}-type`}>Cut</FieldLabel>
        <Select value={value.type} onValueChange={(v) => onChange(withType(value, v as CutStrategyType))}>
          <SelectTrigger
            id={`${id}-type`}
            data-testid={`${id}-type`}
            className="w-full"
            {...fieldError('type').props}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {types.map((t) => (
              <SelectItem key={t} value={t}>
                {CUT_STRATEGY_LABELS[t]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldError id={fieldError('type').id}>{error('type')}</FieldError>
      </Field>

      {value.type === 'interval' && (
        <Field data-invalid={fieldError('every').invalid}>
          <FieldLabel htmlFor={`${id}-every`}>Every (minutes)</FieldLabel>
          <Input
            id={`${id}-every`}
            data-testid={`${id}-every`}
            inputMode="decimal"
            value={value.every_minutes}
            onChange={(e) => patch({ every_minutes: e.target.value })}
            {...fieldError('every').props}
          />
          <FieldError id={fieldError('every').id}>{error('every')}</FieldError>
          <FieldDescription>
            A break every this many minutes from the start ({MIN_CUT_INTERVAL_MINUTES} to{' '}
            {MAX_CUT_INTERVAL_MINUTES}, a day at most).
          </FieldDescription>
        </Field>
      )}

      {value.type === 'percentage' && (
        <Field data-invalid={fieldError('percentages').invalid}>
          <FieldLabel htmlFor={`${id}-percentages`}>Percentages of the duration</FieldLabel>
          <Input
            id={`${id}-percentages`}
            data-testid={`${id}-percentages`}
            value={value.percentages}
            placeholder="50"
            onChange={(e) => patch({ percentages: e.target.value })}
            {...fieldError('percentages').props}
          />
          <FieldError id={fieldError('percentages').id}>{error('percentages')}</FieldError>
          <FieldDescription>
            One or more values above 0 and below 100 (at most {MAX_CUT_PERCENTAGES}), comma separated, in
            ascending order with no repeats.
          </FieldDescription>
        </Field>
      )}

      {value.type === 'offsets' && (
        <Field data-invalid={fieldError('offsets').invalid}>
          <FieldLabel htmlFor={`${id}-offsets`}>Cut times (minutes from the start)</FieldLabel>
          <Input
            id={`${id}-offsets`}
            data-testid={`${id}-offsets`}
            value={value.offsets_minutes}
            placeholder="20, 50"
            onChange={(e) => patch({ offsets_minutes: e.target.value })}
            {...fieldError('offsets').props}
          />
          <FieldError id={fieldError('offsets').id}>{error('offsets')}</FieldError>
          <FieldDescription>
            Ascending minutes above 0, at most {MAX_CUT_OFFSETS} of them. Times past the end of an item are
            ignored.
          </FieldDescription>
        </Field>
      )}

      {value.type === 'phased' && (
        <div className="flex flex-col gap-3">
          <Field>
            <FieldLabel htmlFor={`${id}-bound`}>Each phase ends</FieldLabel>
            <Select value={value.phase_bound} onValueChange={(v) => patch({ phase_bound: v as PhaseBound })}>
              <SelectTrigger id={`${id}-bound`} data-testid={`${id}-bound`} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="before_end">A number of minutes before the end of the item</SelectItem>
                <SelectItem value="from_start">A number of minutes after the start of the item</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {phases.map((p, i) => {
            const last = i === phases.length - 1
            return (
              <div key={p.key} className="flex flex-wrap items-end gap-2" data-testid={`${id}-phase-${i}`}>
                <Field className="w-32" data-invalid={fieldError(`phase-${i}-every`).invalid}>
                  <FieldLabel htmlFor={`${id}-phase-${i}-every`}>{i === 0 ? 'Every (min)' : 'Then every (min)'}</FieldLabel>
                  <Input
                    id={`${id}-phase-${i}-every`}
                    data-testid={`${id}-phase-${i}-every`}
                    inputMode="decimal"
                    value={p.every_minutes}
                    onChange={(e) =>
                      patch({ phases: phases.map((q, j) => (j === i ? { ...q, every_minutes: e.target.value } : q)) })
                    }
                    {...fieldError(`phase-${i}-every`).props}
                  />
                  <FieldError id={fieldError(`phase-${i}-every`).id}>{error(`phase-${i}-every`)}</FieldError>
                </Field>
                {last ? (
                  <span className="pb-2 text-sm text-muted-foreground">until the end</span>
                ) : (
                  <Field className="w-44" data-invalid={fieldError(`phase-${i}-until`).invalid}>
                    <FieldLabel htmlFor={`${id}-phase-${i}-until`}>
                      {value.phase_bound === 'before_end' ? 'Until the last (min)' : 'Until minute'}
                    </FieldLabel>
                    <Input
                      id={`${id}-phase-${i}-until`}
                      data-testid={`${id}-phase-${i}-until`}
                      inputMode="decimal"
                      value={p.until_minutes}
                      onChange={(e) =>
                        patch({ phases: phases.map((q, j) => (j === i ? { ...q, until_minutes: e.target.value } : q)) })
                      }
                      {...fieldError(`phase-${i}-until`).props}
                    />
                    <FieldError id={fieldError(`phase-${i}-until`).id}>{error(`phase-${i}-until`)}</FieldError>
                  </Field>
                )}
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove phase ${i + 1}`}
                  data-testid={`${id}-phase-${i}-remove`}
                  disabled={phases.length === 1}
                  onClick={() => patch({ phases: phases.filter((_, j) => j !== i) })}
                >
                  <Trash2 />
                </Button>
              </div>
            )
          })}
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid={`${id}-add-phase`}
              disabled={phases.length >= MAX_CUT_PHASES}
              onClick={() => {
                // The new phase becomes the last one: the old last phase needs an end.
                patch({ phases: [...phases, emptyPhase('5')] })
              }}
            >
              <Plus /> Add phase
            </Button>
          </div>
          <FieldError id={`${id}-phases-error`}>{error('phases')}</FieldError>
          <FieldDescription>
            Each phase cuts at its cadence ({MIN_CUT_INTERVAL_MINUTES} to {MAX_CUT_INTERVAL_MINUTES} minutes), counted
            from where the previous phase ended. The last phase runs to the end of the item.
          </FieldDescription>
        </div>
      )}

      {value.type === 'chapter' && (
        <div className="flex flex-col gap-3">
          <fieldset className="flex flex-col gap-2" data-invalid={fieldError('roles').invalid}>
            <legend className="mb-1 text-sm font-medium">Cut at</legend>
            {(Object.keys(CUT_ROLE_LABELS) as CutChapterRole[]).map((role) => (
              <label key={role} className="flex items-center gap-2 text-sm">
                <Checkbox
                  data-testid={`${id}-role-${role}`}
                  checked={value.roles.includes(role)}
                  onCheckedChange={(on) =>
                    patch({ roles: on === true ? [...value.roles, role] : value.roles.filter((r) => r !== role) })
                  }
                />
                {CUT_ROLE_LABELS[role]}
              </label>
            ))}
            <FieldError id={fieldError('roles').id}>{error('roles')}</FieldError>
            <FieldDescription>
              The middle is snapped to the nearest chapter boundary within the snap distance of the rule (5 minutes by
              default). After the intro is the end of the item's intro marker from the media server (Jellyfin intro
              segment) when it has one; otherwise the chapter after the intro chapter.
            </FieldDescription>
          </fieldset>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              data-testid={`${id}-fallback-enabled`}
              checked={value.fallback_enabled}
              onCheckedChange={(on) => patch({ fallback_enabled: on === true })}
            />
            Use another cut when the item has no intro marker or chapters
          </label>
          {value.fallback_enabled && value.fallback && (
            <div className="rounded-lg border p-3" data-testid={`${id}-fallback`}>
              <CutStrategyFields
                id={`${id}-fallback`}
                value={value.fallback}
                types={CUT_FALLBACK_TYPES}
                errors={errors}
                onChange={(next) => patch({ fallback: next })}
              />
            </div>
          )}
          {!value.fallback_enabled && (
            <FieldDescription>
              Without it, an item with no intro marker or chapters gets one break in the middle (never none for lack of
              data).
            </FieldDescription>
          )}
        </div>
      )}
    </div>
  )
}
