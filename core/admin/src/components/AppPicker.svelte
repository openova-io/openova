<script lang="ts">
  import type { App } from '../lib/api';

  interface Props {
    apps: App[];
    selected: string[];
    onchange: (selected: string[]) => void;
    placeholder?: string;
    excludeSlug?: string;
  }

  let { apps, selected, onchange, placeholder = 'Search apps...', excludeSlug = '' }: Props = $props();

  let query = $state('');
  let open = $state(false);
  let container: HTMLDivElement | undefined = $state();

  const selectedApps = $derived(
    selected.map(slug => apps.find(a => a.slug === slug)).filter((a): a is App => !!a)
  );

  const availableApps = $derived(
    apps
      .filter(a => !selected.includes(a.slug) && a.slug !== excludeSlug)
      .filter(a => {
        if (!query) return true;
        const q = query.toLowerCase();
        return a.name.toLowerCase().includes(q) || a.slug.toLowerCase().includes(q);
      })
      .slice(0, 20)
  );

  function add(slug: string) {
    onchange([...selected, slug]);
    query = '';
    open = false;
  }

  function remove(slug: string) {
    onchange(selected.filter(s => s !== slug));
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && availableApps.length > 0) {
      e.preventDefault();
      add(availableApps[0].slug);
    } else if (e.key === 'Escape') {
      open = false;
    } else if (e.key === 'Backspace' && !query && selectedApps.length > 0) {
      remove(selectedApps[selectedApps.length - 1].slug);
    }
  }

  function handleBlur(e: FocusEvent) {
    const next = e.relatedTarget as Node | null;
    if (!container || !next || !container.contains(next)) {
      setTimeout(() => { open = false; }, 150);
    }
  }
</script>

<div bind:this={container} class="app-picker relative">
  {#if selectedApps.length > 0}
    <div class="mb-2 flex flex-wrap gap-1.5">
      {#each selectedApps as app}
        <span class="inline-flex items-center gap-1.5 rounded-full bg-[var(--color-accent)]/15 px-2.5 py-1 text-xs font-medium text-[var(--color-accent)]">
          {#if app.icon}<span style="color: {app.color}">{app.icon}</span>{/if}
          <span>{app.name}</span>
          <button
            type="button"
            onclick={() => remove(app.slug)}
            class="-mr-0.5 rounded-full hover:bg-[var(--color-accent)]/20"
            aria-label="Remove {app.name}"
          >
            <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </span>
      {/each}
    </div>
  {/if}

  <input
    type="text"
    bind:value={query}
    onfocus={() => { open = true; }}
    onblur={handleBlur}
    onkeydown={handleKeydown}
    {placeholder}
    class="w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
  />

  {#if open && availableApps.length > 0}
    <div class="absolute left-0 right-0 top-full z-20 mt-1 max-h-56 overflow-y-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] shadow-lg">
      {#each availableApps as app}
        <button
          type="button"
          onclick={() => add(app.slug)}
          class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)]"
        >
          {#if app.icon}
            <span class="flex h-6 w-6 items-center justify-center rounded text-xs" style="background: {app.color}20; color: {app.color};">{app.icon}</span>
          {/if}
          <span class="flex-1 truncate">{app.name}</span>
          <span class="text-[10px] text-[var(--color-text-dimmer)] font-mono">{app.slug}</span>
          {#if app.system}
            <span class="rounded bg-[var(--color-text-dim)]/15 px-1.5 py-0.5 text-[9px] font-medium uppercase text-[var(--color-text-dim)]">sys</span>
          {/if}
        </button>
      {/each}
    </div>
  {:else if open && query && availableApps.length === 0}
    <div class="absolute left-0 right-0 top-full z-20 mt-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-xs text-[var(--color-text-dim)]">
      No matches for "{query}"
    </div>
  {/if}
</div>
