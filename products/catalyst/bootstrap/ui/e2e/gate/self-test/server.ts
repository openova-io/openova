/**
 * e2e/gate/self-test/server.ts — the stand-in Sovereign of the harness self-test (FND-161).
 *
 * The global setup of a self-test run. Every host of the stand-in domain
 * (the browser sends *.self-test.invalid to 127.0.0.1) gets a page that
 * shows the host it was asked for, the console session cookie the browser
 * sent, and a canvas that writes every key it receives into #typed.
 */

import { createServer } from 'node:http'

function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  const port = Number(process.env.GATE_PORT)
  if (!Number.isInteger(port) || port <= 0) throw new Error('GATE_PORT is not the port of the self-test server')
  const server = createServer((req, res) => {
    const host = (req.headers.host ?? '').split(':')[0]
    const session = /(?:^|;\s*)catalyst_session=([^;]*)/.exec(req.headers.cookie ?? '')?.[1] ?? ''
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
    res.end(`<!doctype html><title>${escapeHTML(host)}</title>
<p id="host">${escapeHTML(host)}</p><p id="session">${escapeHTML(session)}</p>
<canvas id="screen" width="320" height="120" tabindex="0"></canvas><p id="typed"></p>
<script>const t = document.getElementById('typed');
document.getElementById('screen').addEventListener('keydown', (e) => { t.textContent += e.key.length === 1 ? e.key : '[' + e.key + ']'; });</script>`)
  })
  await new Promise<void>((ok, fail) => {
    server.once('error', fail)
    server.listen(port, '127.0.0.1', () => ok())
  })
  return async () => {
    await new Promise<void>((ok) => server.close(() => ok()))
  }
}
