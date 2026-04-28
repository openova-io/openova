<script lang="ts">
  import { getMe, getMyOrgs, setAuthTokens, AUTH_CHANGED_EVENT, type User, type Org } from '../lib/api';
  import { CHECKOUT_URL } from '../lib/config';
  import Sidebar from './Sidebar.svelte';
  import type { Snippet } from 'svelte';

  let { activePage = 'dashboard', children }: { activePage?: string; children: Snippet<[User, Org | null]> } = $props();

  const ACTIVE_ORG_KEY = 'sme-active-org';
  const SESSION_CACHE_KEY = 'sme-session-cache-v1';

  type SessionCache = { user: User; orgs: Org[] };

  function readSessionCache(): SessionCache | null {
    try {
      const raw = sessionStorage.getItem(SESSION_CACHE_KEY);
      if (!raw) return null;
      const parsed = JSON.parse(raw) as SessionCache;
      if (!parsed.user || !Array.isArray(parsed.orgs)) return null;
      return parsed;
    } catch { return null; }
  }
  function writeSessionCache(u: User, list: Org[]) {
    try { sessionStorage.setItem(SESSION_CACHE_KEY, JSON.stringify({ user: u, orgs: list })); } catch {}
  }

  // SWR pattern: hydrate from sessionStorage cache immediately so the first
  // paint after in-app navigation is instant; refetch in the background to
  // catch stale data. Prevents the "unauthenticated flash → session" flicker
  // users reported when moving between console pages.
  const cached = typeof window !== 'undefined' ? readSessionCache() : null;
  let user = $state<User | null>(cached?.user ?? null);
  let orgs = $state<Org[]>(cached?.orgs ?? []);
  let org = $state<Org | null>(null);
  let loading = $state(!cached);
  let error = $state('');

  function pickOrg(list: Org[]): Org | null {
    if (!list.length) return null;
    const saved = localStorage.getItem(ACTIVE_ORG_KEY);
    if (saved) {
      const match = list.find((o) => o.id === saved);
      if (match) return match;
    }
    return list[0];
  }

  // Seed org from cache immediately so children render with the right scope.
  if (cached) {
    org = pickOrg(cached.orgs);
  }

  function switchOrg(id: string) {
    localStorage.setItem(ACTIVE_ORG_KEY, id);
    // Full reload — every page reads org-scoped data on mount.
    window.location.reload();
  }

  $effect(() => {
    // Accept token from URL params (cross-subdomain handoff from marketplace).
    // Route the write through setAuthTokens so `sme-auth-changed` fires —
    // any island mounted before the handoff (Sidebar, Header) can then
    // re-hydrate without a full reload (#83).
    const params = new URLSearchParams(window.location.search);
    const urlToken = params.get('token');
    const urlRefresh = params.get('refresh_token');
    if (urlToken) {
      setAuthTokens(urlToken, urlRefresh || '');
      window.history.replaceState({}, '', window.location.pathname);
      // Fresh token from handoff — old session cache may belong to a different user.
      try { sessionStorage.removeItem(SESSION_CACHE_KEY); } catch {}
    }

    const loadSession = () => {
      const token = localStorage.getItem('sme-token');
      if (!token) {
        window.location.href = CHECKOUT_URL;
        return;
      }
      Promise.all([getMe(), getMyOrgs()])
        .then(([u, list]) => {
          user = u;
          orgs = list || [];
          org = pickOrg(orgs);
          if (org) localStorage.setItem(ACTIVE_ORG_KEY, org.id);
          writeSessionCache(u, orgs);
          loading = false;
        })
        .catch((e) => {
          // If we already have cached data, don't tear down the UI — let the user
          // keep working. Only show the error screen on a genuine cold start.
          if (cached) return;
          error = e.message;
          loading = false;
        });
    };

    loadSession();
    // Re-hydrate on any auth change in this tab, plus cross-tab sign-out
    // via the native `storage` event.
    window.addEventListener(AUTH_CHANGED_EVENT, loadSession);
    const onStorage = (e: StorageEvent) => {
      if (e.key === 'sme-token' || e.key === null) {
        if (!localStorage.getItem('sme-token')) {
          user = null;
          window.location.href = CHECKOUT_URL;
          return;
        }
        loadSession();
      }
    };
    window.addEventListener('storage', onStorage);
    return () => {
      window.removeEventListener(AUTH_CHANGED_EVENT, loadSession);
      window.removeEventListener('storage', onStorage);
    };
  });
</script>

{#if loading}
  <div class="flex h-screen items-center justify-center">
    <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
  </div>
{:else if error}
  <div class="flex h-screen flex-col items-center justify-center gap-4">
    <p class="text-[var(--color-danger)]">{error}</p>
    <a href={CHECKOUT_URL} class="text-sm text-[var(--color-accent)] hover:underline no-underline">Sign in</a>
  </div>
{:else if user}
  <div class="flex min-h-screen">
    <Sidebar {user} {org} {orgs} {activePage} onSwitchOrg={switchOrg} />
    <main class="ml-56 flex-1 p-8">
      {@render children(user, org)}
    </main>
  </div>
{/if}
