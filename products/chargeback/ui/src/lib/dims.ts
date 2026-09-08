/**
 * Should the explorer print a group's raw key under its label?
 *
 * It is worth printing when the key is itself readable — `eip` under "Elastic
 * IP", `ecs.m7n.xlarge.8` under a flavour — because that is the token the
 * reader will type into a filter or find in an invoice. It is noise when the
 * key is an opaque identifier: a customer row grouped by customer carries a
 * UUID, which tells the reader nothing and, with a page of customers, buries
 * the names it sits under (#6867).
 */
export function showsRawKey(key: string, label: string): boolean {
  if (!key || key === label || key === 'other') return false
  return !UUID_RE.test(key)
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
