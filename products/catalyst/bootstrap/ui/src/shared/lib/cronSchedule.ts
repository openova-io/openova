/**
 * cronSchedule.ts — a minimal, dependency-free standard-cron parser for the
 * sovereign-admin /jobs consolidated Schedule view (P3-frontend, Refs #6703).
 *
 * There is intentionally NO npm dependency (cron-parser / cronstrue): the
 * "what fires at 12:00" timeline an operator trusts must be small, auditable,
 * and unit-tested in this repo. This module supports the standard 5-field
 * Kubernetes CronJob syntax:
 *
 *   ┌───────────── minute        (0-59)
 *   │ ┌─────────── hour          (0-23)
 *   │ │ ┌───────── day-of-month  (1-31)
 *   │ │ │ ┌─────── month         (1-12)
 *   │ │ │ │ ┌───── day-of-week   (0-7, both 0 and 7 = Sunday)
 *   * * * * *
 *
 * Per field, each comma item may be `*`, a step (a star or range followed by
 * a slash-N), a range `a-b`, a list `a,b,c`, or a plain numeric value — all
 * composable (`0,30 9-17 * * 1-5`).
 * The predefined macros `@yearly`/`@annually`/`@monthly`/`@weekly`/`@daily`/
 * `@midnight`/`@hourly` normalise to their 5-field equivalents; `@reboot`
 * has no wall-clock schedule and parses as unschedulable (null).
 *
 * Day-of-month vs day-of-week uses the canonical Vixie-cron rule: if BOTH
 * fields are restricted (neither is a star), a fire happens when EITHER
 * matches; if either field is a star, they are ANDed. This is the behaviour
 * `man 5 crontab` documents and the kube-controller-manager's robfig/cron
 * implements — getting it wrong would mis-place marks on the timeline.
 *
 * The x-axis of the Schedule timeline is the cron's OWN wall-clock time
 * (hour/minute fields placed directly on a 00:00→24:00 axis) — it is NOT
 * converted to the viewer's timezone, so "fires at 12:00" is literally
 * `hour == 12`. Only the day-of-month/month/day-of-week gate needs a
 * reference calendar date to decide whether the cron fires that day at all;
 * that reference (and next-fire) is evaluated against the supplied Date.
 */

/** One parsed cron field: the expanded set of allowed values + the Vixie
 *  "star" flag (set when the raw field is a star — `*` or a star-step). */
export interface CronField {
  /** The raw field text, trimmed. */
  raw: string
  /** Every allowed value in the field's domain. */
  values: ReadonlySet<number>
  /** True when the field is a star (`*` or a star-step) — the Vixie
   *  DOM_STAR/DOW_STAR flag that decides AND-vs-OR between day-of-month and
   *  day-of-week. */
  star: boolean
}

/** A fully parsed 5-field cron expression. */
export interface ParsedCron {
  minute: CronField
  hour: CronField
  dom: CronField
  month: CronField
  dow: CronField
}

/** Thrown by {@link parseCron} on a syntactically invalid expression. */
export class CronParseError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'CronParseError'
  }
}

interface FieldSpec {
  min: number
  max: number
  /** day-of-week only: map 7 → 0 (both are Sunday). */
  wrap7to0?: boolean
}

const MINUTE: FieldSpec = { min: 0, max: 59 }
const HOUR: FieldSpec = { min: 0, max: 23 }
const DOM: FieldSpec = { min: 1, max: 31 }
const MONTH: FieldSpec = { min: 1, max: 12 }
const DOW: FieldSpec = { min: 0, max: 7, wrap7to0: true }

/** Predefined macros → their canonical 5-field expansions. */
const MACROS: Readonly<Record<string, string>> = Object.freeze({
  '@yearly': '0 0 1 1 *',
  '@annually': '0 0 1 1 *',
  '@monthly': '0 0 1 * *',
  '@weekly': '0 0 * * 0',
  '@daily': '0 0 * * *',
  '@midnight': '0 0 * * *',
  '@hourly': '0 * * * *',
})

/**
 * Expand a single cron field (which may itself be a comma list) into the
 * set of allowed values in [spec.min, spec.max]. Throws CronParseError on
 * anything malformed so a bad chart schedule surfaces as an honest error
 * rather than a silently-empty timeline.
 */
