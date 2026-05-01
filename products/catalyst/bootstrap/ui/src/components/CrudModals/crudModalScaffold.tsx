/**
 * crudModalScaffold.tsx — generic Add/Edit modal scaffolding shared by
 * every per-kind modal (Cluster, vCluster, NodePool, WorkerNode,
 * LoadBalancer, Network, PVC, Bucket, Volume).
 *
 * Pattern (per issue #349):
 *   • Each kind defines a typed schema (the form's value object).
 *   • The kind's <Kind>FormFields component renders the inputs against
 *     that schema, used by both Add and Edit modals.
 *   • <CrudFormModal /> is the shared scaffolding that renders the
 *     ModalShell, wires submit/cancel, and surfaces submit errors. Add
 *     and Edit modals are thin wrappers that pre-fill the values and
 *     dispatch the right CRUD wrapper.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (no compromise) every kind goes
 * through this same scaffolding so the surface reads as one consistent
 * vocabulary across all twelve resource types.
 */

import { useState, type ReactNode } from 'react'
import { ModalShell } from './_shared'

export interface CrudFormModalProps {
  open: boolean
  /** Stable testid suffix — `infrastructure-modal-<id>` is the root. */
  id: string
  /** "Add cluster", "Edit cluster", … */
  title: string
  /** "Region <id>" / "Cluster <id>" / etc. */
  subtitle?: string
  /** "Add cluster" / "Save changes" — primary button label. */
  primaryLabel: string
  /** Disable submit when the form isn't valid yet. */
  canSubmit: boolean
  /** Async submit handler. Resolves with no value; rejects on API error. */
  onSubmit: () => Promise<void>
  onClose: () => void
  /** The kind-specific FormFields component goes here. */
  children: ReactNode
}

/** Single shell every Add/Edit modal renders through. The underlying
 *  ModalShell still does the keyboard / backdrop / styling work. */
export function CrudFormModal({
  open,
  id,
  title,
  subtitle,
  primaryLabel,
  canSubmit,
  onSubmit,
  onClose,
  children,
}: CrudFormModalProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!open) return null

  async function handleSubmit() {
    setError(null)
    setSubmitting(true)
    try {
      await onSubmit()
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setError(msg)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id={id}
      open={open}
      title={title}
      subtitle={subtitle}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: primaryLabel,
        onClick: handleSubmit,
        loading: submitting,
        disabled: !canSubmit,
      }}
    >
      {children}
      {error && (
        <div
          data-testid={`infrastructure-modal-${id}-error`}
          role="alert"
          style={{
            marginTop: 6,
            padding: '8px 10px',
            borderRadius: 6,
            border: '1px solid color-mix(in srgb, var(--color-danger) 50%, transparent)',
            background: 'color-mix(in srgb, var(--color-danger) 8%, transparent)',
            color: 'var(--color-danger)',
            fontSize: '0.78rem',
          }}
        >
          {error}
        </div>
      )}
    </ModalShell>
  )
}
