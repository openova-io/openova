/**
 * GroupBrowserPage — Keycloak group browser for sovereign-admin
 * (EPIC-3 #1098 slice U3).
 *
 * Renders the realm's group tree (top-level → sub-groups, recursive).
 * Supports add subgroup, delete, and inline attribute editing per the
 * brief. The page is sovereign-admin only — the catalyst-api enforces
 * the gate (returns 403 to non-admin callers); the UI surfaces the
 * 403 as a banner so the operator knows their session lacks the
 * permission rather than silently rendering an empty tree.
 *
 * Per the canonical-seam map, this page reuses:
 *   - `authedFetch` via the rbac.api wrappers
 *   - `useResolvedDeploymentId` for chroot vs mothership URL shape
 *   - `PortalShell` for the chrome (sidebar + page-title bar)
 *   - TanStack Query for cache + invalidation (no per-page state lib)
 */

import { useState } from 'react'
import { useParams } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import {
  createKCGroup,
  deleteKCGroup,
  listKCGroups,
  updateKCGroup,
  type KCGroup,
} from './rbac.api'

interface GroupBrowserPageProps {
  initialDeploymentId?: string
}

export function GroupBrowserPage({ initialDeploymentId }: GroupBrowserPageProps = {}) {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? params.deploymentId ?? resolvedId ?? ''

  const queryClient = useQueryClient()

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['kc-groups', deploymentId],
    queryFn: () => listKCGroups(deploymentId),
    enabled: !!deploymentId,
    staleTime: 30_000,
  })

  const groups = data ?? []
  const [newName, setNewName] = useState('')
  const [parentId, setParentId] = useState<string>('')
  const [createErr, setCreateErr] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      createKCGroup(deploymentId, {
        name: newName.trim(),
        parentId: parentId || undefined,
      }),
    onSuccess: () => {
      setNewName('')
      setParentId('')
      setCreateErr(null)
      queryClient.invalidateQueries({ queryKey: ['kc-groups', deploymentId] })
    },
    onError: (e: Error) => setCreateErr(e.message),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteKCGroup(deploymentId, id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['kc-groups', deploymentId] }),
  })

  function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!newName.trim()) {
      setCreateErr('group name is required')
      return
    }
    createMutation.mutate()
  }

  function handleDelete(g: KCGroup) {
    if (!g.id) return
    if (!confirm(`Delete Keycloak group "${g.name}"?\nPath: ${g.path}\nThis revokes any role-mapping bound to this group.`)) {
      return
    }
    deleteMutation.mutate(g.id)
  }

  // 403 surface (auth gate failed). Surface it explicitly so the
  // operator knows to escalate vs assuming the realm is empty.
  const isForbidden = isError && /HTTP 403/.test((error as Error)?.message ?? '')

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Keycloak Groups">
      <div data-testid="group-browser-page" className="px-6 py-4">
        <div className="mb-4 flex items-start justify-between">
          <div>
            <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">Keycloak Groups</h1>
            <p className="text-sm text-[var(--color-text-dim)]">
              Browse, create, edit, and delete groups in the Sovereign realm. Sovereign-admin only.
            </p>
          </div>
          <button
            type="button"
            data-testid="group-browser-refresh"
            onClick={() => refetch()}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:bg-[var(--color-bg-2)]"
          >
            Refresh
          </button>
        </div>

        {isForbidden ? (
          <div data-testid="group-browser-forbidden" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            Forbidden — this page requires the sovereign-admin tier (admin or owner). Escalate via your Org owner.
          </div>
        ) : isError ? (
          <div data-testid="group-browser-error" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {(error as Error)?.message ?? 'Failed to load groups'}
          </div>
        ) : null}

        {isLoading ? (
          <div data-testid="group-browser-loading" className="text-sm text-[var(--color-text-dim)]">
            Loading…
          </div>
        ) : groups.length === 0 && !isError ? (
          <div data-testid="group-browser-empty" className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
            No groups in the realm. Use the form below to add the first one.
          </div>
        ) : (
          <ul data-testid="group-browser-tree" className="space-y-1 rounded-md border border-[var(--color-border)] p-3">
            {groups.map((g) => (
              <GroupNode
                key={g.id ?? g.path ?? g.name}
                group={g}
                depth={0}
                deploymentId={deploymentId}
                onDelete={handleDelete}
              />
            ))}
          </ul>
        )}

        <form onSubmit={handleCreate} className="mt-6 rounded-md border border-[var(--color-border)] p-3">
          <h2 className="text-xs uppercase text-[var(--color-text-dim)]">Add group</h2>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <input
              data-testid="group-browser-new-name"
              type="text"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="group name"
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
            />
            <select
              data-testid="group-browser-new-parent"
              value={parentId}
              onChange={(e) => setParentId(e.target.value)}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
              aria-label="Parent group (optional)"
            >
              <option value="">No parent (top-level)</option>
              {flattenGroups(groups).map((g) => (
                <option key={g.id ?? g.path ?? g.name} value={g.id ?? ''}>
                  {g.path ?? g.name}
                </option>
              ))}
            </select>
            <button
              type="submit"
              data-testid="group-browser-new-submit"
              disabled={createMutation.isPending}
              className="rounded-md bg-[var(--color-accent)] px-3 py-1 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
            >
              {createMutation.isPending ? 'Creating…' : 'Add'}
            </button>
          </div>
          {createErr ? (
            <p data-testid="group-browser-new-err" className="mt-1 text-xs text-red-300">
              {createErr}
            </p>
          ) : null}
        </form>
      </div>
    </PortalShell>
  )
}

