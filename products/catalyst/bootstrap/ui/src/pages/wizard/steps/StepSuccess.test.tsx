/**
 * StepSuccess.test.tsx — vitest coverage for the wizard's terminal step.
 *
 * Closes part of GitHub issue #126:
 *   "[I] ux: success state — link to new console + show first-time admin
 *    login flow"
 *
 * Asserts that every CTA / link in StepSuccess renders the correct URL
 * derived from the wizard store (sovereign FQDN), per the never-hardcode
 * rule (Inviolable-Principle #4): the test feeds the store a known FQDN
 * and verifies that EVERY external href is computed from it.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import {
  StepSuccess,
  sovereignSubURL,
  adminLoginAPIPath,
  kubeconfigAPIPath,
  sovereignContextName,
  buildKubeconfigMergeCommand,
  DEFAULT_FIRST_LOGIN_DOCS_URL,
  DEFAULT_KUBECONFIG_DOCS_URL,
} from './StepSuccess'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE, parseDeploymentID } from '@/entities/deployment/model'

/* ── Test fixtures ──────────────────────────────────────────────── */

const FIXTURE_FQDN = 'omantel.omani.works'
// Branded `DeploymentID` shape is 16-char lowercase hex (#749 + #754).
// Use `parseDeploymentID` so the fixture also exercises the validator.
const FIXTURE_DEPLOYMENT_ID = parseDeploymentID('deadbeefcafebabe')

const FIXTURE_RESULT = {
  sovereignFQDN: FIXTURE_FQDN,
  controlPlaneIP: '203.0.113.10',
  loadBalancerIP: '203.0.113.11',
  consoleURL: `https://console.${FIXTURE_FQDN}`,
  gitopsRepoURL: `https://gitea.${FIXTURE_FQDN}/openova/sovereign-config`,
}

beforeEach(() => {
  // Reset the persisted Zustand store between tests so leakage from one
  // assertion doesn't poison the next. We pass a partial — Zustand merges
  // with the existing actions.
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    sovereignDomainMode: 'pool',
    sovereignPoolDomain: 'omani-works',
    sovereignSubdomain: 'omantel',
    deploymentId: FIXTURE_DEPLOYMENT_ID,
    lastProvisionResult: FIXTURE_RESULT,
  })
  // wizardNav store side-effect — StepShell publishes nav state via a
  // useEffect; this is fine in the test environment (no router).
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/* ── Pure URL helpers ───────────────────────────────────────────── */

describe('URL helpers', () => {
  it('sovereignSubURL composes <sub>.<fqdn>', () => {
    expect(sovereignSubURL('omantel.omani.works', 'console')).toBe(
      'https://console.omantel.omani.works',
    )
    expect(sovereignSubURL('omantel.omani.works', 'admin')).toBe(
      'https://admin.omantel.omani.works',
    )
    expect(sovereignSubURL('omantel.omani.works', 'docs')).toBe(
      'https://docs.omantel.omani.works',
    )
  })

  it('sovereignSubURL returns empty string for empty/whitespace fqdn', () => {
    expect(sovereignSubURL('', 'console')).toBe('')
    expect(sovereignSubURL('   ', 'console')).toBe('')
  })

  it('adminLoginAPIPath is null when deployment id is missing', () => {
    expect(adminLoginAPIPath(null)).toBeNull()
    expect(adminLoginAPIPath('')).toBeNull()
  })

  it('adminLoginAPIPath returns the correct catalyst-api endpoint', () => {
    expect(adminLoginAPIPath('abc')).toMatch(
      /\/api\/v1\/deployments\/abc\/admin-login-url$/,
    )
  })

  it('kubeconfigAPIPath returns the correct catalyst-api endpoint', () => {
    expect(kubeconfigAPIPath('abc')).toMatch(
      /\/api\/v1\/deployments\/abc\/kubeconfig$/,
    )
  })

  /* Issue #765 — context-name + merge-command helpers. The backend
   * picks the same name with a Go-side mirror of this logic; the two
   * implementations agreeing is the load-bearing invariant. */
  it('sovereignContextName prefers the subdomain', () => {
    expect(sovereignContextName('otech94', 'otech94.omantel.omani.works')).toBe('otech94')
  })

  it('sovereignContextName falls back to the first FQDN label', () => {
    expect(sovereignContextName('', 'k8s.byo.example')).toBe('k8s')
  })

  it('sovereignContextName lowercases + strips invalid chars', () => {
    expect(sovereignContextName('OTECH-Beta!', '')).toBe('otech-beta')
  })

  it('sovereignContextName returns "sovereign" when both inputs are empty', () => {
    expect(sovereignContextName('', '')).toBe('sovereign')
  })

  it('buildKubeconfigMergeCommand emits a one-liner with KUBECONFIG + flatten + k9s', () => {
    const cmd = buildKubeconfigMergeCommand('otech94.yaml', 'otech94')
    expect(cmd).toMatch(/^KUBECONFIG=\$HOME\/\.kube\/config:\$HOME\/Downloads\/otech94\.yaml /)
    expect(cmd).toContain('kubectl config view --flatten')
    expect(cmd).toContain('chmod 600 $HOME/.kube/config')
    expect(cmd).toContain('k9s --context=otech94')
    // Atomic move pattern — we never write directly to ~/.kube/config
    // mid-pipe (a Ctrl-C would otherwise corrupt the operator's
    // existing config).
    expect(cmd).toContain('$HOME/.kube/config.tmp')
    expect(cmd).toContain('mv $HOME/.kube/config.tmp $HOME/.kube/config')
  })
})

