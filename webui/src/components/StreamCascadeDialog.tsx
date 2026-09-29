import { type FormEvent, useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/http'
import type { StreamPreferenceRule, StreamPreferenceScope, StreamRuleCascade, StreamRuleStep } from '@/lib/types'

const MAX_STEPS = 20

interface StreamCascadeDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  scopeType: StreamPreferenceScope
  // Ignored (and sent as "") for scopeType "global" — there's only ever one
  // global row per tenant. A channel.id for scopeType "channel".
  scopeId: string
  // Human-readable name shown in the dialog title/description, e.g. "Global
  // default" or a channel's "number — name".
  scopeLabel: string
}

// One cascade step's editable form shape: title_blocklist/title_allowlist
// are edited as raw comma-separated text (split into string[] on save)
// rather than a tag input, and subtitle preference is split into an
// enabled flag plus its language so the UI can offer the three-way
// "no subtitle preference at this step" / "any subtitle language" /
// "this subtitle language" choice that StreamRuleStep.subtitle_language
// (null vs. "" vs. a value) represents.
interface StepForm {
  audio_language: string
  subtitle_enabled: boolean
  subtitle_language: string
  title_blocklist: string
  title_allowlist: string
}

function emptyStep(): StepForm {
  return {
    audio_language: '',
    subtitle_enabled: false,
    subtitle_language: '',
    title_blocklist: '',
    title_allowlist: '',
  }
}

function splitList(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}

function stepToForm(step: StreamRuleStep): StepForm {
  return {
    audio_language: step.audio_language,
    subtitle_enabled: step.subtitle_language !== null,
    subtitle_language: step.subtitle_language ?? '',
    title_blocklist: step.title_blocklist.join(', '),
    title_allowlist: step.title_allowlist.join(', '),
  }
}

function formToStep(form: StepForm): StreamRuleStep {
  return {
    audio_language: form.audio_language.trim(),
    subtitle_language: form.subtitle_enabled ? form.subtitle_language.trim() : null,
    title_blocklist: splitList(form.title_blocklist),
    title_allowlist: splitList(form.title_allowlist),
  }
}

function cascadeToForm(cascade: StreamRuleCascade): StepForm[] {
  // The backend encodes an unset cascade's Steps (a nil Go slice) as JSON
  // null, not [] — an unconfigured scope's GET response is
  // {"rules":{"steps":null}}, not {"rules":{"steps":[]}}.
  return (cascade.steps ?? []).map(stepToForm)
}

