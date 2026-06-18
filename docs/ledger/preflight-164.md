# Preflight Validation — hw164 (the gate: NO fire until every row is ✅)

> **Rule (founder, 2026-06-18):** "Before running 164, you must validate ALL." This file is the gate.
> Every host↔vCluster wire #3642 created, every convergence precondition, and every walk-time
> consumer is a row. hw164 fires **only** when zero rows are ❌ GAP and zero are ⏳ unverified-blocking.
> Status: ✅ PASS (proven) · ⏳ PENDING (validator/agent in flight) · ❌ GAP (must fix before fire).
>
> **Root shape being validated:** the platform is *half-migrated* — the 7 NS#1 apps run IN vc-mgmt,
> but the **console (catalyst-api/ui) + sso-bridge are still host-side** (`audit-placement-conformance.py
> rendered` → 10 slots `host→mgmt/rtz` staged). Every row below is a crossing of that boundary.

## A. Bootstrap wires — host console/install must reach vCluster apps (the 10-wire manifest)

| # | Wire (host consumer → vCluster producer) | Expected on a fresh prov | Fix | Status |
|---|------------------------------------------|--------------------------|-----|:---:|
| A1 | 7 apps → host shared-pg `-rw` DNS | `shared-pg[-b/-c]-rw.shared-data` resolves inside vc-mgmt | PR #3808 (replicateServices.fromHost) | ✅ merged+verified live |
| A2 | console → gitea HTTP svc | `gitea-http.gitea:3000` resolves host-side | PR #3810 (replicateServices.toHost) | ⏳ merge-ready (CLEAN) |
| A3 | console gitea-token mint → gitea-admin-secret | plain `gitea/gitea-admin-secret` present host-side | PR #3812 (lookup-and-mirror bridge) | ⏳ merge-ready (CLEAN) |
| A4 | 7 apps → their DB/SSO secrets | host-minted `<app>-database-secret` mirrored into vc-mgmt | vcluster 0.23 `sync.fromHost.secrets` | ✅ on main |
| A5 | sso-bridge → keycloak SA creds | `catalyst-kc-sa-credentials` (addr/client-id/secret) POPULATED | fix agent `a09aed…` | ⏳ agent in flight |
| A6 | console → keycloak OIDC clients | `catalyst-{kc-sa,pin-broker,magic-link,openova-kc}-credentials` populated → handover 200 not 401 | fix agent `a09aed…` | ⏳ agent in flight |
| A7 | cert-manager → powerdns DNS-01 | `cert-manager/powerdns-api-credentials` present + key authorized vs `pdns.openova.io` | fix agent (live-proven: needs the secret + a valid mothership key) | ⏳ agent in flight |
| A8 | sovereign-tls → bootstrap-kit gate | wildcard cert issues; sovereign-tls NOT blocked by `bp-hcloud-ccm`/`bp-cluster-autoscaler-hcloud` (N/A on Huawei) | fix agent (gate hcloud HRs off non-Hetzner) | ⏳ agent in flight |
| A9 | ferretdb → sme-database-secret | `org-services/ferretdb` not CreateContainerConfigError | fix agent | ⏳ agent in flight |
| A10 | openbao-snapshot/hubble-oauth2/oidc-gate×2 → their secrets | none CreateContainerConfigError | fix agent | ⏳ agent in flight |

## B. Convergence preflight — will the bootstrap-kit reach a walkable console zero-touch?

