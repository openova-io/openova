<script lang="ts">
  import { onMount } from 'svelte';

  export let target: number = 0;
  export let prefix: string = '';
  export let suffix: string = '';
  export let duration: number = 1500;

  let display = '0';
  let el: HTMLElement;

  function formatNumber(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace('.0', '') + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K';
    return n.toFixed(0);
  }

  function animate() {
    const start = performance.now();
    const prefersReduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    if (prefersReduced) {
      display = formatNumber(target);
      return;
    }

    function step(now: number) {
      const elapsed = now - start;
      const progress = Math.min(elapsed / duration, 1);
      // ease-out cubic
      const eased = 1 - Math.pow(1 - progress, 3);
      const current = Math.floor(eased * target);
      display = formatNumber(current);

      if (progress < 1) {
        requestAnimationFrame(step);
      } else {
        display = formatNumber(target);
      }
    }

    requestAnimationFrame(step);
  }

  onMount(() => {
    const observer = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            animate();
            observer.disconnect();
          }
        });
      },
      { threshold: 0.3 }
    );

    if (el) observer.observe(el);

    return () => observer.disconnect();
  });
</script>

<span bind:this={el}>{prefix}{display}{suffix}</span>
