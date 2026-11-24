// Gate check FND-G22 (FND-164): the customer's passwordless sign-in on the storefront. The sign-in code is
// mailed to the gate mailbox of the Sovereign's own mail server (the Secret
// platform/stalwart-sovereign-conformance, as check BSS-G01 reads it) and read there over IMAP, read-only,
// through a port-forward of the API server. The customer address is the mailbox's own with the fixed tag
// fnd-g22, so every run signs in the same customer. One test per ledger row; the title is the row id and
// its clause.
import { test, expect } from '../fixtures.ts'
import { spawn } from 'node:child_process'
import { connect } from 'node:tls'
import { kubeconfigOf, type Region } from '../env.ts'
import { REGIONS, at, expectNoLoginForm, getObj, host, until } from './lib.ts'

const SUBJECT = 'Your login code' // core/services/auth/handlers/handlers.go, sendCodeEmail

interface Mailbox { region: Region; service: string; port: number; host: string; user: string; password: string; address: string }

async function mailbox(): Promise<Mailbox> {
  for (const r of REGIONS) {
    const s = await getObj(r, 'secret', 'platform', 'stalwart-sovereign-conformance')
    if (!s) continue
    const v = (k: string) => Buffer.from(String(at(s, 'data', k) ?? ''), 'base64').toString('utf8').trim()
    const [local, domain] = v('address').split('@')
    expect(Boolean(local && domain && v('user') && v('password') && v('imap-host') && v('imap-service')), 'the gate mailbox Secret holds its keys').toBe(true)
    return { region: r, service: v('imap-service'), port: Number(v('imap-port')), host: v('imap-host'), user: v('user'), password: v('password'), address: `${local}+fnd-g22@${domain}` }
  }
  throw new Error('no region holds the Secret platform/stalwart-sovereign-conformance: the Sovereign has no gate mailbox to read the code from')
}

/** kubectl port-forward to a Service; the local port and a stop. */
function forward(m: Mailbox): Promise<{ port: number; stop: () => void }> {
  const p = spawn('kubectl', ['--kubeconfig', kubeconfigOf(m.region), '-n', 'platform', 'port-forward', `service/${m.service}`, `:${m.port}`])
  return new Promise((done, fail) => {
    const timer = setTimeout(() => { p.kill(); fail(new Error('kubectl port-forward: no local port within 30 s')) }, 30_000)
    p.stdout.on('data', (d) => {
      const hit = /127\.0\.0\.1:(\d+)/.exec(String(d))
      if (hit) { clearTimeout(timer); done({ port: Number(hit[1]), stop: () => p.kill() }) }
    })
    p.on('exit', (c) => { clearTimeout(timer); fail(new Error(`kubectl port-forward exited ${c}`)) })
  })
}

/** An IMAP session over implicit TLS: one command at a time, the untagged lines and literals it answered. */
function imap(port: number, servername: string): Promise<{ cmd: (c: string) => Promise<string>; close: () => void }> {
  return new Promise((ready, fail) => {
    const sock = connect({ host: '127.0.0.1', port, servername })
    sock.setEncoding('latin1')
    let buf = ''
    let greeted = false
    let n = 0
    let wait: { tag: string; verb: string; done: (s: string) => void; fail: (e: Error) => void } | null = null
    const cmd = (c: string) => new Promise<string>((done, f) => {
      const tag = `g${++n}`
      wait = { tag, verb: c.split(' ')[0], done, fail: f }
      sock.write(`${tag} ${c}\r\n`)
    })
    sock.on('data', (d: string) => {
      buf += d
      if (!greeted) {
        if (buf.includes('\r\n')) { greeted = true; buf = ''; ready({ cmd, close: () => sock.end() }) }
        return
      }
      const w = wait
      if (!w) return
      const end = new RegExp(`(^|\\r\\n)${w.tag} (OK|NO|BAD)[^\\r\\n]*\\r\\n`).exec(buf)
      if (!end) return
      const out = buf
      buf = ''
      wait = null
      if (end[2] === 'OK') w.done(out)
      else w.fail(new Error(`IMAP ${w.verb} answered ${end[2]}`)) // never the command: LOGIN holds the password
    })
    sock.on('error', (e) => (wait ? wait.fail(e) : fail(e)))
  })
}

