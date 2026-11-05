/**
 * scheduleForm.ts — pure validation behind ScheduleFormDialog (epic #6703
 * P4b). Lives apart from the component so the dialog file exports only a
 * component (react-refresh) and the rules are unit-testable without a DOM.
 *
 * Mirrors the server's rules (internal/handler/schedules.go) so the operator
 * sees the reason before a round-trip; the server stays authoritative.
 */

import { tryParseCron } from '@/shared/lib/cronSchedule'

/** DNS-1123 label, as the apiserver enforces on a CronJob name. */
export const DNS1123_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
/** The apiserver's CronJob name limit (child Job names need the suffix room). */
export const MAX_CRONJOB_NAME = 52

export interface ScheduleFormInput {
  mode: 'create' | 'edit'
  sourceKey: string
  name: string
  schedule: string
  timeZone: string
}

export interface ScheduleFormErrors {
  source?: string
  name?: string
  schedule?: string
  timeZone?: string
}

/** True when `tz` is an IANA zone the runtime knows. */
export function isKnownTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

export function validateScheduleForm(input: ScheduleFormInput): ScheduleFormErrors {
  const errors: ScheduleFormErrors = {}
  if (input.mode === 'create') {
    if (!input.sourceKey) errors.source = 'Pick the CronJob whose work this schedule runs.'
    const name = input.name.trim()
    if (!name) errors.name = 'A name is required.'
    else if (name.length > MAX_CRONJOB_NAME)
      errors.name = `At most ${MAX_CRONJOB_NAME} characters (a CronJob name must leave room for its child Job suffix).`
    else if (!DNS1123_LABEL.test(name))
      errors.name = 'Lowercase letters, digits and "-" only, starting and ending alphanumeric.'
  }
  const schedule = input.schedule.trim()
  if (!schedule) errors.schedule = 'A cron expression is required.'
  else if (schedule.toLowerCase() === '@reboot') errors.schedule = '@reboot has no wall-clock schedule.'
  else if (!tryParseCron(schedule))
    errors.schedule = 'Not a valid 5-field cron expression (minute hour day-of-month month day-of-week).'
  const tz = input.timeZone.trim()
  if (tz && !isKnownTimeZone(tz)) {
    errors.timeZone = `"${tz}" is not a known IANA time zone (e.g. Asia/Muscat).`
  }
  return errors
}
