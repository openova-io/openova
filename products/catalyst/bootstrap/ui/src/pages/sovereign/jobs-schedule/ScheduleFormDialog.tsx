/**
 * ScheduleFormDialog — the "New schedule" / "Edit schedule" modal of the
 * CronJob Schedule surface (epic #6703 P4b).
 *
 * Console primitives (owner-enforced): no inline forms on a page — this is a
 * modal opened from the toolbar's primary action (create) or from a row
 * (edit). It reuses the shared Dialog / Input / Button primitives.
 *
 * What it creates is a REAL batch/v1 CronJob: the operator picks the WORK
 * (an existing CronJob — a Blueprint's cron — whose jobTemplate is cloned
 * server-side, verbatim) and gives it a cadence. The form never authors a
 * pod spec; a schedule can only do what its source already does.
 *
 * Edit mode changes only what the server allows on a console-origin
 * schedule: the cron expression, the IANA time zone, and suspend.
 */

import { useMemo, useState, type FormEvent } from 'react'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/shared/ui/dialog'
import { describeCron, nextFireTime, tryParseCron } from '@/shared/lib/cronSchedule'
import type { CronRow } from './cronScheduleModel'
import { validateScheduleForm } from './scheduleForm'
import {
  createSchedule,
  updateSchedule,
  ScheduleApiError,
  type ScheduleResponse,
} from './schedules.api'

export type ScheduleFormMode = { kind: 'create' } | { kind: 'edit'; row: CronRow }

interface ScheduleFormDialogProps {
  deploymentId: string
  mode: ScheduleFormMode
  /** Candidate source CronJobs (create mode): the rows on the page. */
  sources: readonly CronRow[]
  /** Test seam / caller-supplied "now" for the next-fire preview. */
  now?: Date
  onClose: () => void
  onSaved: (saved: ScheduleResponse) => void
}

