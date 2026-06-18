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

## C. Walk-time wires — what a real UAT walk exercises (NEVER reached this session → highest risk)

| # | Case | Expected | Status |
|---|------|----------|:---:|
| C1 | Funnel → per-Org vCluster install | per-Org `vcluster@0.33.3` StatefulSet not kyverno-denied (images proxy-globbed) | ✅ render-validated (#3760, both images proxy-ghcr) |
| C2 | Funnel → per-Org gitea token / gitops | per-Org GitRepository auth works (mirror of the A3 break?) | ⏳ validator `add534…` auditing |
| C3 | App launch into Org vCluster | app DB/SSO secrets delivered across the boundary | ⏳ validator `add534…` |
| C4 | 17-surface zero-login SSO landing | each app redirect→keycloak→callback resolves end-to-end | ⏳ validator `add534…` + depends on A5/A6 |
| C5 | Showback / Jobs / Topology / Orgs UI | live data reads from vCluster resources the host catalyst-api can see | ⏳ validator `add534…` |

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