/* ── Component rendering ───────────────────────────────────────── */

describe('StepSuccess CTAs render with correct hrefs', () => {
  it('renders the console URL CTA computed from sovereign FQDN', () => {
    render(<StepSuccess />)
    const consoleLink = screen.getByTestId('console-url') as HTMLAnchorElement
    expect(consoleLink.href).toBe(`https://console.${FIXTURE_FQDN}/`)
  })

  it('renders the admin username admin@<fqdn>', () => {
    render(<StepSuccess />)
    expect(screen.getByTestId('admin-username').textContent).toBe(
      `admin@${FIXTURE_FQDN}`,
    )
  })

  it('renders voucher CTA pointing at admin.<fqdn>/billing/vouchers/new', () => {
    render(<StepSuccess />)
    const voucher = screen.getByTestId('voucher-cta') as HTMLAnchorElement
    expect(voucher.href).toBe(
      `https://admin.${FIXTURE_FQDN}/billing/vouchers/new`,
    )
  })

  it('renders docs CTA pointing at docs.<fqdn>', () => {
    render(<StepSuccess />)
    const docs = screen.getByTestId('docs-cta') as HTMLAnchorElement
    expect(docs.href).toBe(`https://docs.${FIXTURE_FQDN}/`)
  })

  it('renders the GitOps repo URL when provided in the result', () => {
    render(<StepSuccess />)
    const gitops = screen.getByTestId('gitops-url') as HTMLAnchorElement
    expect(gitops.href).toBe(FIXTURE_RESULT.gitopsRepoURL)
  })

  it('falls back to resolved domain when lastProvisionResult is missing', () => {
    useWizardStore.setState({ lastProvisionResult: null })
    render(<StepSuccess />)
    const consoleLink = screen.getByTestId('console-url') as HTMLAnchorElement
    expect(consoleLink.href).toBe(`https://console.${FIXTURE_FQDN}/`)
    const voucher = screen.getByTestId('voucher-cta') as HTMLAnchorElement
    expect(voucher.href).toBe(
      `https://admin.${FIXTURE_FQDN}/billing/vouchers/new`,
    )
  })
})

/* ── First-time login fallback ─────────────────────────────────── */

describe('First-time login fallback when API endpoint is not implemented', () => {
  it('shows runbook fallback when /admin-login-url returns 404', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 404 } as Response)
    vi.stubGlobal('fetch', fetchMock)
    render(<StepSuccess />)
    fireEvent.click(screen.getByTestId('mint-one-time-url'))
    // Wait for state update
    const fallback = await screen.findByTestId('one-time-fallback')
    expect(fallback).toBeTruthy()
    const docsLink = screen.getByTestId('first-login-docs') as HTMLAnchorElement
    expect(docsLink.href).toBe(DEFAULT_FIRST_LOGIN_DOCS_URL)
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringMatching(/\/api\/v1\/deployments\/deadbeefcafebabe\/admin-login-url$/),
      expect.any(Object),
    )
  })

  it('renders the minted one-time URL when backend returns 200', async () => {
    const minted = `https://console.${FIXTURE_FQDN}/auth/realms/catalyst-admin/login-actions/action-token?key=tok`
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ url: minted }),
    } as Response)
    vi.stubGlobal('fetch', fetchMock)
    render(<StepSuccess />)
    fireEvent.click(screen.getByTestId('mint-one-time-url'))
    const link = await screen.findByTestId('one-time-url')
    expect((link as HTMLAnchorElement).href).toBe(minted)
  })
})

/* ── kubeconfig fallback ───────────────────────────────────────── */

