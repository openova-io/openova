<script lang="ts">
  // #3860 / UAT row 86 — live provisioning-stage timeline.
  //
  // Renders the ordered stages the provisioning service emits for a funnel
  // Org (core/services/provisioning/handlers/consumer.go): Creating tenant →
  // Committing manifests to Git → Provisioning vCluster → Installing <dep>
  // (dependency) → Deploying <app> → Configuring TLS certificates → Running
  // health checks. Stage NAMES are rendered verbatim from the backend
  // payload — this component invents none, so dependency/app rows scale with
  // the real order and the labels always match what the workflow actually
  // did.
  //
  // HONEST STATES ONLY. Each stage renders its real backend status:
  //   completed → green check      running → accent spinner
  //   failed    → red cross + why  pending → neutral ring
  // Nothing shows green until the backend reports `completed`, and a failed
  // stage renders red and names itself (never a premature success, never an
  // indefinite spinner on a dead provision).
  import type { ProvisionStep } from '../lib/api';

  let { steps = [], status = '' }: { steps?: ProvisionStep[]; status?: string } = $props();
</script>

<ol class="flex flex-col gap-3" data-testid="provisioning-timeline" aria-label="Provisioning progress">
  {#each steps as step}
    <li class="flex items-start gap-3" data-testid="provisioning-stage" data-status={step.status}>
      {#if step.status === 'completed'}
        <div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-success)]">
          <svg class="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
          </svg>
        </div>
      {:else if step.status === 'failed'}
        <div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-danger)]">
          <svg class="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </div>
      {:else if step.status === 'running'}
        <div class="mt-0.5 h-6 w-6 shrink-0 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" aria-hidden="true"></div>
      {:else}
        <div class="mt-0.5 h-6 w-6 shrink-0 rounded-full border border-[var(--color-border)]" aria-hidden="true"></div>
      {/if}
      <div class="min-w-0">
        <span
          class="block text-sm {step.status === 'failed'
            ? 'font-medium text-[var(--color-danger)]'
            : step.status === 'running'
              ? 'font-medium text-[var(--color-accent)]'
              : step.status === 'completed'
                ? 'text-[var(--color-text)]'
                : 'text-[var(--color-text-dimmer)]'}"
        >{step.name}</span>
        {#if step.status === 'failed' && step.message && step.message !== 'ok'}
          <span class="mt-0.5 block break-words text-xs text-[var(--color-danger)]">{step.message}</span>
        {/if}
      </div>
    </li>
  {/each}
</ol>
