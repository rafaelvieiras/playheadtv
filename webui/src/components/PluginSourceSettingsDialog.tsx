import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/http'
import type { PluginConfigField, PluginSource } from '@/lib/types'

interface PluginSourceSettingsDialogProps {
  // Mounted per source (the page renders it with a key of the source id), so the
  // form is initialised once, from the values that source actually has — no
  // effect copying props into state, and no flash of the previous source.
  source: PluginSource
  fields: PluginConfigField[]
  onOpenChange: (open: boolean) => void
}

// What the form holds while it is being edited: a bool as a boolean, everything
// else as the text the field shows. A choice is the index of the option in the
// declared list, never the value itself — an option's value may be a number or a
// string, and an index cannot be confused with either.
type FormValue = string | boolean

function initialForm(source: PluginSource, fields: PluginConfigField[]): Record<string, FormValue> {
  const form: Record<string, FormValue> = {}
  for (const field of fields) {
    const current = source.settings[field.key]
    switch (field.type) {
      case 'bool':
        form[field.key] = current === true
        break
      case 'choice': {
        const index = (field.options ?? []).findIndex((option) => option.value === current)
        form[field.key] = index === -1 ? '' : String(index)
        break
      }
      case 'number':
        form[field.key] = typeof current === 'number' ? String(current) : ''
        break
      default:
        form[field.key] = typeof current === 'string' ? current : ''
    }
  }
  return form
}

/**
 * Only what the admin changed, in the shape the plugin declared: a choice as the
 * option's own value (so a numeric setting stays a number), a number as a JSON
 * number, a bool as a boolean, text as text.
 *
 * A field left untouched is not sent at all. That matters: the config belongs to
 * the plugin, and writing a placeholder for a key the source never had would
 * change what the plugin reads (`max_items: 0` is not "unset"). An emptied number
 * field is likewise skipped rather than sent as NaN.
 */
function settingsPatch(
  fields: PluginConfigField[],
  initial: Record<string, FormValue>,
  form: Record<string, FormValue>,
): Record<string, unknown> {
  const patch: Record<string, unknown> = {}
  for (const field of fields) {
    const now = form[field.key]
    if (now === initial[field.key]) continue
    if (field.type === 'choice') {
      const option = field.options?.[Number(now)]
      if (option === undefined) continue
      patch[field.key] = option.value
    } else if (field.type === 'number') {
      const text = String(now).trim()
      if (text === '') continue
      patch[field.key] = Number(text)
    } else {
      patch[field.key] = now
    }
  }
  return patch
}

/**
 * The settings one plugin declares for its own sources (docs/PLUGINS.md
 * "Declared settings"). The admin page draws them from the declaration, so it
 * never learns a single plugin's keys: a plugin that adds a setting shows up here
 * with no change to this file.
 */
export function PluginSourceSettingsDialog({ source, fields, onOpenChange }: PluginSourceSettingsDialogProps) {
  const queryClient = useQueryClient()
  const [initial] = useState<Record<string, FormValue>>(() => initialForm(source, fields))
  const [form, setForm] = useState<Record<string, FormValue>>(initial)

  const patch = useMemo(() => settingsPatch(fields, initial, form), [fields, initial, form])
  const changed = Object.keys(patch).length > 0

  const mutation = useMutation({
    mutationFn: () => api.put<PluginSource>(`/plugin-sources/${source.id}/settings`, { settings: patch }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['plugin-sources'] })
      toast.success('Settings saved.')
      onOpenChange(false)
    },
    onError: (error: Error) => {
      toast.error(error.message || 'Could not save the settings.')
    },
  })

  const set = (key: string, value: FormValue) => setForm((prev) => ({ ...prev, [key]: value }))
  const title = source.name || source.id

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            What the <code>{source.plugin_id}</code> plugin takes for this source. Every field here was
            declared by the plugin itself, and only what you change is written.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          {fields.map((field) => (
            <Field key={field.key}>
              <FieldLabel htmlFor={`setting-${field.key}`}>{field.label}</FieldLabel>
              {field.type === 'bool' && (
                <Switch
                  id={`setting-${field.key}`}
                  checked={form[field.key] === true}
                  onCheckedChange={(checked) => set(field.key, checked)}
                />
              )}
              {field.type === 'number' && (
                <Input
                  id={`setting-${field.key}`}
                  type="number"
                  value={String(form[field.key] ?? '')}
                  onChange={(event) => set(field.key, event.target.value)}
                />
              )}
              {field.type === 'text' && (
                <Input
                  id={`setting-${field.key}`}
                  value={String(form[field.key] ?? '')}
                  onChange={(event) => set(field.key, event.target.value)}
                />
              )}
              {field.type === 'choice' && (
                <Select value={String(form[field.key] ?? '')} onValueChange={(value) => set(field.key, value)}>
                  <SelectTrigger id={`setting-${field.key}`}>
                    <SelectValue placeholder="Not set" />
                  </SelectTrigger>
                  <SelectContent>
                    {(field.options ?? []).map((option, index) => (
                      <SelectItem key={index} value={String(index)}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {field.help && <FieldDescription>{field.help}</FieldDescription>}
            </Field>
          ))}
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={() => mutation.mutate()} disabled={!changed || mutation.isPending}>
            {mutation.isPending ? 'Saving…' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
