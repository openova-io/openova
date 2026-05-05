<script lang="ts">
  // PinInput6 — Stripe-style 6-digit PIN input, Svelte 5 port of the
  // canonical React component at
  // products/catalyst/bootstrap/ui/src/components/PinInput6.tsx (#1010).
  //
  // Architecture (founder rule "paste must just work" 2026-05-04):
  //   - ONE real <input maxlength={6}> captures all keystrokes + paste.
  //   - 6 visible boxes mirror the digits the input is holding. Boxes
  //     are decorative; clicking any focuses the hidden input.
  //   - Input is positioned over the box row with transparent text, so
  //     paste / autofill / one-time-code suggestion all flow into a
  //     single canonical element. No per-box paste fan-out, no race.
  //
  // Per docs/INVIOLABLE-PRINCIPLES.md #10: presentational only — parent
  // owns the fetch and clears the value after submit.

  interface Props {
    disabled?: boolean;
    autoFocus?: boolean;
    value?: string;
    testId?: string;
    onChange?: (pin: string) => void;
    onComplete?: (pin: string) => void;
    /** Re-mount key from parent flips → reset internal state. */
    resetKey?: number;
  }

  let {
    disabled = false,
    autoFocus = true,
    value = '',
    testId = 'pin-box',
    onChange,
    onComplete,
    resetKey = 0,
  }: Props = $props();

  const N = 6;
  let inputEl: HTMLInputElement | null = $state(null);

  function extractDigits(s: string): string {
    return (s.match(/\d/g) ?? []).join('');
  }

  let pin = $state<string>(extractDigits(value).slice(0, N));

  // Reset when parent flips resetKey (mirrors React's `key` re-mount idiom).
  let lastResetKey = resetKey;
  $effect(() => {
    if (resetKey !== lastResetKey) {
      lastResetKey = resetKey;
      pin = '';
    }
  });

  // Notify parent on change + completion.
  $effect(() => {
    onChange?.(pin);
    if (pin.length === N) onComplete?.(pin);
  });

  // Auto-focus on mount AND when the user tab-backs to the page (so they
  // can paste IMMEDIATELY after returning from their email client without
  // having to click). Founder rule 2026-05-04.
  $effect(() => {
    if (!autoFocus || disabled) return;
    const el = inputEl;
    if (!el) return;
    el.focus();
    const onVisible = () => {
      if (document.visibilityState === 'visible' && !disabled && !el.disabled) {
        el.focus();
      }
    };
    const onWindowFocus = () => {
      if (!disabled && !el.disabled) el.focus();
    };
    document.addEventListener('visibilitychange', onVisible);
    window.addEventListener('focus', onWindowFocus);
    return () => {
      document.removeEventListener('visibilitychange', onVisible);
      window.removeEventListener('focus', onWindowFocus);
    };
  });

  function handleInput(e: Event) {
    const el = e.target as HTMLInputElement;
    pin = extractDigits(el.value).slice(0, N);
  }

  function focusInput() {
    const el = inputEl;
    if (!el) return;
    el.focus();
    const len = el.value.length;
    el.setSelectionRange(len, len);
  }
</script>

<div
  role="group"
  aria-label="Sign-in code"
  class="relative flex items-center justify-center gap-2 sm:gap-2.5"
  onclick={focusInput}
  data-testid={testId}
>
  {#each Array.from({ length: N }) as _, i}
    {@const digit = pin[i] ?? ''}
    {@const filled = digit !== ''}
    {@const isActive = !disabled && pin.length === i}
    <div
      data-testid={`${testId}-${i}`}
      aria-label={`Digit ${i + 1}: ${digit || 'empty'}`}
      class={[
        'flex items-center justify-center select-none',
        'w-12 h-14 sm:w-14 sm:h-16',
        'text-center text-2xl sm:text-3xl font-semibold tabular-nums tracking-tight',
        'rounded-xl border-[1.5px] bg-[var(--color-bg)] text-[var(--color-text)]',
        'shadow-[0_1px_0_rgba(255,255,255,0.04),_inset_0_-1px_0_rgba(255,255,255,0.02)]',
        filled
          ? 'border-[var(--color-accent)]/70'
          : isActive
            ? 'border-[var(--color-accent)] ring-[3px] ring-[var(--color-accent)]/30'
            : 'border-[var(--color-border)]',
        'transition-[border-color,box-shadow,transform] duration-150 ease-out',
        disabled ? 'opacity-50' : '',
      ].join(' ')}
    >
      {digit}
    </div>
  {/each}

  <!--
    Single real input. Captures every keystroke + paste. Visually hidden
    via opacity:0 + transparent caret. Positioned absolute over the row
    so clicks on boxes hit it via pointer-events. autoComplete="one-time-code"
    targets iOS SMS autofill into the focused input.
  -->
  <input
    bind:this={inputEl}
    type="text"
    inputmode="numeric"
    pattern="[0-9]*"
    autocomplete="one-time-code"
    maxlength={N}
    value={pin}
    {disabled}
    oninput={handleInput}
    aria-label="Sign-in code (6 digits)"
    data-testid={`${testId}-input`}
    class="absolute inset-0 w-full h-full opacity-0 cursor-text"
    style="caret-color: transparent; color: transparent; background: transparent; border: 0; outline: 0; padding: 0; font-size: inherit; letter-spacing: inherit;"
  />
</div>
