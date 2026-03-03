<script lang="ts">
  import { onMount, onDestroy } from 'svelte';

  export let slideCount = 5;

  let current = 0;
  let timer;
  let container;
  let touchStartX = 0;
  let touchStartY = 0;
  let swiping = false;
  let animating = false;

  const INTERVAL = 5000;
  const TRANSITION_MS = 400;

  const backgrounds = [
    '#0E1A3D',    // Slide 0: Navy blue (Concepts)
    '#1C1048',    // Slide 1: Rich purple (Tech names)
    '#0A2D16',    // Slide 2: Forest green (Engagement)
    '#2D0A0A',    // Slide 3: Deep crimson (Exodus)
    '#0A2528',    // Slide 4: Deep teal (Identity)
  ];

  function updateSlides() {
    if (!container) return;
    const slides = container.querySelectorAll('[data-carousel-slide]');
    slides.forEach((slide, i) => {
      if (i === current) {
        slide.classList.add('carousel-slide--active');
        slide.removeAttribute('aria-hidden');
      } else {
        slide.classList.remove('carousel-slide--active');
        slide.setAttribute('aria-hidden', 'true');
      }
    });
    container.style.backgroundColor = backgrounds[current] || backgrounds[0];
  }

  function goTo(idx) {
    if (animating || idx === current) return;
    animating = true;
    current = idx;
    updateSlides();
    setTimeout(() => { animating = false; }, TRANSITION_MS);
  }

  function next() { goTo((current + 1) % slideCount); }
  function prev() { goTo((current - 1 + slideCount) % slideCount); }

  function startTimer() { stopTimer(); timer = setInterval(next, INTERVAL); }
  function stopTimer() { if (timer) clearInterval(timer); }

  function handleMouseEnter() { stopTimer(); }
  function handleMouseLeave() { startTimer(); }

  function handleKeydown(e) {
    if (e.key === 'ArrowLeft') { stopTimer(); prev(); startTimer(); }
    if (e.key === 'ArrowRight') { stopTimer(); next(); startTimer(); }
  }

  function handleTouchStart(e) {
    touchStartX = e.touches[0].clientX;
    touchStartY = e.touches[0].clientY;
    swiping = true;
    stopTimer();
  }

  function handleTouchMove(e) {
    if (!swiping) return;
    const dx = e.touches[0].clientX - touchStartX;
    const dy = e.touches[0].clientY - touchStartY;
    if (Math.abs(dy) > Math.abs(dx)) { swiping = false; return; }
    if (Math.abs(dx) > 10) e.preventDefault();
  }

  function handleTouchEnd(e) {
    if (!swiping) { startTimer(); return; }
    swiping = false;
    const dx = e.changedTouches[0].clientX - touchStartX;
    if (Math.abs(dx) > 40) {
      if (dx < 0) next();
      else prev();
    }
    startTimer();
  }

  onMount(() => {
    updateSlides();
    startTimer();
  });

  onDestroy(() => { stopTimer(); });
</script>

<svelte:window on:keydown={handleKeydown} />

<div
  bind:this={container}
  class="carousel"
  style="background-color: {backgrounds[current]};"
  on:mouseenter={handleMouseEnter}
  on:mouseleave={handleMouseLeave}
  on:touchstart={handleTouchStart}
  on:touchmove|nonpassive={handleTouchMove}
  on:touchend={handleTouchEnd}
  role="region"
  aria-label="Homepage carousel"
  aria-roledescription="carousel"
>
  <div class="carousel__slides">
    <slot />
  </div>

  <!-- Arrows -->
  <button class="carousel__arrow carousel__arrow--prev" on:click={() => { stopTimer(); prev(); startTimer(); }} aria-label="Previous slide">
    <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 18l-6-6 6-6"/></svg>
  </button>
  <button class="carousel__arrow carousel__arrow--next" on:click={() => { stopTimer(); next(); startTimer(); }} aria-label="Next slide">
    <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18l6-6-6-6"/></svg>
  </button>

  <!-- Dots -->
  <div class="carousel__dots" role="tablist" aria-label="Slide navigation">
    {#each [0, 1, 2, 3, 4] as i}
      <button
        class="carousel__dot"
        class:carousel__dot--active={i === current}
        on:click={() => { stopTimer(); goTo(i); startTimer(); }}
        role="tab"
        aria-selected={i === current}
        aria-label={`Go to slide ${i + 1}`}
      ></button>
    {/each}
  </div>
</div>

<style>
  .carousel {
    position: relative;
    min-height: calc(100vh - 6rem);
    overflow: hidden;
    transition: background-color 0.5s ease;
    display: flex;
    flex-direction: column;
    margin: 1rem 1.5rem;
    border-radius: 1.25rem;
  }

  .carousel__slides {
    flex: 1;
    position: relative;
  }

  /* Arrows */
  .carousel__arrow {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    z-index: 10;
    width: 44px;
    height: 44px;
    border-radius: 50%;
    border: 1px solid rgba(255, 255, 255, 0.15);
    background: rgba(0, 0, 0, 0.3);
    color: rgba(255, 255, 255, 0.7);
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: all 0.2s;
    backdrop-filter: blur(8px);
  }
  .carousel__arrow:hover {
    background: rgba(0, 0, 0, 0.5);
    color: #fff;
    border-color: rgba(255, 255, 255, 0.3);
  }
  .carousel__arrow--prev { left: 1rem; }
  .carousel__arrow--next { right: 1rem; }

  /* Dots */
  .carousel__dots {
    display: flex;
    justify-content: center;
    gap: 0.5rem;
    padding: 1.5rem 1.5rem 2rem;
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    z-index: 10;
  }
  .carousel__dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    border: 1.5px solid rgba(255, 255, 255, 0.3);
    background: transparent;
    cursor: pointer;
    padding: 0;
    transition: all 0.3s;
  }
  .carousel__dot--active {
    background: #10B981;
    border-color: #10B981;
    transform: scale(1.2);
  }
  .carousel__dot:hover:not(.carousel__dot--active) {
    border-color: rgba(255, 255, 255, 0.6);
    background: rgba(255, 255, 255, 0.15);
  }

  /* Mobile */
  @media (max-width: 767px) {
    .carousel {
      min-height: auto;
      margin: 0.5rem;
      border-radius: 0.75rem;
    }
    .carousel__arrow { display: none; }
    .carousel__dots { position: relative; padding: 1rem; }
  }
</style>
