<script lang="ts">
  import AdminShell from './AdminShell.svelte';
  import AppPicker from './AppPicker.svelte';
  import {
    getApps, getPlans, getIndustries, getAddons,
    createApp, updateApp, deleteApp,
    createPlan, updatePlan, deletePlan,
    createIndustry, updateIndustry, deleteIndustry,
    createAddon, updateAddon, deleteAddon,
    type User, type App, type Plan, type Industry, type AddOn,
  } from '../lib/api';

  type Tab = 'apps' | 'plans' | 'industries' | 'addons';
  let activeTab = $state<Tab>('apps');

  let apps = $state<App[]>([]);
  let plans = $state<Plan[]>([]);
  let industries = $state<Industry[]>([]);
  let addons = $state<AddOn[]>([]);
  let loading = $state(true);
  let error = $state('');

  // Generic form state
  let showForm = $state(false);
  let formType = $state<Tab>('apps');
  let editingId = $state<string | null>(null);
  let saving = $state(false);

  // App form
  let appForm = $state({ name: '', slug: '', tagline: '', description: '', category: '', icon: '', color: '#3b82f6', free: true, dependencies: [] as string[], system: false });
  // Editing slug kept separately so AppPicker can exclude self-reference when editing.
  let editingSlug = $state('');
  // Plan form
  let planForm = $state({ name: '', slug: '', description: '', cpu: '', memory: '', storage: '', price_omr: 0, popular: false, sort_order: 0, features: [] as string[], stripe_price_id: '' });
  let newFeature = $state('');
  // Industry form
  let indForm = $state({ name: '', slug: '', emoji: '', description: '', selected_apps: [] as string[] });
  // Addon form
  let addonForm = $state({ name: '', slug: '', description: '', price_omr: 0, included: false, category: '' });

  $effect(() => {
    Promise.all([getApps(), getPlans(), getIndustries(), getAddons()])
      .then(([a, p, i, ad]) => {
        apps = a; plans = p; industries = i; addons = ad;
        loading = false;
      })
      .catch(e => { error = e.message; loading = false; });
  });

  // --- Apps ---
  function openCreateApp() {
    editingId = null;
    editingSlug = '';
    appForm = { name: '', slug: '', tagline: '', description: '', category: '', icon: '', color: '#3b82f6', free: true, dependencies: [], system: false };
    formType = 'apps'; showForm = true;
  }
  function openEditApp(app: App) {
    editingId = app.id;
    editingSlug = app.slug;
    appForm = { name: app.name, slug: app.slug, tagline: app.tagline, description: app.description || '', category: app.category, icon: app.icon, color: app.color, free: app.free, dependencies: [...(app.dependencies || [])], system: !!app.system };
    formType = 'apps'; showForm = true;
  }
  async function saveApp() {
    saving = true;
    try {
      if (editingId) {
        await updateApp(editingId, appForm);
      } else {
        await createApp(appForm);
      }
      const fresh = await getApps();
      apps = fresh;
      showForm = false;
    } catch (e: any) { error = e.message; }
    saving = false;
  }
  async function handleDeleteApp(app: App) {
    if (!confirm(`Delete "${app.name}"?`)) return;
    try { await deleteApp(app.id); apps = apps.filter(a => a.id !== app.id); } catch (e: any) { error = e.message; }
  }

  // --- Plans ---
  function openCreatePlan() {
    editingId = null;
    planForm = { name: '', slug: '', description: '', cpu: '', memory: '', storage: '', price_omr: 0, popular: false, sort_order: 0, features: [], stripe_price_id: '' };
    newFeature = '';
    formType = 'plans'; showForm = true;
  }
  function openEditPlan(plan: Plan) {
    editingId = plan.id;
    planForm = {
      name: plan.name, slug: plan.slug, description: plan.description,
      cpu: plan.resources.cpu, memory: plan.resources.memory, storage: plan.resources.storage,
      price_omr: plan.monthly_price, popular: plan.popular, sort_order: plan.sort_order,
      features: [...(plan.features || [])], stripe_price_id: plan.stripe_price_id || '',
    };
    newFeature = '';
    formType = 'plans'; showForm = true;
  }
  function addFeature() {
    const f = newFeature.trim();
    if (f && !planForm.features.includes(f)) {
      planForm.features = [...planForm.features, f];
      newFeature = '';
    }
  }
  function removeFeature(idx: number) {
    planForm.features = planForm.features.filter((_, i) => i !== idx);
  }
  async function savePlan() {
    saving = true;
    try {
      if (editingId) {
        await updatePlan(editingId, planForm);
      } else {
        await createPlan(planForm);
      }
      const fresh = await getPlans();
      plans = fresh;
      showForm = false;
    } catch (e: any) { error = e.message; }
    saving = false;
  }
  async function handleDeletePlan(plan: Plan) {
    if (!confirm(`Delete plan "${plan.name}"?`)) return;
    try { await deletePlan(plan.id); plans = plans.filter(p => p.id !== plan.id); } catch (e: any) { error = e.message; }
  }

  // --- Industries ---
  function openCreateIndustry() {
    editingId = null;
    indForm = { name: '', slug: '', emoji: '', description: '', selected_apps: [] };
    formType = 'industries'; showForm = true;
  }
  function openEditIndustry(ind: Industry) {
    editingId = ind.id;
    indForm = { name: ind.name, slug: ind.slug, emoji: ind.icon, description: ind.description, selected_apps: [...ind.app_ids] };
    formType = 'industries'; showForm = true;
  }
  async function saveIndustry() {
    saving = true;
    try {
      const payload = { ...indForm, suggested_apps: indForm.selected_apps };
      if (editingId) {
        await updateIndustry(editingId, payload);
      } else {
        await createIndustry(payload);
      }
      const fresh = await getIndustries();
      industries = fresh;
      showForm = false;
    } catch (e: any) { error = e.message; }
    saving = false;
  }
  async function handleDeleteIndustry(ind: Industry) {
    if (!confirm(`Delete industry "${ind.name}"?`)) return;
    try { await deleteIndustry(ind.id); industries = industries.filter(i => i.id !== ind.id); } catch (e: any) { error = e.message; }
  }

  // --- Addons ---
  function openCreateAddon() {
    editingId = null;
    addonForm = { name: '', slug: '', description: '', price_omr: 0, included: false, category: '' };
    formType = 'addons'; showForm = true;
  }
  function openEditAddon(addon: AddOn) {
    editingId = addon.id;
    addonForm = { name: addon.name, slug: addon.slug, description: addon.description, price_omr: addon.monthly_price, included: addon.included, category: addon.category };
    formType = 'addons'; showForm = true;
  }
  async function saveAddon() {
    saving = true;
    try {
      if (editingId) {
        await updateAddon(editingId, addonForm);
      } else {
        await createAddon(addonForm);
      }
      const fresh = await getAddons();
      addons = fresh;
      showForm = false;
    } catch (e: any) { error = e.message; }
    saving = false;
  }
  async function handleDeleteAddon(addon: AddOn) {
    if (!confirm(`Delete add-on "${addon.name}"?`)) return;
    try { await deleteAddon(addon.id); addons = addons.filter(a => a.id !== addon.id); } catch (e: any) { error = e.message; }
  }

  const tabs: { id: Tab; label: string }[] = [
    { id: 'apps', label: 'Apps' },
    { id: 'plans', label: 'Plans' },
    { id: 'industries', label: 'Industries' },
    { id: 'addons', label: 'Add-ons' },
  ];

  function closeForm() { showForm = false; }
  function handleFormSave() {
    if (formType === 'apps') saveApp();
    else if (formType === 'plans') savePlan();
    else if (formType === 'industries') saveIndustry();
    else if (formType === 'addons') saveAddon();
  }

  // Helper: find app name by slug
  function appName(slug: string): string {
    return apps.find(a => a.slug === slug)?.name || slug;
  }
