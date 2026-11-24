// One formatter for every timestamp in the admin UI.
//
// Before this there were three: `toLocaleString('en-US')` with seconds and no
// zone on the devices and sources pages, `toLocaleDateString` + a separate time
// string in `lib/schedule.ts` (so a schedule row read "11/23/2026 08:52 PM"
// while its own edit dialog read "23/11/2026, 20:52"), and a bare
// `toLocaleTimeString` on the guide. A timestamp the operator has to convert in
// their head is a defect, so every screen renders through these.
const LOCALE = 'en-US'

const DATE_TIME: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
}

/** "11/23/2026, 20:52" — seconds are noise, and the zone is opt-in. */
export function formatDateTime(ms: number, timeZone?: string): string {
  return new Intl.DateTimeFormat(LOCALE, { ...DATE_TIME, ...(timeZone ? { timeZone } : {}) }).format(new Date(ms))
}

/** "11/23/2026" */
export function formatDate(ms: number, timeZone?: string): string {
  return new Intl.DateTimeFormat(LOCALE, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    ...(timeZone ? { timeZone } : {}),
  }).format(new Date(ms))
}

/** "20:52" — a clock time, used by the guide and by schedule rows. */
export function formatTime(ms: number, timeZone?: string): string {
  return new Intl.DateTimeFormat(LOCALE, {
    hour: '2-digit',
    minute: '2-digit',
    ...(timeZone ? { timeZone } : {}),
  }).format(new Date(ms))
}

/** "Mon" — the day label the guide's ruler and cells use. */
export function formatWeekday(ms: number, timeZone?: string): string {
  return new Intl.DateTimeFormat(LOCALE, { weekday: 'short', ...(timeZone ? { timeZone } : {}) }).format(new Date(ms))
}

/** Where a timestamp is: the guide says "Times in America/Sao_Paulo." */
export function tzLabel(timeZone: string | undefined): string {
  return timeZone ? `Times in ${timeZone}.` : 'Times in your own time zone.'
}

/** A timestamp that may be absent: one wording for "never", everywhere. */
export function formatMaybeDateTime(ms: number | null | undefined, timeZone?: string): string {
  return ms === null || ms === undefined || ms === 0 ? 'Never' : formatDateTime(ms, timeZone)
}
