#!/usr/bin/env node
// Helper for sso-landing-stale-token-revalidate.sh (#5459, UAT row 183).
//
// Loads the ACTUAL landing-page <script> body extracted from a live `helm
// template` render (never a hand-copied snippet — this is what would have
// masked the regression) into a Node `vm` context with mocked
// window/document/localStorage/fetch, seeds a CACHED token whose
// CLIENT-SIDE tokenExpirationEpoch is far in the future (looks valid), and
// observes two things the real browser would do: which URLs the script
// fetch()es, and what it passes to window.location.replace().
//
// Usage: node _sso_landing_stale_token_check.js <path-to-extracted-js> <scenario>
//   scenario = "revoked" -> mock GET .../auth/token/lookup-self => 403
//   scenario = "valid"   -> mock GET .../auth/token/lookup-self => 200
//
// Prints one JSON line: {"fetchCalls":[...],"replaceCalls":[...]}

'use strict';
const fs = require('fs');
const vm = require('vm');

const jsPath = process.argv[2];
const scenario = process.argv[3];
if (!jsPath || !scenario) {
  console.error('usage: node _sso_landing_stale_token_check.js <js-file> <revoked|valid>');
  process.exit(2);
}
const src = fs.readFileSync(jsPath, 'utf8');

function makeStorage(initial) {
  const map = new Map(Object.entries(initial));
  return {
    getItem: function (k) { return map.has(k) ? map.get(k) : null; },
    setItem: function (k, v) { map.set(k, String(v)); },
    removeItem: function (k) { map.delete(k); },
  };
}

const fetchCalls = [];
const replaceCalls = [];

// A token that LOOKS locally valid — ttl 24h, minted "now" — but whose
// server-side fate is controlled by `scenario` below. This is the exact
// shape persist() in the landing page writes.
const cached = JSON.stringify({
  token: 's.testcachedtoken00000',
  policies: ['default', 'sso-operator-read'],
  renewable: true,
  entity_id: 'e-test-0000',
  ttl: 86400,
  tokenExpirationEpoch: Date.now() + 24 * 3600 * 1000,
});

const storage = makeStorage({ 'vault-token☃1': cached });

function mockFetch(url) {
  fetchCalls.push(String(url));
  if (String(url).indexOf('/auth/token/lookup-self') !== -1) {
    if (scenario === 'valid') {
      return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve({ data: { display_name: 'oidc-test' } }); } });
    }
    return Promise.resolve({ ok: false, status: 403, json: function () { return Promise.resolve({ errors: ['permission denied'] }); } });
  }
  if (String(url).indexOf('/oidc/auth_url') !== -1) {
    return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve({ data: { auth_url: 'https://kc.example.test/realms/sovereign/protocol/openid-connect/auth?x=1' } }); } });
  }
  return Promise.reject(new Error('unexpected fetch: ' + url));
}

function stubEl() {
  return { style: {}, textContent: '', innerHTML: '' };
}

const ctx = {
  console: console,
  Date: Date,
  JSON: JSON,
  URLSearchParams: URLSearchParams,
  Promise: Promise,
  window: {
    location: {
      origin: 'https://bao.hw133.omani.works',
      search: '',
      replace: function (url) { replaceCalls.push(String(url)); },
    },
    localStorage: storage,
  },
  document: { getElementById: stubEl },
  fetch: mockFetch,
};
vm.createContext(ctx);
vm.runInContext(src, ctx, { filename: 'sso-landing.js' });

// The re-entry path chains at least one `.then` (lookup-self) and, on the
// invalid branch, a second round-trip (begin() -> auth_url). Flush the
// microtask/macrotask queue before reporting.
setTimeout(function () {
  process.stdout.write(JSON.stringify({ fetchCalls: fetchCalls, replaceCalls: replaceCalls }) + '\n');
}, 100);