describe('kubeconfig fallback when endpoint is not implemented', () => {
  it('shows the SSH-coming-soon copy when /kubeconfig returns 501', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 501 } as Response)
    vi.stubGlobal('fetch', fetchMock)
    render(<StepSuccess />)
    fireEvent.click(screen.getByTestId('download-kubeconfig'))
    const fallback = await screen.findByTestId('kubeconfig-fallback')
    expect(fallback.textContent).toMatch(/Coming soon — fetch via SSH/)
    const docsLink = screen.getByTestId('kubeconfig-docs') as HTMLAnchorElement
    expect(docsLink.href).toBe(DEFAULT_KUBECONFIG_DOCS_URL)
  })
})

/* ── kubeconfig merge command (issue #765) ─────────────────────── */

describe('kubeconfig merge command surface', () => {
  it('renders the merge one-liner with the right context name + filename', () => {
    render(<StepSuccess />)
    const cmd = screen.getByTestId('kubeconfig-merge-command')
    // Subdomain in fixture is "omantel" → context name omantel, filename omantel.yaml.
    expect(cmd.textContent).toContain('$HOME/Downloads/omantel.yaml')
    expect(cmd.textContent).toContain('k9s --context=omantel')
    expect(cmd.textContent).toContain('kubectl config view --flatten')
  })

  it('"Copy" button copies the command to clipboard and flips to Copied', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    render(<StepSuccess />)
    const btn = screen.getByTestId('copy-merge-command')
    expect(btn.textContent).toMatch(/Copy/)
    fireEvent.click(btn)
    // writeText invoked with a string containing the full pipeline.
    expect(writeText).toHaveBeenCalledTimes(1)
    const written = String(writeText.mock.calls[0][0])
    expect(written).toContain('KUBECONFIG=$HOME/.kube/config:$HOME/Downloads/omantel.yaml')
    expect(written).toContain('k9s --context=omantel')
    // Wait for state flip to "Copied".
    await screen.findByText(/Copied/)
  })

  it('uses a different filename + context when subdomain changes', () => {
    useWizardStore.setState({
      sovereignSubdomain: 'otech94',
      sovereignPoolDomain: 'omani-works',
      lastProvisionResult: {
        ...FIXTURE_RESULT,
        sovereignFQDN: 'otech94.omani.works',
      },
    })
    render(<StepSuccess />)
    const cmd = screen.getByTestId('kubeconfig-merge-command')
    expect(cmd.textContent).toContain('$HOME/Downloads/otech94.yaml')
    expect(cmd.textContent).toContain('k9s --context=otech94')
  })
})

/* ── Log tail expander ─────────────────────────────────────────── */

describe('SSE final-state log tail', () => {
  it('renders collapsed by default and expands to show the last 20 lines', () => {
    const tail = Array.from({ length: 25 }, (_, i) => `line-${i}`)
    render(<StepSuccess finalLogTail={tail} />)
    // Collapsed initially — pre is not in the DOM
    expect(screen.queryByTestId('log-tail-pre')).toBeNull()
    fireEvent.click(screen.getByTestId('log-tail-toggle'))
    const pre = screen.getByTestId('log-tail-pre')
    // Only the last 20 lines should be present
    expect(pre.textContent).toContain('line-5')
    expect(pre.textContent).toContain('line-24')
    expect(pre.textContent).not.toContain('line-4')
  })
})

/* ── Hardcoded-URL hygiene smoke test ──────────────────────────── */

describe('No hardcoded sovereign URL', () => {
  it('every external CTA points at the configured FQDN, not a hardcoded one', () => {
    const newFQDN = 'sovereign.acme-bank.com'
    useWizardStore.setState({
      sovereignDomainMode: 'byo-manual',
      sovereignByoDomain: newFQDN,
      lastProvisionResult: {
        ...FIXTURE_RESULT,
        sovereignFQDN: newFQDN,
        consoleURL: `https://console.${newFQDN}`,
      },
    })
    render(<StepSuccess />)
    expect((screen.getByTestId('console-url') as HTMLAnchorElement).href).toBe(
      `https://console.${newFQDN}/`,
    )
    expect((screen.getByTestId('voucher-cta') as HTMLAnchorElement).href).toBe(
      `https://admin.${newFQDN}/billing/vouchers/new`,
    )
    expect((screen.getByTestId('docs-cta') as HTMLAnchorElement).href).toBe(
      `https://docs.${newFQDN}/`,
    )
    expect(screen.getByTestId('admin-username').textContent).toBe(
      `admin@${newFQDN}`,
    )
  })
})