export function StreamCascadeDialog({ open, onOpenChange, scopeType, scopeId, scopeLabel }: StreamCascadeDialogProps) {
  const queryClient = useQueryClient()
  const effectiveScopeId = scopeType === 'global' ? '' : scopeId
  const queryKey = ['stream-preference-rule', scopeType, effectiveScopeId]

  const { data: rule, isLoading } = useQuery({
    queryKey,
    queryFn: () =>
      api.get<StreamPreferenceRule>(
        `/stream-preferences/config?scope_type=${scopeType}${
          scopeType === 'channel' ? `&scope_id=${encodeURIComponent(effectiveScopeId)}` : ''
        }`,
      ),
    enabled: open,
  })

  const [steps, setSteps] = useState<StepForm[]>([])

  useEffect(() => {
    if (open && rule) {
      setSteps(cascadeToForm(rule.rules))
    }
  }, [open, rule])

  const mutation = useMutation({
    mutationFn: (cascade: StreamRuleCascade) =>
      api.put<StreamPreferenceRule>(
        `/stream-preferences/config?scope_type=${scopeType}${
          scopeType === 'channel' ? `&scope_id=${encodeURIComponent(effectiveScopeId)}` : ''
        }`,
        { rules: cascade },
      ),
    onSuccess: () => {
      // Both this dialog's own query and the page's per-channel/global rows
      // are keyed off "stream-preference-rule" — invalidate the whole
      // prefix so every row reflects the save, not just this one.
      queryClient.invalidateQueries({ queryKey: ['stream-preference-rule'] })
      toast.success('Stream selection preference saved.')
      onOpenChange(false)
    },
    onError: () => {
      toast.error('Could not save the stream selection preference.')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    mutation.mutate({ steps: steps.map(formToStep) })
  }

  function addStep() {
    setSteps((s) => [...s, emptyStep()])
  }

  function removeStep(index: number) {
    setSteps((s) => s.filter((_, i) => i !== index))
  }

  function moveStep(index: number, direction: -1 | 1) {
    setSteps((s) => {
      const target = index + direction
      if (target < 0 || target >= s.length) return s
      const next = [...s]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }

  function updateStep(index: number, patch: Partial<StepForm>) {
    setSteps((s) => s.map((step, i) => (i === index ? { ...step, ...patch } : step)))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Stream selection: {scopeLabel}</DialogTitle>
            <DialogDescription>
              Ordered fallback steps, most-preferred first. Playback picks the first step whose audio (and, if
              requested, subtitle) track is available. An empty cascade means no preference — the player falls back
              to the next tier (or the source's own default track).
            </DialogDescription>
          </DialogHeader>

          {isLoading && <p className="py-4 text-sm text-muted-foreground">Loading…</p>}

          {!isLoading && (
            <FieldGroup className="max-h-[60vh] gap-3 overflow-y-auto py-4">
              {steps.length === 0 && (
                <p className="text-sm text-muted-foreground">No steps yet — add one below.</p>
              )}
              {steps.map((step, index) => (
                <div key={index} className="flex flex-col gap-3 rounded-lg border border-border p-3">
                  <div className="flex items-center justify-between">
                    <span className="text-sm font-medium">Step {index + 1}</span>
                    <div className="flex gap-1">
                      <Button
                        type="button"
                        variant="outline"
                        size="icon-sm"
                        onClick={() => moveStep(index, -1)}
                        disabled={index === 0}
                        aria-label="Move step up"
                      >
                        <ArrowUp />
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="icon-sm"
                        onClick={() => moveStep(index, 1)}
                        disabled={index === steps.length - 1}
                        aria-label="Move step down"
                      >
                        <ArrowDown />
                      </Button>
                      <Button
                        type="button"
                        variant="destructive"
                        size="icon-sm"
                        onClick={() => removeStep(index)}
                        aria-label="Remove step"
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  </div>

                  <Field>
                    <FieldLabel htmlFor={`step-${index}-audio`}>Audio language</FieldLabel>
                    <Input
                      id={`step-${index}-audio`}
                      placeholder="e.g. pt, pt* (wildcard) — empty means any language"
                      value={step.audio_language}
                      onChange={(e) => updateStep(index, { audio_language: e.target.value })}
                    />
                  </Field>

                  <Field orientation="horizontal">
                    <FieldLabel htmlFor={`step-${index}-subtitle-enabled`}>Require a subtitle</FieldLabel>
                    <Switch
                      id={`step-${index}-subtitle-enabled`}
                      checked={step.subtitle_enabled}
                      onCheckedChange={(checked) => updateStep(index, { subtitle_enabled: checked })}
                    />
                  </Field>

                  {step.subtitle_enabled && (
                    <Field>
                      <FieldLabel htmlFor={`step-${index}-subtitle-language`}>Subtitle language</FieldLabel>
                      <Input
                        id={`step-${index}-subtitle-language`}
                        placeholder="e.g. en, en* (wildcard) — empty means any language"
                        value={step.subtitle_language}
                        onChange={(e) => updateStep(index, { subtitle_language: e.target.value })}
                      />
                    </Field>
                  )}

                  <Field>
                    <FieldLabel htmlFor={`step-${index}-blocklist`}>Title blocklist (comma-separated)</FieldLabel>
                    <Input
                      id={`step-${index}-blocklist`}
                      placeholder="e.g. Commentary, Descriptive"
                      value={step.title_blocklist}
                      onChange={(e) => updateStep(index, { title_blocklist: e.target.value })}
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor={`step-${index}-allowlist`}>Title allowlist (comma-separated)</FieldLabel>
                    <Input
                      id={`step-${index}-allowlist`}
                      placeholder="Leave empty to allow any title"
                      value={step.title_allowlist}
                      onChange={(e) => updateStep(index, { title_allowlist: e.target.value })}
                    />
                  </Field>
                </div>
              ))}

              <Button type="button" variant="outline" onClick={addStep} disabled={steps.length >= MAX_STEPS}>
                <Plus /> Add step
              </Button>
            </FieldGroup>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={isLoading || mutation.isPending}>
              {mutation.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
