import { useMemo } from 'react'
import { asList } from '../api/client'
import type { Customer } from '../api/types'
import { useQuery } from './useQuery'

/** The customer directory, for scope selects and name lookups on the operator pages. */
export function useCustomers() {
  const q = useQuery<unknown>('/customers')
  const customers = useMemo(() => asList<Customer>(q.data, 'customers'), [q.data])
  return { customers, error: q.error, loading: q.loading, reload: q.reload }
}

/** A customer's display name by id; `fallback` when the directory has not loaded. */
export function customerName(customers: Customer[], id: string | null | undefined, fallback?: string | null): string {
  if (!id) return 'All customers'
  return customers.find((c) => c.id === id)?.name ?? fallback ?? id
}
