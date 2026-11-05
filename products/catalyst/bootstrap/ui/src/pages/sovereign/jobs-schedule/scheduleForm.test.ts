import { describe, expect, it } from 'vitest'
import { validateScheduleForm } from './scheduleForm'

const ok = { mode: 'create' as const, sourceKey: 'cronjob:openbao/openbao-snapshot-save', name: 'snap-noon', schedule: '0 12 * * 1-5', timeZone: '' }

describe('validateScheduleForm (#6703 P4b)', () => {
  it('accepts a well-formed create', () => {
    expect(validateScheduleForm(ok)).toEqual({})
    expect(validateScheduleForm({ ...ok, timeZone: 'Asia/Muscat' })).toEqual({})
    expect(validateScheduleForm({ ...ok, schedule: '@daily' })).toEqual({})
  })

  it('names the reason for each bad field, mirroring the server', () => {
    expect(validateScheduleForm({ ...ok, sourceKey: '' }).source).toMatch(/Pick the CronJob/)
    expect(validateScheduleForm({ ...ok, name: '' }).name).toMatch(/required/)
    expect(validateScheduleForm({ ...ok, name: 'Noon' }).name).toMatch(/Lowercase/)
    expect(validateScheduleForm({ ...ok, name: 'a'.repeat(53) }).name).toMatch(/52/)
    expect(validateScheduleForm({ ...ok, schedule: '' }).schedule).toMatch(/required/)
    expect(validateScheduleForm({ ...ok, schedule: '0 25 * * *' }).schedule).toMatch(/5-field/)
    expect(validateScheduleForm({ ...ok, schedule: '@reboot' }).schedule).toMatch(/@reboot/)
    expect(validateScheduleForm({ ...ok, timeZone: 'Mars/Olympus' }).timeZone).toMatch(/not a known IANA/)
  })

  it('edit mode validates only what the server lets a console schedule change', () => {
    expect(validateScheduleForm({ mode: 'edit', sourceKey: '', name: '', schedule: '0 13 * * *', timeZone: '' })).toEqual({})
    expect(validateScheduleForm({ mode: 'edit', sourceKey: '', name: '', schedule: 'every day', timeZone: '' }).schedule).toBeTruthy()
  })
})
