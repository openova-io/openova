<!--
  G117.3 — Endpoints tab (standalone route).

  Same component as the tabbed view inside /apps/{id}, just at its own
  URL for deep-linking from the catalog drill-down.
-->
<script lang="ts">
  import EndpointsTab from '../../../../components/EndpointsTab.svelte';

  let { appId = '' }: { appId?: string } = $props();

  $effect(() => {
    if (typeof window !== 'undefined') {
      const m = window.location.pathname.match(/\/apps\/([^/]+)\/endpoints/);
      if (m) appId = m[1];
    }
  });
</script>

<section class="g117-endpoints-route" data-testid="endpoints-route" data-app-id={appId}>
  <p class="back">
    <a href={`/apps/${appId}`}>← back to Application</a>
  </p>
  <EndpointsTab {appId} />
</section>

<style>
  .g117-endpoints-route {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .back a {
    color: #1d4ed8;
    text-decoration: none;
    font-size: 0.85rem;
  }
  .back a:hover {
    text-decoration: underline;
  }
</style>
