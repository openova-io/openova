import { useCallback, useEffect, useState } from 'react'
import { api, errorText } from '../api/client'
import { useSession } from '../auth/session'
import type { CostSource, Customer } from '../api/types'
import { Notice } from '../components/ui'
import { SourcesPanel } from '../panels/SourcesPanel'
import { StatementsPanel } from '../panels/StatementsPanel'
import { UsagePanel } from '../panels/UsagePanel'

// Customer-side pages (spec §5): every query is scoped server-side to
// session.customer_id; the UI only needs the id to build the paths.

function useMyCustomerId(): string | null {
  const { me } = useSession()
  return me?.customer_id ?? null
}

function NoCustomer() {
  return <Notice kind="warn">This account is not linked to a customer.</Notice>
}

export function MyUsage() {
  const id = useMyCustomerId()
  if (!id) return <NoCustomer />
  return (
    <div>
      <h1>My usage</h1>
      <UsagePanel customerId={id} />
    </div>
  )
}

export function MyStatements() {
  const id = useMyCustomerId()
  if (!id) return <NoCustomer />
  return (
    <div>
      <h1>My statements</h1>
      <StatementsPanel customerId={id} canIssue={false} />
    </div>
  )
}

export function MySources() {
  const id = useMyCustomerId()
  const { me } = useSession()
  const [c, setC] = useState<Customer | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(async () => {
    if (!id) return
    try {
      setC(await api.get<Customer>(`/customers/${id}`))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [id])
  useEffect(() => {
    void load()
  }, [load])
  if (!id) return <NoCustomer />
  const sources: CostSource[] = c && Array.isArray(c.sources) ? c.sources : []
  return (
    <div>
      <h1>My sources</h1>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      <SourcesPanel
        customerId={id}
        sources={sources}
        canManage={false}
        canRotate={me?.role === 'customer-admin'}
        onChanged={load}
      />
    </div>
  )
}
