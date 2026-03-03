<script lang="ts">
  interface Component {
    name: string;
    slug: string;
    purpose: string;
    category: string;
    type: 'core' | 'alacarte';
  }

  export let components: Component[] = [];
  export let categoryLabels: Record<string, string> = {};

  let activeFilter = 'all';
  let searchQuery = '';

  $: categories = [...new Set(components.map(c => c.category))];

  $: filtered = components.filter(c => {
    const matchesType = activeFilter === 'all' || activeFilter === c.type ||
      (activeFilter !== 'core' && activeFilter !== 'alacarte' && activeFilter === c.category);
    const matchesSearch = !searchQuery ||
      c.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      c.purpose.toLowerCase().includes(searchQuery.toLowerCase());
    return matchesType && matchesSearch;
  });

  $: coreCount = components.filter(c => c.type === 'core').length;
  $: alacarteCount = components.filter(c => c.type === 'alacarte').length;

  function setFilter(f: string) {
    activeFilter = f;
  }
</script>

<div class="space-y-6">
  <!-- Search -->
  <div class="flex flex-col gap-4 sm:flex-row sm:items-center">
    <input
      type="text"
      bind:value={searchQuery}
      placeholder="Search components..."
      class="w-full rounded-lg border border-[var(--color-border-primary)] bg-[var(--color-bg-secondary)] px-4 py-2.5 text-sm text-[var(--color-text-primary)] placeholder-[var(--color-text-tertiary)] transition-colors duration-200 focus:border-[var(--color-accent)] focus:outline-none sm:max-w-xs"
    />
    <span class="text-sm text-[var(--color-text-tertiary)]">
      {filtered.length} component{filtered.length !== 1 ? 's' : ''}
    </span>
  </div>

  <!-- Filter tabs -->
  <div class="flex flex-wrap gap-2">
    <button
      on:click={() => setFilter('all')}
      class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors duration-200"
      class:active-filter={activeFilter === 'all'}
      class:inactive-filter={activeFilter !== 'all'}
    >
      All ({components.length})
    </button>
    <button
      on:click={() => setFilter('core')}
      class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors duration-200"
      class:active-filter={activeFilter === 'core'}
      class:inactive-filter={activeFilter !== 'core'}
    >
      Core ({coreCount})
    </button>
    <button
      on:click={() => setFilter('alacarte')}
      class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors duration-200"
      class:active-filter={activeFilter === 'alacarte'}
      class:inactive-filter={activeFilter !== 'alacarte'}
    >
      A la carte ({alacarteCount})
    </button>

    <span class="mx-1 hidden h-6 w-px bg-[var(--color-border-primary)] sm:block"></span>

    {#each categories as cat}
      <button
        on:click={() => setFilter(cat)}
        class="rounded-lg px-3 py-1.5 text-xs font-medium transition-colors duration-200"
        class:active-filter={activeFilter === cat}
        class:inactive-filter={activeFilter !== cat}
      >
        {categoryLabels[cat] || cat}
      </button>
    {/each}
  </div>

  <!-- Grid -->
  <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
    {#each filtered as comp (comp.slug)}
      <div class="rounded-lg border border-[var(--color-border-primary)] bg-[var(--color-bg-secondary)] p-4 transition-all duration-200 hover:border-[var(--color-accent)]/30">
        <div class="flex items-center gap-2">
          <span
            class="h-2 w-2 rounded-full"
            style="background: {comp.type === 'core' ? 'var(--color-accent)' : 'var(--color-text-tertiary)'}"
          ></span>
          <span class="text-sm font-medium text-[var(--color-text-primary)]">{comp.name}</span>
        </div>
        <p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{comp.purpose}</p>
        <span class="mt-2 inline-block rounded bg-[var(--color-bg-elevated)] px-1.5 py-0.5 text-[10px] uppercase text-[var(--color-text-tertiary)]">
          {categoryLabels[comp.category] || comp.category}
        </span>
      </div>
    {/each}
  </div>

  {#if filtered.length === 0}
    <p class="py-12 text-center text-sm text-[var(--color-text-tertiary)]">
      No components match your search.
    </p>
  {/if}
</div>

<style>
  .active-filter {
    background: var(--color-accent);
    color: var(--color-bg-primary);
  }
  .inactive-filter {
    background: var(--color-bg-elevated);
    color: var(--color-text-secondary);
  }
  .inactive-filter:hover {
    color: var(--color-text-primary);
  }
</style>