function expandField(raw: string, spec: FieldSpec): CronField {
  const text = raw.trim()
  if (text === '') throw new CronParseError('empty field')
  const star = text === '*' || text.startsWith('*/')
  const values = new Set<number>()

  const norm = (n: number): number => (spec.wrap7to0 && n === 7 ? 0 : n)
  const inRange = (n: number): boolean => n >= spec.min && n <= spec.max
  const int = (s: string): number => {
    if (!/^\d+$/.test(s)) throw new CronParseError(`not a number: "${s}"`)
    return Number(s)
  }

  for (const item of text.split(',')) {
    const part = item.trim()
    if (part === '') throw new CronParseError(`empty list item in "${raw}"`)

    // Split optional /step.
    let rangePart = part
    let step = 1
    const slash = part.indexOf('/')
    if (slash !== -1) {
      rangePart = part.slice(0, slash)
      const stepStr = part.slice(slash + 1)
      step = int(stepStr)
      if (step <= 0) throw new CronParseError(`step must be positive in "${part}"`)
    }

    let lo: number
    let hi: number
    if (rangePart === '*') {
      lo = spec.min
      hi = spec.max
    } else if (rangePart.includes('-')) {
      const [aStr, bStr] = rangePart.split('-')
      if (bStr === undefined || aStr === '' || bStr === '') {
        throw new CronParseError(`malformed range "${rangePart}"`)
      }
      lo = int(aStr)
      hi = int(bStr)
    } else {
      lo = int(rangePart)
      // A bare `n/step` counts from n up to the field max (cron semantics).
      hi = slash !== -1 ? spec.max : lo
    }

    if (!inRange(lo) || !inRange(hi)) {
      throw new CronParseError(
        `value out of range [${spec.min}-${spec.max}] in "${part}"`,
      )
    }
    if (lo > hi) throw new CronParseError(`inverted range "${rangePart}"`)

    for (let v = lo; v <= hi; v += step) values.add(norm(v))
  }

  return { raw: text, values, star }
}

/**
 * Parse a 5-field cron expression (or a supported macro). Throws
 * CronParseError on invalid syntax; returns null for `@reboot` (no
 * wall-clock schedule to place on a timeline).
 */
export function parseCron(expr: string): ParsedCron | null {
  const trimmed = (expr ?? '').trim()
  if (trimmed === '') throw new CronParseError('empty expression')

  if (trimmed.startsWith('@')) {
    const lower = trimmed.toLowerCase()
    if (lower === '@reboot') return null
    const expanded = MACROS[lower]
    if (!expanded) throw new CronParseError(`unknown macro "${trimmed}"`)
    return parseCron(expanded)
  }

  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5) {
    throw new CronParseError(
      `expected 5 fields, got ${fields.length} in "${expr}"`,
    )
  }
  return {
    minute: expandField(fields[0], MINUTE),
    hour: expandField(fields[1], HOUR),
    dom: expandField(fields[2], DOM),
    month: expandField(fields[3], MONTH),
    dow: expandField(fields[4], DOW),
  }
}

/**
 * Non-throwing variant — returns null on ANY parse failure (including
 * @reboot). Used by the UI where a malformed schedule must degrade to
 * "unparseable" rather than crash the render.
 */
export function tryParseCron(expr: string): ParsedCron | null {
  try {
    return parseCron(expr)
  } catch {
    return null
  }
}

/**
 * True when the cron's day-gate (day-of-month / month / day-of-week) matches
 * the given calendar date. Implements the Vixie AND-vs-OR rule between DOM
 * and DOW. Evaluated against the Date's LOCAL calendar fields.
 */
export function matchesDate(parsed: ParsedCron, date: Date): boolean {
  const month = date.getMonth() + 1
  if (!parsed.month.values.has(month)) return false

  const dom = date.getDate()
  const dow = date.getDay() // 0 = Sunday
  const domMatch = parsed.dom.values.has(dom)
  const dowMatch = parsed.dow.values.has(dow)

  // Vixie rule: if either day field is a star, AND them; if both are
  // restricted, OR them (the "runs on the 1st AND every Sunday" behaviour).
  if (parsed.dom.star || parsed.dow.star) return domMatch && dowMatch
  return domMatch || dowMatch
}

/**
 * All minutes-of-day (0..1439) at which the cron fires on `date`'s calendar
 * day, ascending. Empty when the day-gate does not match that date. The
 * values are wall-clock (hour*60 + minute) — position them directly on a
 * 00:00→24:00 axis.
 */
export function fireMinutesOnDate(parsed: ParsedCron, date: Date): number[] {
  if (!matchesDate(parsed, date)) return []
  const hours = [...parsed.hour.values].sort((a, b) => a - b)
  const minutes = [...parsed.minute.values].sort((a, b) => a - b)
  const out: number[] = []
  for (const h of hours) {
    for (const m of minutes) out.push(h * 60 + m)
  }
  return out
}

/**
 * The next wall-clock fire at or after `from` (strictly after `from`'s
 * minute), evaluated against the local calendar. Returns null if no fire is
 * found within a ~4-year horizon (e.g. an impossible date like Feb 30) or
 * for an unschedulable expression.
 *
 * Iterates day-by-day (not minute-by-minute) so a yearly cron resolves in a
 * few hundred cheap iterations, not half a million.
 */
