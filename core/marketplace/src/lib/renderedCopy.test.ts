// #5646 — the banned term must never reach the customer's screen in the funnel.
//
// The issue was filed on "Creating tenant" in the provisioning timeline, which
// is producer-side and guarded in Go
// (core/services/provisioning/handlers/customer_facing_copy_5646_test.go). This
// is the other half: the copy the funnel itself renders.
//
// WHY THIS IS NOT A GREP. `core/marketplace/src` contains ~30 legitimate uses of
// the word as an identifier — the `/tenant/orgs` API path, the
// `org-checkout-tenant` localStorage key, the `Tenant` TypeScript interface, the
// `data-tenant` styling hook, code comments. A guard that flagged those would be
// unusable and would be deleted. So the assertion is made on RENDERED TEXT
// extracted by `renderedCopy.ts` — the words a customer reads — and the control
// below pins that every one of those identifier uses stays unflagged.

import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import {
  extractRenderedText,
  extractSvelteRenderedText,
  extractAstroRenderedText,
} from './renderedCopy';

// vitest runs with the package root as cwd (core/marketplace).
const SRC = join(process.cwd(), 'src');

/**
 * docs/GLOSSARY.md §Banned-terms: "tenant" → "Organization".
 *
 * Case-insensitive and NOT word-anchored, so the spellings that would dodge a
 * naive search are all covered: "Tenant", "TENANT", "tenants", "per-tenant",
 * and the term embedded in a runtime object name (`tenant-uatco-kubeconfig`) —
 * that last shape is how #5435 put the term on a console screen with no source
 * string to grep for.
 */
const BANNED = /tenant/i;

function walkFiles(dir: string, exts: string[], acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry.startsWith('.')) continue;
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) walkFiles(p, exts, acc);
    else if (exts.some((e) => entry.endsWith(e))) acc.push(p);
  }
  return acc;
}

describe('#5646 — no banned product term in rendered funnel copy', () => {
  const files = walkFiles(SRC, ['.svelte', '.astro']);

  it('finds the funnel components (guard is not scanning an empty set)', () => {
    // Without this, a bad glob would make every assertion below pass on nothing.
    expect(files.length).toBeGreaterThan(15);
  });

  it.each(files.map((f) => [relative(SRC, f), f]))(
    '%s renders no banned term',
    (rel, abs) => {
      const strings = extractRenderedText(readFileSync(abs, 'utf8'), abs);
      const offenders = strings.filter((s) => BANNED.test(s.text));

      expect(
        offenders,
        `${rel} renders the banned term "tenant" to a customer.\n` +
          offenders.map((o) => `  [${o.kind}] ${o.text}`).join('\n') +
          `\ndocs/GLOSSARY.md §Banned-terms: use "Organization".\n` +
          `If this is an INTERNAL identifier (API path, storage key, type name, ` +
          `comment, data-attribute) it should not be reaching rendered text at all.`,
      ).toEqual([]);
    },
  );
});

describe('#5646 control — legitimate internal uses stay unflagged', () => {
  // Every pattern below is real code from this tree. If the extractor ever
  // starts returning these, the guard becomes noise and gets disabled — so this
  // is as load-bearing as the assertion above, and it is green on BOTH the
  // pre-fix and post-fix trees.
  it('ignores script identifiers, comments, attribute expressions and data-attributes', () => {
    const component = `
<script lang="ts">
  import { createTenant, getProvisionByTenant, type Tenant } from '../lib/api';
  let tenantId = $state('');
  const saved = localStorage.getItem('org-checkout-tenant');
  const url = \`\${API_BASE}/provisioning/tenant/\${id}\`;
</script>

<!-- Logo (tenant-aware: rendered unconditionally; CSS hides the inactive one
     based on html[data-tenant=...] set by the pre-hydration script) -->
<div data-tenant={activeTenant} class="tenant-logo" id="tenant-root">
  <a href="/tenant/orgs">Console (my Organizations)</a>
  <span>{tenantId}</span>
</div>
`;
    const rendered = extractSvelteRenderedText(component);
    expect(rendered.filter((s) => BANNED.test(s.text))).toEqual([]);
    // ...and it did not simply return nothing: the real copy is still there.
    expect(rendered.map((s) => s.text)).toContain('Console (my Organizations)');
  });

  it('ignores an inline <script> in an Astro page (redeem.astro shape)', () => {
    const page = `---
import Layout from '../layouts/Layout.astro';
const tenant = Astro.props.tenant;
---
<Layout title="Redeem">
  <p>Redeem your voucher to create your Organization.</p>
  <script>
    var tenant = (window as any).__ORG_TENANT__;
    var skipConsoleRedirect = !!(tenant && tenant.skipConsoleRedirect);
  </script>
</Layout>`;
    const rendered = extractAstroRenderedText(page);
    expect(rendered.filter((s) => BANNED.test(s.text))).toEqual([]);
    expect(rendered.map((s) => s.text)).toContain(
      'Redeem your voucher to create your Organization.',
    );
  });
});

describe('#5646 vacuity — the extractor can still go red', () => {
  // An absence-assertion passes both when the term is absent AND when the
  // extractor stopped working. These pin that it still SEES rendered copy, so a
  // broken walker cannot turn the suite permanently green on nothing.
  it('catches the banned term in Svelte rendered text, including inside blocks', () => {
    const offending = `
<script>let user = {};</script>
{#if user}
  <div>
    <a href="/portal" aria-label="Open the tenant console">Console (my tenants)</a>
  </div>
{:else}
  <p>Sign in to see your tenants</p>
{/if}`;
    const hits = extractSvelteRenderedText(offending).filter((s) => BANNED.test(s.text));
    const texts = hits.map((h) => h.text);

    expect(texts).toContain('Console (my tenants)');          // the string this PR removed
    expect(texts).toContain('Open the tenant console');       // aria-label surface
    expect(texts).toContain('Sign in to see your tenants');   // the {:else} branch
  });

  it('catches the banned term in Astro rendered text', () => {
    const offending = `---
const x = 1;
---
<p>Your tenant is being created</p>`;
    const hits = extractAstroRenderedText(offending).filter((s) => BANNED.test(s.text));
    expect(hits.map((h) => h.text)).toContain('Your tenant is being created');
  });

  it('catches a runtime-shaped object name if one is ever rendered (#5435 class)', () => {
    const hits = extractSvelteRenderedText(
      '<span>could not read tenant-uatco-kubeconfig</span>',
    ).filter((s) => BANNED.test(s.text));
    expect(hits.length).toBeGreaterThan(0);
  });
});
