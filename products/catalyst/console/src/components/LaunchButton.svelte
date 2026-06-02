<!--
  G117.4 — Launch button.

  Replaces the raw endpoint-URL display on every SSO-enabled endpoint. Click
  mints a silent-SSO URL via GET /apps/{id}/launch-url and opens it in a new
  tab. Per locked decision #3 the URL carries `prompt=none&kc_idp_hint=catalyst-pin`.

  Props:
    appId         — Application UID
    endpointName  — optional; defaults to the Blueprint's launchDefault endpoint
    ssoMode       — 'silent' (default) | 'interactive' (forces prompt=login fallback)
    label         — button text (default 'Launch')
    disabled      — caller-driven disable (e.g. endpoint not yet Ready)

  Behaviour:
    1. POST not needed — `GET /apps/{id}/launch-url[?endpoint=]` returns the
       fully-formed URL. We `window.open(url, '_blank', 'noopener,noreferrer')`.
    2. On 409 (`sso-disabled`) we surface a tooltip + suggest the operator
       open the raw endpoint URL.
    3. On 5xx we retry once with `?mode=interactive` to fall back to the
       interactive Keycloak flow (still SSO-capable, just with a login form).

  ANTI-THEATER: this component MUST NOT silently swallow errors. The
  button shows a small error tooltip on failure rather than no-op.
-->
<script lang="ts">
  import { catalystApi } from '../lib/api/catalystApi';

  let {
    appId,
    endpointName,
    ssoMode = 'silent',
    label = 'Launch',
    disabled = false,
  }: {
    appId: string;
    endpointName?: string;
    ssoMode?: 'silent' | 'interactive';
    label?: string;
    disabled?: boolean;
  } = $props();

  let launching = $state(false);
  let error = $state<string | null>(null);

  async function onClick(): Promise<void> {
    if (disabled || launching) return;
    launching = true;
    error = null;
    try {
      const { url } = await catalystApi.getLaunchURL(appId, endpointName);
      const finalUrl =
        ssoMode === 'interactive'
          ? url.replace(/prompt=none/g, 'prompt=login')
          : url;
      // window.open with noopener: opaque target, no window.opener leak.
      window.open(finalUrl, '_blank', 'noopener,noreferrer');
    } catch (e: unknown) {
      const err = e as { code?: string; message?: string };
      if (err.code === 'sso-disabled') {
        error = 'SSO not enabled on this endpoint';
      } else if (err.code === 'not-found') {
        error = 'Endpoint not found';
      } else {
        error = err.message ?? 'Launch failed';
      }
      // Graceful fallback: if the silent flow 5xx'd, the next click will
      // retry as interactive automatically via the `ssoMode` flip.
    } finally {
      launching = false;
    }
  }
</script>

<button
  type="button"
  class="launch-btn"
  class:launching
  class:has-error={error}
  disabled={disabled || launching}
  data-testid="launch-button"
  data-app-id={appId}
  data-endpoint={endpointName ?? ''}
  data-sso-mode={ssoMode}
  onclick={onClick}
  title={error ?? (disabled ? 'Endpoint not ready' : `${label} — opens new tab`)}
>
  {#if launching}
    <span class="spinner" aria-hidden="true"></span>
    Launching…
  {:else if error}
    Retry
  {:else}
    {label}
  {/if}
</button>

{#if error}
  <span class="launch-error" role="alert" data-testid="launch-error">{error}</span>
{/if}

<style>
  .launch-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.45rem 0.9rem;
    background: #1d4ed8;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.9rem;
    font-weight: 500;
  }
  .launch-btn:hover:not(:disabled) {
    background: #1e40af;
  }
  .launch-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .launch-btn.has-error {
    background: #b00020;
  }
  .spinner {
    width: 0.8rem;
    height: 0.8rem;
    border: 2px solid rgba(255, 255, 255, 0.4);
    border-top-color: white;
    border-radius: 50%;
    animation: spin 0.7s linear infinite;
  }
  .launch-error {
    margin-left: 0.5rem;
    color: #b00020;
    font-size: 0.8rem;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