export function nextFireTime(parsed: ParsedCron, from: Date): Date | null {
  // Start of the minute strictly after `from`.
  const cursor = new Date(from)
  cursor.setSeconds(0, 0)
  cursor.setMinutes(cursor.getMinutes() + 1)

  const fromDayStart = new Date(
    cursor.getFullYear(),
    cursor.getMonth(),
    cursor.getDate(),
  )
  const startMinuteOfDay = cursor.getHours() * 60 + cursor.getMinutes()

  const MAX_DAYS = 366 * 4
  for (let i = 0; i < MAX_DAYS; i++) {
    const day = new Date(fromDayStart)
    day.setDate(day.getDate() + i)
    if (!matchesDate(parsed, day)) continue
    const lower = i === 0 ? startMinuteOfDay : 0
    for (const minuteOfDay of fireMinutesOnDate(parsed, day)) {
      if (minuteOfDay >= lower) {
        const fire = new Date(day)
        fire.setHours(Math.floor(minuteOfDay / 60), minuteOfDay % 60, 0, 0)
        return fire
      }
    }
  }
  return null
}

/* ── Human-readable describe() ────────────────────────────────────────── */

const DOW_NAMES = [
  'Sunday',
  'Monday',
  'Tuesday',
  'Wednesday',
  'Thursday',
  'Friday',
  'Saturday',
]

const pad2 = (n: number): string => String(n).padStart(2, '0')

/** The step `n` of a star-step field (`*` slash `n`), or null. */
function starStep(field: CronField): number | null {
  const m = /^\*\/(\d+)$/.exec(field.raw)
  return m ? Number(m[1]) : null
}

/** The single value of a field, or null when it holds 0 or >1 values. */
function soleValue(field: CronField): number | null {
  if (field.values.size !== 1) return null
  return [...field.values][0]
}

const isEvery = (field: CronField): boolean => field.raw === '*'

/** Render the day-of-week set as "Sunday" / "Monday–Friday" / "Mon, Wed". */
function describeDow(field: CronField): string {
  const vals = [...field.values].sort((a, b) => a - b)
  if (vals.length === 1) return `Every ${DOW_NAMES[vals[0]]}`
  // Contiguous run → "A–B" (only when it doesn't wrap through Sunday).
  const contiguous = vals.every((v, i) => i === 0 || v === vals[i - 1] + 1)
  if (contiguous) return `${DOW_NAMES[vals[0]]}–${DOW_NAMES[vals[vals.length - 1]]}`
  return vals.map((v) => DOW_NAMES[v]).join(', ')
}

/**
 * A best-effort human sentence for a cron expression. Deliberately covers
 * the common shapes robustly and falls back to the raw expression for
 * anything exotic rather than over-reaching on natural language.
 */
export function describeCron(input: ParsedCron | string): string {
  const parsed = typeof input === 'string' ? tryParseCron(input) : input
  if (!parsed) {
    if (typeof input === 'string') {
      return input.trim().toLowerCase() === '@reboot' ? 'At startup' : input
    }
    return ''
  }
  const { minute, hour, dom, month, dow } = parsed
  const dayFieldsBare = isEvery(dom) && isEvery(month) && isEvery(dow)

  // Every minute.
  if (isEvery(minute) && isEvery(hour) && dayFieldsBare) return 'Every minute'

  // Every N minutes.
  const minStep = starStep(minute)
  if (minStep && isEvery(hour) && dayFieldsBare) {
    return minStep === 1 ? 'Every minute' : `Every ${minStep} minutes`
  }

  const m = soleValue(minute)

  // Every N hours (at minute 0).
  const hourStep = starStep(hour)
  if (m === 0 && hourStep && dayFieldsBare) {
    return hourStep === 1 ? 'Every hour' : `Every ${hourStep} hours`
  }

  // Hourly at a fixed minute.
  if (m !== null && isEvery(hour) && dayFieldsBare) {
    return `Every hour at :${pad2(m)}`
  }

  const h = soleValue(hour)
  if (m !== null && h !== null) {
    const time = `${pad2(h)}:${pad2(m)}`
    if (dayFieldsBare) return `Every day at ${time}`
    // Weekly / weekday cadence.
    if (isEvery(dom) && isEvery(month) && !dow.star) {
      return `${describeDow(dow)} at ${time}`
    }
    // Monthly day(s).
    if (!dom.star && dow.star && isEvery(month)) {
      const days = [...dom.values].sort((a, b) => a - b).join(', ')
      return `On day ${days} of the month at ${time}`
    }
    return `At ${time}`
  }

  // Anything else — return the normalised raw expression (safe, honest).
  return `${minute.raw} ${hour.raw} ${dom.raw} ${month.raw} ${dow.raw}`
}