/** The UIDs of the sign-in mails to an address, and the code of the newest one above a floor. */
async function codeMails(m: Mailbox, floor: number): Promise<{ newest: number; code: string }> {
  const fwd = await forward(m)
  try {
    const s = await imap(fwd.port, m.host)
    try {
      await s.cmd(`LOGIN ${JSON.stringify(m.user)} ${JSON.stringify(m.password)}`)
      await s.cmd('EXAMINE INBOX')
      const found = /\* SEARCH([\d ]*)/.exec(await s.cmd(`UID SEARCH TO ${JSON.stringify(m.address)} SUBJECT ${JSON.stringify(SUBJECT)}`))
      const uids = (found?.[1] ?? '').trim().split(/\s+/).filter((x) => x !== '').map(Number)
      const newest = Math.max(0, ...uids)
      if (newest <= floor) return { newest, code: '' }
      const body = (await s.cmd(`UID FETCH ${newest} BODY.PEEK[TEXT]`)).replace(/=\r\n/g, '')
      const code = /login code:\s*(\d{6})\b/i.exec(body)?.[1] ?? />\s*(\d{6})\s*</.exec(body)?.[1] ?? ''
      return { newest, code }
    } finally {
      await s.cmd('LOGOUT').catch(() => undefined)
      s.close()
    }
  } finally {
    fwd.stop()
  }
}

test('217 The user logs in via the passwordless SSO magic-email PIN (any domain, e.g. demo@openova.io) → 6-digit PIN emailed → lands signed-in, no password. PASS = the PIN actually arrives (verify IMAP mail.openova.io:993) AND entering it lands signed-in as the CUSTOMER (their email in the avatar, their Org in the header) with no password field — this is the customer PIN flow, NOT the operator handover.', async ({ browser, request }) => {
  test.setTimeout(480_000)
  // The PIN is read from the Sovereign's own gate mailbox, not the walk's mail.openova.io. Their Org in the
  // header: the Organizations the storefront holds for the customer (GET /api/tenant/orgs, the list
  // layouts/Layout.astro routes a signed-in customer by), one of them named in the header that holds the
  // avatar. A customer who owns no Organization leaves the header nothing to name, and the test fails with
  // that reason.
  const m = await mailbox()
  const floor = (await codeMails(m, Number.MAX_SAFE_INTEGER)).newest
  const ctx = await browser.newContext()
  try {
    const p = await ctx.newPage()
    await p.goto(host('marketplace', '/checkout'))
    await p.locator('input[type="email"]').fill(m.address)
    await p.getByRole('button', { name: 'Send sign-in code' }).click()
    const mail = await until('the sign-in code arrives in the mailbox', async () => { const x = await codeMails(m, floor); return x.code ? x : null }, 300_000, 10_000)
    expect(mail.code, 'a 6-digit code').toMatch(/^\d{6}$/)
    await p.locator('input[maxlength="6"]').fill(mail.code, { force: true })
    const verify = p.getByRole('button', { name: 'Verify & Continue' })
    if (await verify.isEnabled().catch(() => false)) await verify.click().catch(() => undefined)
    await expect.poll(() => p.evaluate(() => window.localStorage.getItem('org-token') ?? ''), { message: 'the customer is signed in', timeout: 60_000 }).not.toBe('')
    await expectNoLoginForm(p, 'the storefront after the code')
    await p.getByRole('button', { name: 'My account' }).click()
    await expect(p.getByRole('menu'), 'the avatar menu names the customer').toContainText(m.address)
    const token = await p.evaluate(() => window.localStorage.getItem('org-token') ?? '')
    const me = await request.get(host('marketplace', '/api/auth/me'), { headers: { Authorization: `Bearer ${token}` } })
    expect(me.status(), 'GET /api/auth/me').toBe(200)
    expect(((await me.json()) as { user?: { email?: string } }).user?.email, 'the signed-in customer').toBe(m.address)
    const listed = await request.get(host('marketplace', '/api/tenant/orgs'), { headers: { Authorization: `Bearer ${token}` } })
    expect(listed.status(), 'GET /api/tenant/orgs').toBe(200)
    const orgs = (await listed.json()) as Array<{ slug?: string; name?: string; status?: string }>
    expect(Array.isArray(orgs), 'GET /api/tenant/orgs answers a list').toBe(true)
    const own = orgs.filter((o) => o.status !== 'deleted')
    expect(own.map((o) => o.slug), `the Organizations of ${m.address}: the customer owns one for the header to name`).not.toEqual([])
    const header = p.locator('header').filter({ has: p.getByRole('button', { name: 'My account' }) })
    const named = async () => { const text = await header.innerText(); return own.filter((o) => (o.name ?? '') !== '' && text.includes(o.name as string)).map((o) => o.slug) }
    await expect.poll(named, { message: `the header names an Organization of the customer (${own.map((o) => o.slug).join(', ')})`, timeout: 30_000 }).not.toEqual([])
  } finally {
    await ctx.close()
  }
})