export function ScheduleFormDialog({
  deploymentId,
  mode,
  sources,
  now,
  onClose,
  onSaved,
}: ScheduleFormDialogProps) {
  const editing = mode.kind === 'edit' ? mode.row : null
  const [sourceKey, setSourceKey] = useState<string>(sources[0]?.key ?? '')
  const [name, setName] = useState<string>('')
  const [schedule, setSchedule] = useState<string>(editing?.schedule ?? '')
  const [timeZone, setTimeZone] = useState<string>(editing?.timeZone ?? '')
  const [suspend, setSuspend] = useState<boolean>(editing?.suspended ?? false)
  const [submitting, setSubmitting] = useState(false)
  const [serverError, setServerError] = useState<string>('')
  const [touched, setTouched] = useState(false)

  const source = useMemo(() => sources.find((s) => s.key === sourceKey) ?? null, [sources, sourceKey])
  const errors = useMemo(
    () => validateScheduleForm({ mode: mode.kind, sourceKey, name, schedule, timeZone }),
    [mode.kind, sourceKey, name, schedule, timeZone],
  )
  const hasErrors = Object.keys(errors).length > 0

  // Live preview: what the expression means + when it next fires.
  const preview = useMemo(() => {
    const parsed = schedule.trim() ? tryParseCron(schedule.trim()) : null
    if (!parsed) return null
    const ref = now ?? new Date()
    return { text: describeCron(parsed), next: nextFireTime(parsed, ref) }
  }, [schedule, now])

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (hasErrors) return
    setSubmitting(true)
    setServerError('')
    try {
      let saved: ScheduleResponse
      if (editing) {
        saved = await updateSchedule(deploymentId, editing.namespace, editing.name, {
          schedule: schedule.trim(),
          suspend,
          ...(timeZone.trim() ? { timeZone: timeZone.trim() } : { clearTimeZone: true }),
        })
      } else {
        if (!source) return
        saved = await createSchedule(deploymentId, {
          sourceNamespace: source.namespace,
          sourceName: source.name,
          name: name.trim(),
          schedule: schedule.trim(),
          ...(timeZone.trim() ? { timeZone: timeZone.trim() } : {}),
          suspend,
        })
      }
      onSaved(saved)
    } catch (err) {
      // Honest failure — the server's own reason, never a green close.
      if (err instanceof ScheduleApiError) {
        setServerError(err.code ? `${err.code}: ${err.message}` : err.message)
      } else {
        setServerError(err instanceof Error ? err.message : 'Network error')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const title = editing ? `Edit schedule — ${editing.name}` : 'New schedule'

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent data-testid="sov-cron-schedule-dialog" aria-describedby="sov-cron-schedule-dialog-desc">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription id="sov-cron-schedule-dialog-desc">
            {editing
              ? 'Change when this ad-hoc schedule fires. The work it runs stays the source CronJob’s.'
              : 'Run an existing CronJob’s work on an additional cadence. The new CronJob clones the source’s job template verbatim.'}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4" data-testid="sov-cron-schedule-form" noValidate>
          {editing ? (
            <div className="text-xs text-[var(--color-text-dim)]" data-testid="sov-cron-schedule-form-source">
              {editing.namespace}/{editing.name}
              {editing.sourceName ? <> · clones <span className="font-mono">{editing.sourceName}</span></> : null}
            </div>
          ) : (
            <label className="flex flex-col gap-1.5 text-[13px] font-medium text-[var(--color-text-secondary)]">
              <span>
                Source CronJob <span className="text-[var(--color-error)]" aria-hidden>*</span>
              </span>
              <select
                className="sov-cron-select"
                data-testid="sov-cron-schedule-form-source"
                value={sourceKey}
                onChange={(e) => setSourceKey(e.target.value)}
                required
              >
                {sources.length === 0 ? <option value="">No CronJobs to clone</option> : null}
                {sources.map((s) => (
                  <option key={s.key} value={s.key}>
                    {s.namespace}/{s.name}
                    {s.blueprint ? ` (${s.blueprint})` : ''}
                    {s.cluster ? ` @${s.cluster}` : ''}
                  </option>
                ))}
              </select>
              {touched && errors.source ? (
                <span className="text-xs text-[var(--color-error)]" role="alert">{errors.source}</span>
              ) : (
                <span className="text-xs font-normal text-[var(--color-text-dim)]">
                  The schedule lands in the source’s namespace and runs its job template.
                </span>
              )}
            </label>
          )}

          {!editing ? (
            <Input
              label="Name"
              id="sov-cron-schedule-form-name"
              data-testid="sov-cron-schedule-form-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={source ? `${source.name}-noon` : 'my-schedule'}
              required
              autoComplete="off"
              error={touched ? errors.name : undefined}
              hint="DNS-1123 label, at most 52 characters."
            />
          ) : null}

          <Input
            label="Schedule"
            id="sov-cron-schedule-form-schedule"
            data-testid="sov-cron-schedule-form-schedule"
            value={schedule}
            onChange={(e) => setSchedule(e.target.value)}
            placeholder="0 12 * * 1-5"
            required
            autoComplete="off"
            className="font-mono"
            error={touched ? errors.schedule : undefined}
            hint={
              preview
                ? `${preview.text}${preview.next ? ` · next ${formatDateTime(preview.next)}` : ''}`
                : 'Standard 5-field cron: minute hour day-of-month month day-of-week (or @hourly, @daily, …).'
            }
          />

          <Input
            label="Time zone"
            id="sov-cron-schedule-form-timezone"
            data-testid="sov-cron-schedule-form-timezone"
            value={timeZone}
            onChange={(e) => setTimeZone(e.target.value)}
            placeholder="Asia/Muscat"
            autoComplete="off"
            error={touched ? errors.timeZone : undefined}
            hint="Optional IANA zone (spec.timeZone). Empty = the controller’s clock."
          />

          <label className="flex items-center gap-2 text-sm text-[var(--color-text)]">
            <input
              type="checkbox"
              data-testid="sov-cron-schedule-form-suspend"
              checked={suspend}
              onChange={(e) => setSuspend(e.target.checked)}
            />
            Suspended (keeps the schedule but does not fire)
          </label>

          {serverError ? (
            <div
              role="alert"
              data-testid="sov-cron-schedule-form-error"
              className="rounded-md border border-[var(--color-error)]/40 bg-[var(--color-error)]/10 p-2 text-xs text-[var(--color-error)]"
            >
              {serverError}
            </div>
          ) : null}

          <div className="mt-1 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose} data-testid="sov-cron-schedule-form-cancel">
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={submitting || (!editing && sources.length === 0)}
              data-testid="sov-cron-schedule-form-submit"
            >
              {submitting ? 'Saving…' : editing ? 'Save changes' : 'Create schedule'}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** `MMM D, HH:MM` in the viewer's locale — same shape as the table. */
function formatDateTime(d: Date): string {
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}
