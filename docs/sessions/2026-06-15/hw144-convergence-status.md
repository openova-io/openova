# hw144 convergence status — 2026-06-15 (the 100% validation run)

**Deployment** `d8e798bdf1b4256b` · 2-region kom4dc · all 5 fixes baked (catalyst `c7cde4d`).
**State: CONVERGING (not stuck)** — through one fixed source wedge.

## Live env reachability
- `console.hw138` = **000** — hw138 **wiped** (freed quota for this prov). No env.
- `console.hw144` = **000** — **pending** catalyst-platform install (the console umbrella is mid-install, ~10-20min incl. image pulls). `status=phase1-watching`.

## Convergence timeline
1. ✅ **Phase-0 cleared** — the kom4dc EIP/infra wall that killed hw140/142/143. (First prov in the saga to clear it.)
2. ⚠️→✅ **Convergence wedge (fixed in place, no re-fire):** `bp-openbao 1.2.41` helm install failed `namespaces "seaweedfs" not found` — `snapshot-replication.yaml $credNs` defaulted to the host `seaweedfs` ns, which #3477 removed (seaweedfs re-homed into the rtz vCluster). **Fixed at source: PR #3575** (`1.2.42`, `$credNs → .Release.Namespace`); **self-healed via Flux** (hw144 GitRepository tracks `main`).
3. ✅ **Chain unwound:** openbao ✓ → sso-bridge ✓ (reconciler minting OIDC clients) → `gitea-oauth-source-credentials` ✓ → **bp-gitea ✓ InstallSucceeded** → **bp-catalyst-platform = Progressing (installing the console)** → console pending.

## UAT rows — all gated on console.hw144 surfacing
The hw139-walked surfaces (placement 22/22, contexts 3-cards/11, SSO 10/12, topology, orgs, jobs, robustness) re-walk on hw144. The **fixed-but-unwalked** rows close on this prov:
- **newapi re-login** (#3564) · **Open buttons** (#3570) · **shared-PG region-kill preserves keycloak realm** (#3572) · **cutover → cutoverComplete + 600s deny-egress** (#3568).

**Trigger:** monitor `bg3dyvm63` fires the instant `console.hw144 ≠ 000` → full Playwright walk per [`hw144-walk-plan.md`](hw144-walk-plan.md). The walk is gated on machine-time convergence, not on operator input.
