/**
 * e2e/gate/env.ts — the inputs of a run of the browser harness (FND-161).
 * The gate workflow (.github/workflows/foundation-gate.yaml) sets them; the
 * self-test (e2e/gate/self-test/run.sh) sets stand-ins.
 *
 *   GATE_FQDN            the Sovereign's domain; the console is console.<fqdn>
 *   GATE_SCHEME          https (the self-test: http)
 *   GATE_PORT            empty (the self-test: the port of its local server)
 *   GATE_SESSION_RUN     a file holding the console session of the run's user
 *   GATE_SESSION_SECOND  ... of the second identity (sovereign-admins, tier admin)
 *   GATE_SESSION_VIEWER  ... of the viewer identity (sovereign-viewers only)
 *   GATE_KUBECONFIG_A    the kubeconfig the gate workflow holds for region a
 *   GATE_KUBECONFIG_B    ... for region b
 */

import { readFileSync } from 'node:fs'

export type Identity = 'run' | 'second' | 'viewer'
export type Region = 'a' | 'b'

export const IDENTITIES: readonly Identity[] = ['run', 'second', 'viewer']

const SESSION_ENV: Record<Identity, string> = {
  run: 'GATE_SESSION_RUN',
  second: 'GATE_SESSION_SECOND',
  viewer: 'GATE_SESSION_VIEWER',
}

export function fqdn(): string {
  const v = process.env.GATE_FQDN ?? ''
  if (!/^[a-z0-9.-]+$/.test(v)) throw new Error('GATE_FQDN is not set to the domain of the Sovereign')
  return v
}

export function scheme(): 'https' | 'http' {
  return process.env.GATE_SCHEME === 'http' ? 'http' : 'https'
}

/** The address of a host of the Sovereign: hostURL('console', '/dashboard'). */
export function hostURL(sub: string, path = '/'): string {
  const port = process.env.GATE_PORT ? `:${process.env.GATE_PORT}` : ''
  return `${scheme()}://${sub}.${fqdn()}${port}${path.startsWith('/') ? path : `/${path}`}`
}

export function consoleURL(path = '/'): string {
  return hostURL('console', path)
}

/** The console session of an identity, read from the file the workflow wrote. */
export function sessionOf(who: Identity): string {
  const file = process.env[SESSION_ENV[who]]
  if (!file) throw new Error(`${SESSION_ENV[who]} is not set: the run has no session of the ${who} identity`)
  const value = readFileSync(file, 'utf8').trim()
  if (!value) throw new Error(`${SESSION_ENV[who]} names an empty file: the ${who} identity has no session`)
  return value
}

/** The kubeconfig the gate workflow holds for a region. */
export function kubeconfigOf(region: Region): string {
  const name = region === 'a' ? 'GATE_KUBECONFIG_A' : 'GATE_KUBECONFIG_B'
  const file = process.env[name]
  if (!file) throw new Error(`${name} is not set: regionKubectl exists only inside the gate workflow`)
  return file
}
