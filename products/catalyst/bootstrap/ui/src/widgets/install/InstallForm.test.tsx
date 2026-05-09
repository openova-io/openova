/**
 * InstallForm.test.tsx — unit tests for the EPIC-2 Slice I (#1097) auto-form.
 *
 * Snapshot rendering across three configSchema shapes:
 *
 *   1. Trivial schema (single string field) — base case.
 *   2. Schema with x-catalyst-ui-hint=password — masked widget engages.
 *   3. Blueprint with no configSchema — install-with-defaults branch.
 *
 * Plus a round-trip: changing a field value + clicking Submit fires
 * onSubmit with the collected parameters.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { InstallForm } from './InstallForm'
import { buildUiSchema, extractConfigSchema } from './installFormSchema'
import type { CatalogItem } from '@/lib/catalog.api'

afterEach(() => cleanup())

const makeBP = (configSchema?: unknown): CatalogItem => ({
  name: 'bp-test',
  version: '1.0.0',
  card: { title: 'Test Blueprint' },
  origin: 1,
  source: 'public',
  raw: configSchema
    ? { spec: { version: '1.0.0', configSchema } }
    : undefined,
})

describe('InstallForm — schema rendering', () => {
  it('renders a string input for a trivial single-field schema', () => {
    const onSubmit = vi.fn()
    render(
      <InstallForm
        blueprint={makeBP({
          type: 'object',
          required: ['domain'],
          properties: { domain: { type: 'string' } },
        })}
        onSubmit={onSubmit}
      />,
    )
    // RJSF renders an input with id rooted at root_<field>.
    expect(document.getElementById('root_domain')).toBeTruthy()
  })

  it('renders the password widget when x-catalyst-ui-hint=password', () => {
    render(
      <InstallForm
        blueprint={makeBP({
          type: 'object',
          required: ['secret'],
          properties: {
            secret: { type: 'string', 'x-catalyst-ui-hint': 'password' },
          },
        })}
        onSubmit={vi.fn()}
      />,
    )
    const masked = screen.getByTestId('install-form-password-input') as HTMLInputElement
    expect(masked.type).toBe('password')
  })

  it('renders the install-with-defaults branch when blueprint has no configSchema', () => {
    const onSubmit = vi.fn()
    render(<InstallForm blueprint={makeBP()} onSubmit={onSubmit} />)
    expect(screen.getByTestId('install-form-no-schema')).toBeTruthy()
    fireEvent.click(screen.getByTestId('install-form-submit-btn'))
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(onSubmit).toHaveBeenCalledWith({})
  })
})

describe('InstallForm — submit + preview wiring', () => {
  it('fires onSubmit with the formData when the form is submitted', async () => {
    const onSubmit = vi.fn()
    render(
      <InstallForm
        blueprint={makeBP({
          type: 'object',
          properties: { domain: { type: 'string' } },
        })}
        onSubmit={onSubmit}
      />,
    )
    const input = document.getElementById('root_domain') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'example.test' } })
    const submit = screen.getByTestId('install-form-submit-btn')
    fireEvent.click(submit)
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(onSubmit.mock.calls[0][0]).toMatchObject({ domain: 'example.test' })
  })

  it('fires onPreview with the current formData when Preview is clicked', () => {
    const onPreview = vi.fn()
    render(
      <InstallForm
        blueprint={makeBP({
          type: 'object',
          properties: { domain: { type: 'string' } },
        })}
        onSubmit={vi.fn()}
        onPreview={onPreview}
      />,
    )
    const input = document.getElementById('root_domain') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'preview.test' } })
    fireEvent.click(screen.getByTestId('install-form-preview-btn'))
    expect(onPreview).toHaveBeenCalledTimes(1)
    expect(onPreview.mock.calls[0][0]).toMatchObject({ domain: 'preview.test' })
  })
})

describe('buildUiSchema', () => {
  it('projects a top-level password hint to PasswordWidget', () => {
    const ui = buildUiSchema({
      type: 'object',
      properties: {
        secret: { type: 'string', 'x-catalyst-ui-hint': 'password' },
        plain: { type: 'string' },
      },
    })
    expect(ui.secret).toMatchObject({ 'ui:widget': 'PasswordWidget' })
    expect(ui.plain).toBeUndefined()
  })

  it('projects a domain-picker hint to DomainPickerWidget', () => {
    const ui = buildUiSchema({
      type: 'object',
      properties: {
        host: { type: 'string', 'x-catalyst-ui-hint': 'domain-picker' },
      },
    })
    expect(ui.host).toMatchObject({ 'ui:widget': 'DomainPickerWidget' })
  })

  it('ignores unknown hint values', () => {
    const ui = buildUiSchema({
      type: 'object',
      properties: {
        weird: { type: 'string', 'x-catalyst-ui-hint': 'totally-made-up' },
      },
    })
    expect(ui.weird).toBeUndefined()
  })
})

describe('extractConfigSchema', () => {
  it('returns the schema when raw.spec.configSchema is present', () => {
    const bp = makeBP({ type: 'object' })
    expect(extractConfigSchema(bp)).toMatchObject({ type: 'object' })
  })

  it('returns null when raw is missing', () => {
    expect(extractConfigSchema(makeBP())).toBeNull()
  })

  it('returns null when raw.spec.configSchema is missing', () => {
    const bp: CatalogItem = {
      ...makeBP(),
      raw: { spec: { version: '1.0.0' } },
    }
    expect(extractConfigSchema(bp)).toBeNull()
  })
})