</script>

<AdminShell activePage="catalog">
  {#snippet children(user: User)}
<div>
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Catalog Management</h1>
      <p class="mt-1 text-sm text-[var(--color-text-dim)]">Manage apps, plans, industries, and add-ons</p>
    </div>
    <button
      onclick={() => {
        if (activeTab === 'apps') openCreateApp();
        else if (activeTab === 'plans') openCreatePlan();
        else if (activeTab === 'industries') openCreateIndustry();
        else if (activeTab === 'addons') openCreateAddon();
      }}
      class="rounded-lg bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-[var(--color-accent-hover)]"
    >
      + Add {activeTab === 'apps' ? 'App' : activeTab === 'plans' ? 'Plan' : activeTab === 'industries' ? 'Industry' : 'Add-on'}
    </button>
  </div>

  {#if error}
    <div class="mt-4 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-sm text-[var(--color-danger)]">
      {error}
      <button onclick={() => error = ''} class="ml-2 underline">dismiss</button>
    </div>
  {/if}

  <!-- Tabs -->
  <div class="mt-6 flex gap-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-1">
    {#each tabs as tab}
      <button
        onclick={() => activeTab = tab.id}
        class="flex-1 rounded-md px-3 py-2 text-sm font-medium transition-colors
          {activeTab === tab.id ? 'bg-[var(--color-accent)] text-white' : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'}"
      >
        {tab.label}
      </button>
    {/each}
  </div>

  {#if loading}
    <div class="mt-12 flex justify-center">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else}
    <!-- Apps Tab — mirrors marketplace /apps/ card layout -->
    {#if activeTab === 'apps'}
      <div class="apps-grid mt-4">
        {#each apps as app}
          <div class="app-card" onclick={() => openEditApp(app)} role="button" tabindex="0" onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openEditApp(app); } }}>
            {#if app.logo}
              <img src={app.logo} alt={app.name} class="app-logo" loading="lazy" />
            {:else}
              <span class="app-icon" style="background: {app.color}">{app.icon || app.name[0]}</span>
            {/if}
            <div class="app-body">
              <div class="app-top">
                <span class="app-name">{app.name}</span>
                <span class="app-cat">{app.category}</span>
              </div>
              <p class="app-desc">{app.description || app.tagline}</p>
              <div class="app-chips">
                {#if app.free}
                  <span class="chip chip-free">FREE</span>
                {/if}
                {#if app.system}
                  <span class="chip chip-system">SYSTEM</span>
                {/if}
                {#if app.dependencies && app.dependencies.length > 0}
                  {#each app.dependencies as dep}
                    <span class="chip chip-dep" title="Bundled dependency">+ {appName(dep)}</span>
                  {/each}
                {/if}
              </div>
            </div>
            <div class="app-actions">
              <button class="action-btn edit" onclick={(e) => { e.stopPropagation(); openEditApp(app); }} title="Edit">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z"/></svg>
              </button>
              <button class="action-btn del" onclick={(e) => { e.stopPropagation(); handleDeleteApp(app); }} title="Delete">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6h14z"/></svg>
              </button>
            </div>
          </div>
        {/each}
      </div>

    <!-- Plans Tab -->
    {:else if activeTab === 'plans'}
      <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each plans as plan}
          <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
            <div class="flex items-start justify-between">
              <div>
                <p class="text-lg font-bold text-[var(--color-text-strong)]">{plan.name} {plan.popular ? '(Popular)' : ''}</p>
                <p class="mt-1 text-2xl font-bold text-[var(--color-accent)]">{plan.monthly_price} <span class="text-sm font-normal text-[var(--color-text-dim)]">OMR/mo</span></p>
              </div>
              <div class="flex gap-1">
                <button onclick={() => openEditPlan(plan)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]" title="Edit">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z"/></svg>
                </button>
                <button onclick={() => handleDeletePlan(plan)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-danger)]/10 hover:text-[var(--color-danger)]" title="Delete">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0"/></svg>
                </button>
              </div>
            </div>
            <p class="mt-2 text-xs text-[var(--color-text-dim)]">{plan.description}</p>
            <div class="mt-3 space-y-1 text-xs text-[var(--color-text-dim)]">
              <p>CPU: {plan.resources.cpu} | Memory: {plan.resources.memory} | Storage: {plan.resources.storage}</p>
            </div>
            {#if plan.features?.length}
              <div class="mt-2 flex flex-wrap gap-1">
                {#each plan.features as feat}
                  <span class="rounded-full bg-[var(--color-accent)]/10 px-2 py-0.5 text-[10px] text-[var(--color-accent)]">{feat}</span>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      </div>

    <!-- Industries Tab -->
    {:else if activeTab === 'industries'}
      <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each industries as industry}
          <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <div class="flex items-start justify-between">
              <div class="flex items-center gap-3">
                <span class="text-2xl">{industry.icon}</span>
                <div>
                  <p class="font-medium text-[var(--color-text-strong)]">{industry.name}</p>
                  <p class="text-xs text-[var(--color-text-dim)]">{industry.app_ids.length} bundled apps</p>
                </div>
              </div>
              <div class="flex gap-1">
                <button onclick={() => openEditIndustry(industry)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]" title="Edit">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z"/></svg>
                </button>
                <button onclick={() => handleDeleteIndustry(industry)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-danger)]/10 hover:text-[var(--color-danger)]" title="Delete">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0"/></svg>
                </button>
              </div>
            </div>
            <div class="mt-2 flex flex-wrap gap-1">
              {#each industry.app_ids as slug}
                <span class="rounded-full bg-[var(--color-surface-hover)] px-2 py-0.5 text-[10px] text-[var(--color-text)]">{appName(slug)}</span>
              {/each}
            </div>
          </div>
        {/each}
      </div>

    <!-- Add-ons Tab -->
    {:else if activeTab === 'addons'}
      <div class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each addons as addon}
          <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <div class="flex items-start justify-between">
              <div>
                <p class="font-medium text-[var(--color-text-strong)]">{addon.name}</p>
                <p class="mt-1 text-xs text-[var(--color-text-dim)]">{addon.description}</p>
              </div>
              <div class="flex items-center gap-2">
                {#if addon.included}
                  <span class="rounded-full bg-[var(--color-success)]/10 px-2 py-0.5 text-xs font-medium text-[var(--color-success)]">Included</span>
                {:else}
                  <span class="text-sm font-medium text-[var(--color-accent)]">{addon.monthly_price} OMR/mo</span>
                {/if}
                <button onclick={() => openEditAddon(addon)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]" title="Edit">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z"/></svg>
                </button>
                <button onclick={() => handleDeleteAddon(addon)} class="rounded p-1 text-[var(--color-text-dim)] hover:bg-[var(--color-danger)]/10 hover:text-[var(--color-danger)]" title="Delete">
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0"/></svg>
                </button>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- Modal Form -->
  {#if showForm}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div class="w-full max-w-lg rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 max-h-[85vh] overflow-y-auto">
        <h3 class="text-lg font-semibold text-[var(--color-text-strong)]">
          {editingId ? 'Edit' : 'Create'} {formType === 'apps' ? 'App' : formType === 'plans' ? 'Plan' : formType === 'industries' ? 'Industry' : 'Add-on'}
        </h3>
        <form onsubmit={(e) => { e.preventDefault(); handleFormSave(); }} class="mt-4 space-y-3">

          {#if formType === 'apps'}
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Name</label>
              <input bind:value={appForm.name} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Slug</label>
              <input bind:value={appForm.slug} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Tagline <span class="text-[10px] opacity-60">(short, one line)</span></label>
              <input bind:value={appForm.tagline} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Description <span class="text-[10px] opacity-60">(rendered on cards — 2 lines)</span></label>
              <textarea bind:value={appForm.description} rows="2" class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)] resize-none"></textarea>
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Category</label>
                <input bind:value={appForm.category} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Color</label>
                <input bind:value={appForm.color} type="color" class="mt-1 h-9 w-full cursor-pointer rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-1" />
              </div>
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Icon (emoji or text)</label>
              <input bind:value={appForm.icon} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div class="flex items-center gap-4">
              <label class="flex items-center gap-2 text-sm text-[var(--color-text)]">
                <input bind:checked={appForm.free} type="checkbox" class="rounded" /> Free app
              </label>
              <label class="flex items-center gap-2 text-sm text-[var(--color-text)]">
                <input bind:checked={appForm.system} type="checkbox" class="rounded" /> System (hide from marketplace)
              </label>
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Dependencies ({appForm.dependencies.length} selected)</label>
              <div class="mt-1">
                <AppPicker
                  apps={apps}
                  selected={appForm.dependencies}
                  excludeSlug={editingSlug}
                  placeholder="Search dependencies (mysql, postgres, redis…)"
                  onchange={(next) => { appForm.dependencies = next; }}
                />
              </div>
              <p class="mt-1 text-[11px] text-[var(--color-text-dimmer)]">Apps required for this one to run. They are provisioned automatically alongside.</p>
            </div>

          {:else if formType === 'plans'}
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Name</label>
                <input bind:value={planForm.name} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Slug</label>
                <input bind:value={planForm.slug} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Description</label>
              <input bind:value={planForm.description} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div class="grid grid-cols-3 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">CPU</label>
                <input bind:value={planForm.cpu} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" placeholder="2 vCPU" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Memory</label>
                <input bind:value={planForm.memory} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" placeholder="4 GB" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Storage</label>
                <input bind:value={planForm.storage} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" placeholder="25 GB" />
              </div>
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Price (OMR)</label>
                <input bind:value={planForm.price_omr} type="number" class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Sort order</label>
                <input bind:value={planForm.sort_order} type="number" class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Stripe Price ID</label>
              <input bind:value={planForm.stripe_price_id} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm font-mono text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" placeholder="price_1A2B3C..." />
            </div>
            <label class="flex items-center gap-2 text-sm text-[var(--color-text)]">
              <input bind:checked={planForm.popular} type="checkbox" class="rounded" /> Popular
            </label>

            <!-- Features list -->
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Features</label>
              <div class="mt-1 flex gap-2">
                <input
                  bind:value={newFeature}
                  placeholder="Add a feature..."
                  onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addFeature(); } }}
                  class="flex-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
                />
                <button type="button" onclick={addFeature} class="rounded-lg bg-[var(--color-accent)]/20 px-3 py-2 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent)]/30">Add</button>
              </div>
              {#if planForm.features.length > 0}
                <div class="mt-2 flex flex-wrap gap-1.5">
                  {#each planForm.features as feat, idx}
                    <span class="flex items-center gap-1 rounded-full bg-[var(--color-accent)]/10 px-2.5 py-1 text-xs text-[var(--color-accent)]">
                      {feat}
                      <button type="button" onclick={() => removeFeature(idx)} class="ml-0.5 text-[var(--color-accent)] hover:text-[var(--color-danger)]">&times;</button>
                    </span>
                  {/each}
                </div>
              {/if}
            </div>

          {:else if formType === 'industries'}
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Name</label>
                <input bind:value={indForm.name} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Slug</label>
                <input bind:value={indForm.slug} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Emoji</label>
                <input bind:value={indForm.emoji} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" placeholder="🍽" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Description</label>
                <input bind:value={indForm.description} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>

            <!-- App selection via searchable chip picker (same pattern as dependencies) -->
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Recommended Apps ({indForm.selected_apps.length} selected)</label>
              <div class="mt-1">
                <AppPicker
                  apps={apps.filter(a => !a.system)}
                  selected={indForm.selected_apps}
                  placeholder="Search apps to recommend…"
                  onchange={(next) => { indForm.selected_apps = next; }}
                />
              </div>
              <p class="mt-1 text-[11px] text-[var(--color-text-dimmer)]">Apps auto-selected when a user picks this industry in the wizard.</p>
            </div>

          {:else if formType === 'addons'}
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Name</label>
                <input bind:value={addonForm.name} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Slug</label>
                <input bind:value={addonForm.slug} required class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Description</label>
              <input bind:value={addonForm.description} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Price (OMR)</label>
                <input bind:value={addonForm.price_omr} type="number" class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
              <div>
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Category</label>
                <input bind:value={addonForm.category} class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]" />
              </div>
            </div>
            <label class="flex items-center gap-2 text-sm text-[var(--color-text)]">
              <input bind:checked={addonForm.included} type="checkbox" class="rounded" /> Included (free)
            </label>
          {/if}

          <div class="flex justify-end gap-2 pt-2">
            <button type="button" onclick={closeForm} class="rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)]">
              Cancel
            </button>
            <button type="submit" disabled={saving} class="rounded-lg bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white hover:bg-[var(--color-accent-hover)] disabled:opacity-50">
              {saving ? 'Saving...' : editingId ? 'Update' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  {/if}
</div>
  {/snippet}
</AdminShell>

<style>
  /* Mirror marketplace /apps/ card look-and-feel */
  /* Auto-fit: pack as many cards as fit, then stretch remaining width across them.
     min = 360px mirrors the marketplace 3-col width at ~1100px container, so admin
     cards are never narrower than marketplace. */
  .apps-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: 0.65rem;
  }

  .app-card {
    position: relative;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    padding: 0.6rem;
    display: flex;
    align-items: stretch;
    gap: 0.75rem;
    transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
    height: 108px;
    overflow: hidden;
    cursor: pointer;
    color: inherit;
    text-decoration: none;
  }
  .app-card:hover {
    transform: translateY(-2px);
    border-color: var(--color-accent);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  }

  .app-logo {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .app-icon {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    color: #fff;
    font-size: 1.3rem;
    font-weight: 700;
  }

  .app-body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .app-top {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
  }
  .app-name {
    color: var(--color-text-strong);
    font-size: 0.92rem;
    font-weight: 600;
    line-height: 1.2;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1 1 auto;
    min-width: 0;
  }
  .app-cat {
    color: var(--color-text-dim);
    font-size: 0.68rem;
    text-transform: capitalize;
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .app-desc {
    margin: 0;
    color: var(--color-text);
    font-size: 0.78rem;
    line-height: 1.45;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .app-chips {
    margin-top: 0.25rem;
    display: flex;
    flex-wrap: nowrap;
    gap: 0.25rem;
    overflow: hidden;
    mask-image: linear-gradient(to right, #000 85%, transparent);
    -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
    min-height: 1.4rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    line-height: 1.4;
    white-space: nowrap;
  }
  .chip-free {
    background: color-mix(in srgb, var(--color-success) 14%, transparent);
    color: var(--color-success);
  }
  .chip-system {
    background: color-mix(in srgb, var(--color-text-dim) 18%, transparent);
    color: var(--color-text-dim);
  }
  .chip-dep {
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent);
    font-weight: 500;
  }

  .app-actions {
    position: absolute;
    top: 0.5rem;
    right: 0.5rem;
    display: flex;
    gap: 0.25rem;
    opacity: 0;
    transform: scale(0.9);
    transition: opacity 0.15s, transform 0.15s;
  }
  .app-card:hover .app-actions {
    opacity: 1;
    transform: scale(1);
  }
  .action-btn {
    width: 28px; height: 28px;
    border-radius: 50%;
    border: none;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--color-bg);
    color: var(--color-text-dim);
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.18);
  }
  .action-btn svg { width: 14px; height: 14px; }
  .action-btn.edit:hover { color: var(--color-accent); }
  .action-btn.del:hover { color: #EF4444; }
</style>
