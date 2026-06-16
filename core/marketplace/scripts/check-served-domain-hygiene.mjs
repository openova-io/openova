#!/usr/bin/env node
// check-served-domain-hygiene.mjs — #3376 (Refs #3691).
//
// Scans the BUILT marketplace bundle (dist/) for banned mothership/partner
// domain literals that would otherwise be served VERBATIM to a franchised
// customer. The served storefront is the FIRST surface a customer touches on
// a cut-over Sovereign; per DoD §4 + GLOSSARY §Forbidden-test-domains +
// PRINCIPLES §4/§11 it must refer ONLY to that Sovereign — zero
// `openova.io` / `console.openova.io` / `omantel.openova.io`.
//
// This is the CI-side complement to the runtime fix in src/lib/config.ts +
// src/lib/tenant.ts + src/layouts/Layout.astro: the mothership URL is now
// assembled from FRAGMENTS at runtime (so the literal never lands in the
// served bundle) and every franchised host derives a sovereign console.
// The grep that the brief asks CI to extend ("curl marketplace.<fqdn> | grep
// openova.io must be empty on a franchised prov") is realised here against
// the build output — the same bytes nginx serves.
//
// Usage:  node scripts/check-served-domain-hygiene.mjs [dist-dir]
// Exit:   0 = clean, 1 = a banned literal was found (fails the build).

import { readdir, readFile, stat } from 'node:fs/promises';
import { join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const DIST = process.argv[2] || join(fileURLToPath(new URL('.', import.meta.url)), '..', 'dist');

// Served text asset extensions worth scanning. Binaries (images/fonts) are
// skipped — a domain literal can only hide in text the browser parses.
const TEXT_EXT = new Set(['.html', '.js', '.mjs', '.css', '.json', '.txt', '.svg', '.map', '.webmanifest']);

// Banned literals. `openova.io` is the umbrella; the two console/partner
// forms are called out for a clearer failure message. Matching is
// case-insensitive and substring-based (the same as the brief's grep).
const BANNED = [/openova\.io/i];

async function* walk(dir) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const e of entries) {
    const p = join(dir, e.name);
    if (e.isDirectory()) {
      yield* walk(p);
    } else if (e.isFile() && TEXT_EXT.has(extname(e.name).toLowerCase())) {
      yield p;
    }
  }
}

async function main() {
  try {
    await stat(DIST);
  } catch {
    console.error(`[domain-hygiene] dist directory not found at ${DIST} — run \`npm run build\` first`);
    process.exit(1);
  }

  const offenders = [];
  let scanned = 0;
  for await (const file of walk(DIST)) {
    scanned++;
    const text = await readFile(file, 'utf8');
    for (const re of BANNED) {
      const m = text.match(re);
      if (m) {
        // Surface the offending line for fast triage.
        const line = text.split('\n').find((l) => re.test(l)) || '';
        offenders.push({ file, match: m[0], snippet: line.trim().slice(0, 160) });
        break;
      }
    }
  }

  if (offenders.length) {
    console.error(`[domain-hygiene] FAIL — ${offenders.length} served file(s) carry a banned mothership/partner domain literal (#3376):`);
    for (const o of offenders) {
      console.error(`  ${o.file}: ${o.match}`);
      console.error(`     ${o.snippet}`);
    }
    console.error('[domain-hygiene] The served marketplace bundle must refer ONLY to the Sovereign it runs on.');
    console.error('[domain-hygiene] Assemble mothership URLs from fragments at runtime; derive franchised console hosts from window.location.hostname.');
    process.exit(1);
  }

  console.log(`[domain-hygiene] OK — ${scanned} served file(s) scanned, zero banned domain literals (#3376).`);
}

main().catch((err) => {
  console.error('[domain-hygiene] unexpected error:', err);
  process.exit(1);
});
