<script lang="ts">
  let name = '';
  let email = '';
  let company = '';
  let interest = '';
  let message = '';
  let submitted = false;
  let submitting = false;
  let errorMsg = '';

  interface Errors {
    name?: string;
    email?: string;
  }

  let errors: Errors = {};

  const CONTACT_EMAIL = 'sales@openova.io';

  function validate(): boolean {
    errors = {};
    if (!name.trim()) errors.name = 'Name is required';
    if (!email.trim()) {
      errors.email = 'Email is required';
    } else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      errors.email = 'Enter a valid email';
    }
    return Object.keys(errors).length === 0;
  }

  async function handleSubmit() {
    if (!validate()) return;
    submitting = true;
    errorMsg = '';

    try {
      const response = await fetch(`https://formsubmit.co/ajax/${CONTACT_EMAIL}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
        body: JSON.stringify({
          name,
          email,
          company: company || 'N/A',
          interest: interest || 'General',
          message,
          _subject: `OpenOva inquiry: ${interest || 'General'}`,
        }),
      });

      const data = await response.json();
      if (data.success) {
        submitted = true;
      } else {
        errorMsg = 'Something went wrong. Please email us directly.';
      }
    } catch {
      errorMsg = 'Something went wrong. Please email us directly.';
    } finally {
      submitting = false;
    }
  }
</script>

{#if submitted}
  <div class="rounded-xl border border-[var(--color-accent)]/30 bg-[var(--color-bg-secondary)] p-8 text-center">
    <div class="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-accent)]/10">
      <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M20 6L9 17l-5-5"/>
      </svg>
    </div>
    <h3 class="text-lg font-semibold text-[var(--color-text-primary)]">
      Message sent
    </h3>
    <p class="mt-2 text-sm text-[var(--color-text-secondary)]">
      We'll get back to you within one business day.
    </p>
  </div>
{:else}
  <form on:submit|preventDefault={handleSubmit} class="space-y-6" novalidate>
    <div>
      <label for="cf-name" class="mb-2 block text-sm font-medium text-[var(--color-text-primary)]">Name</label>
      <input
        type="text"
        id="cf-name"
        bind:value={name}
        class="field"
        class:field-error={errors.name}
        placeholder="Your name"
      />
      {#if errors.name}
        <p class="mt-1 text-xs text-red-400">{errors.name}</p>
      {/if}
    </div>

    <div>
      <label for="cf-email" class="mb-2 block text-sm font-medium text-[var(--color-text-primary)]">Email</label>
      <input
        type="email"
        id="cf-email"
        bind:value={email}
        class="field"
        class:field-error={errors.email}
        placeholder="you@company.com"
      />
      {#if errors.email}
        <p class="mt-1 text-xs text-red-400">{errors.email}</p>
      {/if}
    </div>

    <div>
      <label for="cf-company" class="mb-2 block text-sm font-medium text-[var(--color-text-primary)]">Company</label>
      <input
        type="text"
        id="cf-company"
        bind:value={company}
        class="field"
        placeholder="Your company"
      />
    </div>

    <div>
      <label for="cf-interest" class="mb-2 block text-sm font-medium text-[var(--color-text-primary)]">Interest</label>
      <select id="cf-interest" bind:value={interest} class="field">
        <option value="">Select an area</option>
        <option value="platform">Platform deployment</option>
        <option value="migration">Migration (Exodus)</option>
        <option value="consultancy">Consultancy</option>
        <option value="cortex">AI Hub (Cortex)</option>
        <option value="fingate">Open Banking (Fingate)</option>
        <option value="pricing">Engagement discussion</option>
        <option value="other">Other</option>
      </select>
    </div>

    <div>
      <label for="cf-message" class="mb-2 block text-sm font-medium text-[var(--color-text-primary)]">Message</label>
      <textarea
        id="cf-message"
        bind:value={message}
        rows="4"
        class="field resize-none"
        placeholder="Tell us about your needs..."
      ></textarea>
    </div>

    {#if errorMsg}
      <p class="text-sm text-red-400 text-center">{errorMsg} <a href="mailto:sales@openova.io" class="underline">sales@openova.io</a></p>
    {/if}

    <button
      type="submit"
      disabled={submitting}
      class="w-full rounded-lg bg-[var(--color-accent)] px-6 py-3 font-medium text-[var(--color-bg-primary)] transition-colors duration-200 hover:bg-[var(--color-accent-hover)] disabled:opacity-60"
    >
      {submitting ? 'Sending...' : 'Send message'}
    </button>

    <p class="text-xs text-[var(--color-text-tertiary)] text-center">
      Or email us directly at <a href="mailto:sales@openova.io" class="underline hover:text-[var(--color-text-secondary)]">sales@openova.io</a>
    </p>
  </form>
{/if}

<style>
  .field {
    width: 100%;
    border-radius: 0.5rem;
    border: 1px solid var(--color-border-primary);
    background: var(--color-bg-secondary);
    padding: 0.75rem 1rem;
    color: var(--color-text-primary);
    transition: border-color 0.2s;
    font-size: 0.875rem;
  }
  .field::placeholder {
    color: var(--color-text-tertiary);
  }
  .field:focus {
    border-color: var(--color-accent);
    outline: none;
  }
  .field-error {
    border-color: #f87171;
  }
</style>