| # | Case | Expected | Status |
|---|------|----------|:---:|
| B1 | `audit-placement-conformance.py rendered` | PASS (no placement drift) | ✅ PASS (ran clean) |
| B2 | `helm template` full bootstrap-kit | renders, no unbridged route/secret/CNPG | ⏳ to run pre-fire |
| B3 | hcloud HRs gated off on Huawei | `bp-hcloud-ccm` + `bp-cluster-autoscaler-hcloud` do NOT render/block on provider=huawei | ❌ GAP (was blocking sovereign-tls on hw163) → fix agent A8 |
| B4 | flux + postgresql cold-pull pre-pull | convergence not serialized ~26min behind one image | ⏳ PR #3809 merge-ready |
| B5 | CoreDNS `.server` github-IPv4 pin | primary node PUTs kubeconfig (no IPv6 wedge) | ✅ on main (#3806/#3714) |
| B6 | bootstrap-kit-crs (CRD-ordering) | raw ExternalSecret/HTTPRoute/CNPG don't dry-run-deadlock the atomic kit | ✅ on main (#3807) |

## C. Walk-time wires — validator `add534…` COMPLETE (26 wires audited). ROOT + systematic fix below.

**ROOT (confirmed by 4 cross-checked traces):** `sync.toHost.services` projects in-vCluster Services to the
host ONLY as mangled `<svc>-x-<ns>-x-mgmt-vcluster.mgmt.svc` — the plain `<svc>.<ns>.svc` does NOT exist
host-side (zero ExternalName aliases anywhere). Three stranded host-name families break every host→vc-mgmt call:
**(1) `keycloak.keycloak.svc`** · **(2) `gitea-http.gitea.svc`** · **(3) `openbao.openbao.svc`**.
Migration was abandoned mid-way (Mimir/NATS mangled; gitea/openbao/newapi left plain — `api-deployment.yaml`).

**SYSTEMATIC FIX (≈15 wires → 2 edits):** (a) extend `replicateServices.toHost` (PR #3810 pattern) to export
**keycloak** + **openbao** to host plain names (gitea-http done in #3810); (b) Jobs `helmwatch.go` HR informer
filter must include ns `mgmt`, not only `flux-system`.

| # | Category | Wire | Won't-resolve name | Verdict |
|---|----------|------|--------------------|:---:|
| C-A1 | Funnel | per-Org vCluster `0.33.3` StatefulSet images | (kyverno proxy-glob) | ✅ #3760 render-validated |
| C20🔴 | **SSO** | **sso-bridge → keycloak Admin (mint per-app clients)** | `keycloak.keycloak.svc` | ❌ AT-RISK — **THE walk-blocker** |
| C21🔴 | **SSO** | **sso-bridge → openbao (write `secret/sso/<app>`)** | `openbao.openbao.svc:8200` | ❌ AT-RISK (6 SSO ExternalSecrets empty) |
| C3 | Funnel | catalyst-gitea-token mint Job (→ all per-Org gitops) | `gitea-http.gitea.svc` + host gitea-admin-secret | ❌ AT-RISK — ROOT of funnel |
| C1,C2,C4 | Funnel | org-controller → keycloak/gitea + per-Org Flux GitRepo | keycloak/gitea plain | ❌ AT-RISK (per-Org vcluster never provisions) |
| C5 | Funnel | BSS provisioning → gitea | `gitea-http.gitea.svc` | ❌ AT-RISK |
| C7,C8 | App-launch | application/env/blueprint controllers → gitea | `gitea-http.gitea.svc` | ❌ AT-RISK |
| C13 | Console | Jobs canvas HR informer hard-filtered to `flux-system` | (11 mgmt HRs invisible) | ❌ AT-RISK (Jobs blind to migrated apps) |
| C14,C15,C16 | Console | Catalog/Blueprint→gitea · Secrets/Handover→openbao · NewAPI | gitea/openbao/newapi plain | ❌ AT-RISK |
| C9,C17,C18,C19,C24 | (mixed) | vc kubeconfig · Mimir(mangled) · Topology informer · Orgs · oidc-gate | — | ✅ BRIDGED |
| C22,C23 | SSO | each app issuer `auth.<fqdn>` + the host-bridge route | (public, Gateway→mangled) | ✅ BRIDGED (browser flow only; client-provisioning still gated on C20/C21) |
| **C10** | **App-launch** | **per-Org CUSTOMER vCluster — DB/SSO secret + shared-pg bridge** | per-Org vcluster has no audited bridge | ❓ **UNKNOWN — hw164 frontier (only a walk confirms)** |
| **C11** | **App-launch** | **per-Org Application placement** (`DefaultVClusterPlacements` knows only dmz/mgmt/rtz) | no per-Org entry | ❓ **UNKNOWN** (apps may land in shared tier, not customer vCluster — N#1 risk) |
| C25 | SSO | guacamole callback `/guacamole/` landed-logged-in | callback path | ❓ UNKNOWN (prior walk failure, live-confirm) |

> **New gate rows from this audit (must be ✅ before fire):** the keycloak + openbao `replicateServices.toHost`
> export (fixes C1–C5,C7,C8,C14–C16,C20,C21) and the Jobs `helmwatch` ns filter (C13). C10/C11/C25 are
> UNKNOWN frontiers — acceptable to fire WITH them flagged (they need the walk to resolve), but NOT the
> AT-RISK ❌ rows.

## D. Train assembly — PRs that MUST be merged (in order) before fire

| PR | What | Merge gate |
|---|------|-----------|
| #3810 | bp-mgmt-vcluster 0.2.15 gitea-http toHost | render.sh green → merge |
| #3812 | bp-catalyst-platform gitea-admin bridge | gates green → merge |
| #3809 | cloud-init flux+pg pre-pull | (UNSTABLE check — verify non-blocking) → merge |
| (agent) | keycloak-SSO + 7-secret source fixes (A5–A10) | **the critical batch — fire is GATED on this** |

## E. Fire preconditions (mechanical)

| # | Precondition | Status |
|---|--------------|:---:|
| E1 | hw163 wiped, VPC quota free (~15min release) | ⏳ wiping now |
| E2 | mothership stable after the train merges (pod-age>5min, 0 builds in_progress) | ⏳ post-merge |
| E3 | `scripts/reset-uat.py hw164` (no stale evidence) | ⏳ pre-fire |
| E4 | parent_domains TLD not LE-rate-limited | ⏳ check pre-fire |
| E5 | walk tooling ready (mint-handover-hw164 + shot.js fixed) | ⏳ re-stamp for hw164 |

## F. Architecture decision (founder flagged — the fundamental call)

The half-migrated state (console host-side, apps vCluster-side) is the *source* of rows A2–A10. The
audit's `host→mgmt` target for `bp-catalyst-platform` + `bp-sso-bridge` means the §4 intent is to move
the console IN too — which would make A2–A10 **internal (no boundary, no bridges to forget)**. Three
options, to decide before committing the architecture long-term (NOT blocking hw164's bridge-based fire):
- **(i) Bridges (current):** fast, but every future app/secret is a new wire to remember. ← hw164 path
- **(ii) Complete migration (console+sso-bridge → vc-mgmt):** §4 target; eliminates the boundary; console needs host access for provisioning/Crossplane → must verify.
- **(iii) Keep bootstrap-critical apps (keycloak,gitea) host-side, vCluster the leaf apps:** eliminates the *console-bootstrap* boundary; partial North-Star-#1.

---
**GATE SUMMARY:** A1,A4,B1,B5,B6 ✅ · A2,A3 merge-ready · **A5–A10 + B3 are the blocking batch (fix agent)** · C2–C5 (validator) · then E1–E5. hw164 fires only when A+B+C are all ✅ and D is merged.
