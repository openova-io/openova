// renderedCopy.ts — extract the words a CUSTOMER actually reads from a funnel
// component, as opposed to the identifiers a developer reads.
//
// #5646. The funnel is the storefront a paying customer walks after checkout,
// and docs/GLOSSARY.md §Banned-terms governs every word on it ("tenant" →
// "Organization"). Enforcing that with a plain grep is useless here, because the
// SAME word is legitimate and unavoidable in this tree as an identifier:
//
//   localStorage.getItem('org-checkout-tenant')      ← internal storage key
//   request<Tenant>('/tenant/orgs')                  ← API path + TS type
//   html[data-tenant=...]                            ← styling hook
//   <!-- Logo (tenant-aware: ...) -->                ← code comment
//
// A grep for the banned term flags all four, so a grep-shaped guard is one that
// gets switched off within a week. What matters is narrower and checkable: the
// term must not appear in RENDERED TEXT. So this module parses the component and
// returns only the strings that reach the page — template text nodes plus the
// handful of attributes screen readers and tooltips surface — and deliberately
// drops <script>, <style>, comments, expression tags and every other attribute.
//
// That distinction is the whole guard. `Header.svelte` carried BOTH a rendered
// "Console (my tenants)" and a comment mentioning `data-tenant`; only the first
// is a defect, and only the first is returned here.

/** A single customer-readable string, with enough context to report it. */
export interface RenderedString {
  /** The text as the customer reads it, whitespace-collapsed. */
  text: string;
  /** Where it came from — 'text' for a template text node, or the attribute name. */
  kind: string;
}

/**
 * Attributes whose literal value is presented to a user (visually, on hover, or
 * through a screen reader). Everything else — class, href, data-*, id, bind:,
 * on: — is an identifier surface and is intentionally NOT extracted.
 */
const USER_VISIBLE_ATTRS = new Set([
  'alt',
  'aria-label',
  'aria-description',
  'aria-placeholder',
  'aria-roledescription',
  'placeholder',
  'title',
  'label',
]);

const collapse = (s: string) => s.replace(/\s+/g, ' ').trim();

function push(out: RenderedString[], text: string, kind: string) {
  const t = collapse(text);
  if (t) out.push({ text: t, kind });
}

/**
 * Extract rendered copy from a Svelte component using Svelte's own parser, so
 * the notion of "this is markup, that is script" is the compiler's and not a
 * regex approximation. Walks EVERY branch ({#if}/{#each}/{:else}), which an
 * SSR-render-and-scan approach would not — a render only exercises the branch
 * its props select, and the banned string may well sit in the other one.
 */
export function extractSvelteRenderedText(source: string): RenderedString[] {
  // Imported lazily and synchronously via require-style interop so this module
  // stays usable from a plain vitest run without a Svelte plugin.
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const { parse } = require('svelte/compiler');
  const ast = parse(source, { modern: true });
  const out: RenderedString[] = [];

  // NB: ast.instance / ast.module are the <script> blocks and are never walked.
  walkFragment(ast.fragment, out);
  return out;
}

function walkFragment(fragment: any, out: RenderedString[]) {
  if (!fragment || !Array.isArray(fragment.nodes)) return;
  for (const node of fragment.nodes) walkNode(node, out);
}

function walkNode(node: any, out: RenderedString[]) {
  if (!node || typeof node !== 'object') return;

  switch (node.type) {
    case 'Text':
      push(out, node.data ?? node.raw ?? '', 'text');
      return;

    // Explicitly dropped: comments are developer copy, expression tags render a
    // runtime VALUE (guarded on the producer side, in Go), and <style> is not copy.
    case 'Comment':
    case 'ExpressionTag':
    case 'HtmlTag':
      return;

    case 'RegularElement':
    case 'Component':
    case 'SvelteElement':
    case 'SvelteComponent':
    case 'SlotElement':
    case 'TitleElement': {
      if (node.name === 'script' || node.name === 'style') return;
      for (const attr of node.attributes ?? []) collectAttr(attr, out);
      walkFragment(node.fragment, out);
      return;
    }

    default: {
      // Block constructs ({#if}/{#each}/{#await}/{#snippet}/{#key}) all hang
      // their bodies off named fragment properties. Walking them generically
      // means a new Svelte block type is covered the day it appears, rather
      // than silently escaping the guard.
      for (const key of ['fragment', 'body', 'consequent', 'alternate', 'pending', 'then', 'catch', 'fallback']) {
        const child = node[key];
        if (child && typeof child === 'object') {
          if (Array.isArray(child.nodes)) walkFragment(child, out);
          else walkNode(child, out);
        }
      }
    }
  }
}

function collectAttr(attr: any, out: RenderedString[]) {
  if (!attr || attr.type !== 'Attribute') return;
  if (!USER_VISIBLE_ATTRS.has(String(attr.name).toLowerCase())) return;

  const v = attr.value;
  if (v === true) return;
  const parts = Array.isArray(v) ? v : [v];
  for (const p of parts) {
    // Only STATIC values. A {expression} attribute renders a runtime value.
    if (p && p.type === 'Text') push(out, p.data ?? p.raw ?? '', `@${attr.name}`);
  }
}

/**
 * Extract rendered copy from an Astro page.
 *
 * Astro has no published synchronous parser in this package's dependency set,
 * so this is a deliberately CONSERVATIVE markup scan: everything that is
 * definitively not rendered copy is removed first (frontmatter, script, style,
 * comments, expressions), and what survives between tags is treated as copy.
 * Conservative in the safe direction — it may return slightly more than the
 * customer sees, never less, so the guard cannot go quietly blind.
 */
export function extractAstroRenderedText(source: string): RenderedString[] {
  let s = source;

  // Frontmatter fence (--- ... ---) at the top of the file: TypeScript, not copy.
  s = s.replace(/^\s*---[\s\S]*?\n---/, '');
  // Inline <script>/<style> bodies — where `var tenant = ...` legitimately lives.
  s = s.replace(/<script[\s\S]*?<\/script>/gi, '');
  s = s.replace(/<style[\s\S]*?<\/style>/gi, '');
  // HTML and JS comments.
  s = s.replace(/<!--[\s\S]*?-->/g, '');
  // {expressions} render runtime values, not literal copy.
  s = s.replace(/\{[^{}]*\}/g, ' ');

  const out: RenderedString[] = [];

  // User-visible attributes, before tags are stripped.
  const attrRE = new RegExp(`\\b(${[...USER_VISIBLE_ATTRS].join('|')})\\s*=\\s*"([^"]*)"`, 'gi');
  for (const m of s.matchAll(attrRE)) push(out, m[2], `@${m[1].toLowerCase()}`);

  // Then the text between tags. Tags become line breaks and each surviving line
  // is one chunk, so a rendered phrase stays intact in the failure message
  // instead of being reported as a bag of words.
  for (const chunk of s.replace(/<[^>]*>/g, '\n').split('\n')) push(out, chunk, 'text');

  return out;
}

/** Dispatch on file extension. */
export function extractRenderedText(source: string, filename: string): RenderedString[] {
  if (filename.endsWith('.svelte')) return extractSvelteRenderedText(source);
  if (filename.endsWith('.astro')) return extractAstroRenderedText(source);
  return [];
}
