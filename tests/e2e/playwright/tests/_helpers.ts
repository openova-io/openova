// Shared helpers for Group L smoke tests (#142, #143, #144).
//
// reachable() — pings the configured BASE_URL once before the suite runs so
// individual tests can `test.skip(!ok, 'app not running')` instead of failing
// outright when the dev server isn't available (e.g. CI worktree without
// `npm run dev` running, or a stripped-down container that can't run vite).
//
// Per principle #1 ("never speculate"), we don't pretend a smoke test passed
// just because the app is offline. We mark it skipped with an explicit reason.

/**
 * Probe `url` and return true ONLY when a 2xx/3xx response comes back. A
 * 4xx/5xx counts as "wrong app on that port" and we'd rather skip than
 * fail-noisy against a stranger's index page (e.g. the marketing website
 * happens to bind localhost:4321 in some dev environments).
 */
export async function reachable(url: string, timeoutMs = 2_000): Promise<boolean> {
  try {
    const ctrl = new AbortController()
    const timer = setTimeout(() => ctrl.abort(), timeoutMs)
    const res = await fetch(url, { signal: ctrl.signal, redirect: 'manual' as RequestRedirect })
    clearTimeout(timer)
    return res.status >= 200 && res.status < 400
  } catch {
    return false
  }
}