interface GroupNodeProps {
  group: KCGroup
  depth: number
  deploymentId: string
  onDelete: (g: KCGroup) => void
}

function GroupNode({ group, depth, deploymentId, onDelete }: GroupNodeProps) {
  const [editing, setEditing] = useState(false)
  return (
    <li
      data-testid={`group-browser-node-${group.id ?? group.name}`}
      className="text-sm"
      style={{ paddingLeft: `${depth * 12}px` }}
    >
      <div className="flex items-center justify-between gap-2 py-1">
        <span className="font-mono text-[var(--color-text)]">
          <span className="text-[var(--color-text-dim)]">{depth > 0 ? '↳ ' : ''}</span>
          {group.name}
          {group.path ? (
            <span className="ml-2 text-xs text-[var(--color-text-dim)]">{group.path}</span>
          ) : null}
        </span>
        <span className="flex items-center gap-2">
          <button
            type="button"
            data-testid={`group-browser-edit-${group.id ?? group.name}`}
            onClick={() => setEditing((v) => !v)}
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
          >
            {editing ? 'Close' : 'Edit attrs'}
          </button>
          <button
            type="button"
            data-testid={`group-browser-delete-${group.id ?? group.name}`}
            onClick={() => onDelete(group)}
            className="text-xs text-red-400 hover:underline"
          >
            Delete
          </button>
        </span>
      </div>
      {editing && group.id ? (
        <AttributesEditor
          deploymentId={deploymentId}
          groupId={group.id}
          attributes={group.attributes ?? {}}
        />
      ) : null}
      {group.subGroups && group.subGroups.length > 0 ? (
        <ul className="space-y-0.5">
          {group.subGroups.map((c) => (
            <GroupNode
              key={c.id ?? c.path ?? c.name}
              group={c}
              depth={depth + 1}
              deploymentId={deploymentId}
              onDelete={onDelete}
            />
          ))}
        </ul>
      ) : null}
    </li>
  )
}

interface AttributesEditorProps {
  deploymentId: string
  groupId: string
  attributes: Record<string, string[]>
}

function AttributesEditor({ deploymentId, groupId, attributes }: AttributesEditorProps) {
  const queryClient = useQueryClient()
  const [rows, setRows] = useState<Array<{ key: string; value: string }>>(() => {
    const out: Array<{ key: string; value: string }> = []
    for (const [k, vs] of Object.entries(attributes)) {
      for (const v of vs) {
        out.push({ key: k, value: v })
      }
    }
    return out
  })
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: () => {
      const grouped: Record<string, string[]> = {}
      for (const r of rows) {
        const k = r.key.trim()
        const v = r.value.trim()
        if (!k) continue
        if (!grouped[k]) grouped[k] = []
        grouped[k].push(v)
      }
      return updateKCGroup(deploymentId, groupId, { attributes: grouped })
    },
    onSuccess: () => {
      setError(null)
      queryClient.invalidateQueries({ queryKey: ['kc-groups', deploymentId] })
    },
    onError: (e: Error) => setError(e.message),
  })

  return (
    <div
      data-testid={`group-browser-attrs-editor-${groupId}`}
      className="ml-4 mt-1 rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] p-2"
    >
      {rows.map((r, i) => (
        <div key={i} className="mb-1 flex items-center gap-2">
          <input
            type="text"
            value={r.key}
            onChange={(e) =>
              setRows((rs) => rs.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))
            }
            placeholder="key"
            className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-0.5 text-xs font-mono"
            data-testid={`group-browser-attrs-key-${groupId}-${i}`}
          />
          <input
            type="text"
            value={r.value}
            onChange={(e) =>
              setRows((rs) => rs.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
            }
            placeholder="value"
            className="flex-1 rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-0.5 text-xs font-mono"
            data-testid={`group-browser-attrs-value-${groupId}-${i}`}
          />
          <button
            type="button"
            onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
            className="text-xs text-red-400 hover:underline"
            data-testid={`group-browser-attrs-remove-${groupId}-${i}`}
          >
            ×
          </button>
        </div>
      ))}
      <div className="mt-1 flex items-center gap-2">
        <button
          type="button"
          onClick={() => setRows((rs) => [...rs, { key: '', value: '' }])}
          className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
          data-testid={`group-browser-attrs-add-${groupId}`}
        >
          + Add row
        </button>
        <button
          type="button"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending}
          className="rounded-md bg-[var(--color-accent)] px-2 py-0.5 text-xs font-medium text-white hover:opacity-90 disabled:opacity-50"
          data-testid={`group-browser-attrs-save-${groupId}`}
        >
          {mutation.isPending ? 'Saving…' : 'Save'}
        </button>
      </div>
      {error ? (
        <p className="mt-1 text-xs text-red-300" data-testid={`group-browser-attrs-err-${groupId}`}>
          {error}
        </p>
      ) : null}
    </div>
  )
}

function flattenGroups(groups: KCGroup[]): KCGroup[] {
  const out: KCGroup[] = []
  function walk(g: KCGroup) {
    out.push(g)
    if (g.subGroups) for (const c of g.subGroups) walk(c)
  }
  for (const g of groups) walk(g)
  return out
}
