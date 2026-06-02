<!--
  G117 — PortalShell wrapper.

  Lightweight wrapper so every G117 surface inherits the same header + nav
  + padding. Memory `feedback_subagents_inherit_design_system.md` mandates
  that NO bespoke layout ships in sub-agent output — every page wraps in
  this shell.

  Mirrors the design tokens used by core/console/src/components/PortalShell.svelte
  but does not import from that file (catalyst console is a standalone Astro
  app under products/catalyst/console/). When the two consoles merge per
  the lean-doc plan, this component re-exports core/console's PortalShell.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    breadcrumbs = [],
    children,
  }: {
    title: string;
    breadcrumbs?: Array<{ label: string; href?: string }>;
    children: Snippet;
  } = $props();
</script>

<div class="portal-shell">
  <header class="topbar">
    <a class="brand" href="/">OpenOva · Catalyst</a>
    <nav class="primary">
      <a href="/catalog/grafana">Catalog</a>
      <a href="/apps">Applications</a>
    </nav>
  </header>

  <div class="page">
    {#if breadcrumbs.length > 0}
      <nav class="breadcrumbs" aria-label="Breadcrumb">
        {#each breadcrumbs as crumb, i (crumb.label + i)}
          {#if crumb.href}
            <a href={crumb.href}>{crumb.label}</a>
          {:else}
            <span>{crumb.label}</span>
          {/if}
          {#if i < breadcrumbs.length - 1}<span class="sep">/</span>{/if}
        {/each}
      </nav>
    {/if}

    <h1 class="page-title">{title}</h1>

    {@render children()}
  </div>
</div>

<style>
  .portal-shell {
    --color-bg: #f8fafc;
    --color-surface: #ffffff;
    --color-border: #e2e8f0;
    --color-text: #0f172a;
    --color-muted: #64748b;
    --color-accent: #1d4ed8;
    --color-accent-hover: #1e40af;
    --color-success: #047857;
    --color-warn: #b45309;
    --color-danger: #b00020;
    font-family: 'Inter', system-ui, -apple-system, sans-serif;
    color: var(--color-text);
    background: var(--color-bg);
    min-height: 100vh;
  }
  .topbar {
    display: flex;
    align-items: center;
    gap: 1.5rem;
    padding: 0.75rem 1.5rem;
    background: var(--color-surface);
    border-bottom: 1px solid var(--color-border);
  }
  .brand {
    font-weight: 600;
    color: var(--color-text);
    text-decoration: none;
  }
  .primary {
    display: flex;
    gap: 1rem;
  }
  .primary a {
    color: var(--color-muted);
    text-decoration: none;
    font-size: 0.9rem;
  }
  .primary a:hover {
    color: var(--color-accent);
  }
  .page {
    max-width: 1080px;
    margin: 0 auto;
    padding: 1.5rem;
  }
  .breadcrumbs {
    font-size: 0.85rem;
    color: var(--color-muted);
    margin-bottom: 0.5rem;
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .breadcrumbs a {
    color: var(--color-accent);
    text-decoration: none;
  }
  .breadcrumbs a:hover {
    text-decoration: underline;
  }
  .breadcrumbs .sep {
    opacity: 0.5;
  }
  .page-title {
    margin: 0 0 1rem 0;
    font-size: 1.6rem;
    font-weight: 600;
  }
</style>
