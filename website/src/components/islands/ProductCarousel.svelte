<script lang="ts">
  interface Product {
    name: string;
    fullName: string;
    description: string;
    color: string;
    icon: string;
    components: string[];
  }

  export let products: Product[] = [];

  let container: HTMLElement;
  let isDragging = false;
  let startX = 0;
  let scrollStart = 0;

  function handleMouseDown(e: MouseEvent) {
    isDragging = true;
    startX = e.pageX;
    scrollStart = container.scrollLeft;
    container.style.cursor = 'grabbing';
    container.style.userSelect = 'none';
  }

  function handleMouseMove(e: MouseEvent) {
    if (!isDragging) return;
    const dx = e.pageX - startX;
    container.scrollLeft = scrollStart - dx;
  }

  function handleMouseUp() {
    isDragging = false;
    if (container) {
      container.style.cursor = 'grab';
      container.style.userSelect = '';
    }
  }

  function scrollBy(dir: number) {
    container?.scrollBy({ left: dir * 320, behavior: 'smooth' });
  }
</script>

<svelte:window on:mouseup={handleMouseUp} on:mousemove={handleMouseMove} />

<div class="relative">
  <!-- Scroll buttons -->
  <button
    on:click={() => scrollBy(-1)}
    class="absolute -left-4 top-1/2 z-10 hidden -translate-y-1/2 rounded-full border border-[var(--color-border-primary)] bg-[var(--color-bg-secondary)] p-2 text-[var(--color-text-secondary)] transition-colors hover:text-[var(--color-text-primary)] md:block"
    aria-label="Scroll left"
  >
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
      <path d="M10 4L6 8l4 4"/>
    </svg>
  </button>

  <button
    on:click={() => scrollBy(1)}
    class="absolute -right-4 top-1/2 z-10 hidden -translate-y-1/2 rounded-full border border-[var(--color-border-primary)] bg-[var(--color-bg-secondary)] p-2 text-[var(--color-text-secondary)] transition-colors hover:text-[var(--color-text-primary)] md:block"
    aria-label="Scroll right"
  >
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
      <path d="M6 4l4 4-4 4"/>
    </svg>
  </button>

  <!-- Cards -->
  <div
    bind:this={container}
    on:mousedown={handleMouseDown}
    class="flex gap-4 overflow-x-auto pb-4 scroll-smooth"
    style="cursor: grab; -webkit-overflow-scrolling: touch; scrollbar-width: none;"
    role="list"
  >
    {#each products as product}
      <div
        class="min-w-[280px] max-w-[320px] flex-shrink-0 rounded-xl border border-[var(--color-border-primary)] bg-[var(--color-bg-secondary)] p-6 transition-all duration-200 hover:border-[var(--color-border-hover)]"
        role="listitem"
      >
        <div class="mb-4 flex items-center gap-3">
          <span
            class="flex h-10 w-10 items-center justify-center rounded-lg text-lg"
            style="background: {product.color}15; color: {product.color}"
          >
            {product.icon}
          </span>
          <div>
            <h3 class="font-semibold text-[var(--color-text-primary)]">{product.name}</h3>
            <p class="text-xs text-[var(--color-text-tertiary)]">{product.fullName}</p>
          </div>
        </div>
        <p class="text-sm leading-relaxed text-[var(--color-text-secondary)]">{product.description}</p>
        {#if product.components.length > 0}
          <div class="mt-4 flex flex-wrap gap-1.5">
            {#each product.components.slice(0, 5) as comp}
              <span class="rounded-md bg-[var(--color-bg-elevated)] px-2 py-0.5 text-xs text-[var(--color-text-tertiary)]">{comp}</span>
            {/each}
            {#if product.components.length > 5}
              <span class="rounded-md bg-[var(--color-bg-elevated)] px-2 py-0.5 text-xs text-[var(--color-text-tertiary)]">+{product.components.length - 5}</span>
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  div::-webkit-scrollbar {
    display: none;
  }
</style>
