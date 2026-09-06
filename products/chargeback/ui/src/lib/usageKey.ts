/**
 * keyOfUsageRow — the grouped column of a usage row (#6866).
 *
 * The API returns it as `key` for EVERY grouping. The panel previously read
 * `day` / `resource_id`, which the server does not send, so the day and
 * resource views rendered a dash in every row while the numbers beside them
 * were right — a table that looks populated and is unusable.
 */
export type UsageKeyRow = { key?: string; sku?: string; day?: string; resource_id?: string }

export function keyOfUsageRow(r: UsageKeyRow, groupBy: 'sku' | 'day' | 'resource'): string {
  return r.key ?? (groupBy === 'sku' ? r.sku : groupBy === 'day' ? r.day : r.resource_id) ?? '—'
}
