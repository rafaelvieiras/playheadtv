import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  CUT_KIND_LABELS,
  defaultSamples,
  emptySample,
  formatDuration,
  sampleToPreview,
  type CutConfigInput,
  type SampleDraft,
} from '@/lib/cutRules'
import { api, ApiError } from '@/lib/http'
import type { CutItemKind, CutPreviewResult } from '@/lib/types'

const MAX_SAMPLES = 5

const SOURCE_LABELS: Record<CutPreviewResult['source'], string> = {
  rule: 'rule',
  default: 'the default strategy',
  segment: 'the intro segment',
  chapter: 'the chapters',
  fallback: 'the fallback (no intro segment or chapters found)',
  builtin: 'the built-in middle cut (no intro segment, chapters or fallback)',
  none: 'no rule',
}

// useDebounced returns value after it has stopped changing for delay ms, so a
// request is not sent on every keystroke.
function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(t)
  }, [value, delay])
  return debounced
}

// CutPreviewPanel shows where the cuts fall for sample items the author types
// (kind, duration, optional chapter starts), asking the backend so the preview
// is the real planner and not a copy of it. config is null while the draft has
// a problem the author must fix first; error says which.
export function CutPreviewPanel({
  config,
  configError,
}: {
  config: CutConfigInput | null
  configError: string | null
}) {
  const [samples, setSamples] = useState<SampleDraft[]>(defaultSamples)

  function patch(key: string, next: Partial<SampleDraft>) {
    setSamples((cur) => cur.map((s) => (s.key === key ? { ...s, ...next } : s)))
  }

  return (
    <section className="flex flex-col gap-3" data-testid="cut-preview">
      <h2 className="text-base font-medium">Preview</h2>
      <p className="text-sm text-muted-foreground">
        Where the breaks would fall for a typical item. Type a duration, the chapter starts (minutes) and where the
        intro segment ends to try the chapter strategies: the intro segment decides "after the intro" before the
        chapters do.
      </p>
      {configError && (
        <p className="text-sm text-destructive" data-testid="cut-preview-config-error">
          {configError}
        </p>
      )}
      {samples.map((s, i) => (
        <div key={s.key} className="flex flex-col gap-2 rounded-lg border p-3" data-testid={`cut-sample-${i}`}>
          <div className="flex flex-wrap items-end gap-2">
            <Select value={s.kind} onValueChange={(v) => patch(s.key, { kind: v as CutItemKind })}>
              <SelectTrigger className="w-32" aria-label={`Sample ${i + 1} kind`} data-testid={`cut-sample-${i}-kind`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(CUT_KIND_LABELS) as CutItemKind[]).map((k) => (
                  <SelectItem key={k} value={k}>
                    {CUT_KIND_LABELS[k]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <label className="flex items-center gap-1 text-sm">
              <Input
                className="w-24"
                inputMode="decimal"
                aria-label={`Sample ${i + 1} duration in minutes`}
                data-testid={`cut-sample-${i}-duration`}
                value={s.duration_minutes}
                onChange={(e) => patch(s.key, { duration_minutes: e.target.value })}
              />
              min
            </label>
            <Input
              className="min-w-40 flex-1"
              placeholder="Chapter starts, e.g. 0, 5, 22, 41"
              aria-label={`Sample ${i + 1} chapter starts in minutes`}
              data-testid={`cut-sample-${i}-chapters`}
              value={s.chapter_minutes}
              onChange={(e) => patch(s.key, { chapter_minutes: e.target.value })}
            />
            <label className="flex items-center gap-1 text-sm">
              <Input
                className="w-20"
                inputMode="decimal"
                placeholder="Intro ends"
                aria-label={`Sample ${i + 1} intro segment end in minutes`}
                data-testid={`cut-sample-${i}-intro`}
                value={s.intro_end_minutes}
                onChange={(e) => patch(s.key, { intro_end_minutes: e.target.value })}
              />
              min
            </label>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`Remove sample ${i + 1}`}
              disabled={samples.length === 1}
              onClick={() => setSamples((cur) => cur.filter((x) => x.key !== s.key))}
            >
              <Trash2 />
            </Button>
          </div>
          <SamplePreview index={i} sample={s} config={config} />
        </div>
      ))}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid="cut-add-sample"
          disabled={samples.length >= MAX_SAMPLES}
          onClick={() => setSamples((cur) => [...cur, emptySample('episode', '30')])}
        >
          <Plus /> Add sample
        </Button>
      </div>
    </section>
  )
}

function SamplePreview({ index, sample, config }: { index: number; sample: SampleDraft; config: CutConfigInput | null }) {
  const body = config ? sampleToPreview(config, sample) : null
  const payload = body && body.ok ? body.value : null
  const key = useDebounced(payload ? JSON.stringify(payload) : '', 300)
  const { data, error, isFetching } = useQuery({
    queryKey: ['cut-preview', key],
    queryFn: () => api.post<CutPreviewResult>('/cut-rule-sets/preview', JSON.parse(key)),
    enabled: key !== '',
    retry: false,
    placeholderData: (previous) => previous,
  })

  if (!config) return null
  if (body && !body.ok) return <p className="text-sm text-destructive">{body.error}</p>
  if (error) {
    return (
      <p className="text-sm text-destructive" data-testid={`cut-sample-${index}-error`}>
        {error instanceof ApiError ? error.message : 'Could not compute the preview.'}
      </p>
    )
  }
  if (!data || payload === null) return <p className="text-sm text-muted-foreground">Loading…</p>

  const duration = payload.duration_ms
  const rule = data.rule_index >= 0 ? `rule ${data.rule_index + 1}` : SOURCE_LABELS[data.source]
  const via = data.source === 'rule' ? rule : data.source === 'none' ? SOURCE_LABELS.none : `${rule}, via ${SOURCE_LABELS[data.source]}`
  const introEnd = (payload.segments ?? []).find((x) => x.type === 'intro')?.end_ms
  return (
    <div className={isFetching ? 'flex flex-col gap-1 opacity-70' : 'flex flex-col gap-1'} data-testid={`cut-sample-${index}-result`}>
      <div className="relative h-6 rounded bg-muted" aria-hidden>
        {data.cuts_ms.map((c) => (
          <div
            key={c}
            className="absolute top-0 h-full w-0.5 bg-primary"
            style={{ left: `${(c / duration) * 100}%` }}
            title={formatDuration(c)}
          />
        ))}
        {introEnd !== undefined && introEnd < duration && (
          <div
            className="absolute left-0 top-0 h-2 rounded-sm bg-primary/30"
            style={{ width: `${(introEnd / duration) * 100}%` }}
            title={`intro segment, ends at ${formatDuration(introEnd)}`}
          />
        )}
        {(payload.chapters ?? []).map((c) =>
          c.start_ms > 0 && c.start_ms < duration ? (
            <div
              key={`ch-${c.start_ms}`}
              className="absolute bottom-0 h-2 w-px bg-muted-foreground/60"
              style={{ left: `${(c.start_ms / duration) * 100}%` }}
            />
          ) : null,
        )}
      </div>
      <p className="text-sm" data-testid={`cut-sample-${index}-cuts`}>
        {data.cuts_ms.length === 0 ? (
          <>No cut ({via}).</>
        ) : (
          <>
            {data.cuts_ms.length === 1 ? '1 break' : `${data.cuts_ms.length} breaks`} at{' '}
            <strong>{data.cuts_ms.map(formatDuration).join(', ')}</strong> ({via}).
          </>
        )}
      </p>
      {data.cuts_ms.length > 0 && (
        <p className="text-xs text-muted-foreground" data-testid={`cut-sample-${index}-segments`}>
          Segments: {data.segments_ms.map(formatDuration).join(' + ')}
        </p>
      )}
      {data.notes.length > 0 && <p className="text-xs text-muted-foreground">{data.notes.join('; ')}.</p>}
    </div>
  )
}
