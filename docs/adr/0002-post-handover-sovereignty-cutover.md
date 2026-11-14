# ADR-0002: Post-Handover Sovereignty Cutover

| | |
|---|---|
| **Status** | Accepted — 2026-05-04 |
| **Authors** | hatiyildiz, Claude (Opus 4.7) |
| **Date** | 2026-05-04 |
| **Supersedes** | — |
| **Superseded by** | — |
| **Related** | [ADR-0001](0001-catalyst-control-plane-architecture.md), #790, #791, #792, #793, #794 |

---

> **Status (2026-08-26) — errata (ADR body is immutable; notes-only):**
> - **Partially superseded: the cutover is an 11-step chain, not eight.** The shipped chart `platform/self-sovereign-cutover/chart` runs templates 01..11 — the eight in §3.3 plus `09 gitea-token-mint`, `10 vcluster-registry-pivot`, `11 crossplane-provider-pivot`. The 10-minute deny-egress independence proof is **step 08** (steps 09–11 follow it), not the final step. The **eight tethers** in §2.1 remain correct. See `docs/STATUS.md` §2.2 and `docs/ARCHITECTURE.md`.
> - **Consolidated links:** `INVIOLABLE-PRINCIPLES.md` → [`../PRINCIPLES.md`](../PRINCIPLES.md) (Principle #11); `SOVEREIGN-PROVISIONING.md` → [`../RUNBOOKS.md`](../RUNBOOKS.md). Both were folded in under the lean-doc strategy — read the §3.4 and footer references accordingly.
> - **Substrate moved:** the §2.1 tether audit cites `infra/hetzner/…`; provisioning IaC now lives under `infra/providers/huawei/` (current substrate is Huawei kom4dc; Hetzner is legacy).

## 1. Status

Accepted 2026-05-04. This ADR extends ADR-0001; it does not contradict it. Section 4 of ADR-0001 (component layout) and section 11 of `ARCHITECTURE.md` (Catalyst-on-Catalyst) are now read in conjunction with this document. The sovereignty cutover described here is the **canonical** path by which a franchised Sovereign sheds its mothership tether after handover.

## 2. Context

### 2.1 The eight tethers

A franchised Sovereign emerging from Phase-1 provisioning is operationally tethered to the OpenOva mothership in **eight** places. The tether map (audited 2026-05-04 against `infra/hetzner/cloudinit-control-plane.tftpl`, `clusters/_template/bootstrap-kit/*.yaml`, `products/catalyst/bootstrap/api/internal/...`, and the published Catalyst chart values):

| # | Tether | Where | Phase |
|---|---|---|---|
| 1 | Flux `GitRepository.url = github.com/openova-io/openova` | `infra/hetzner/cloudinit-control-plane.tftpl:734` | P0 |
| 2 | containerd `registries.yaml` rewrites every upstream registry → `https://harbor.openova.io` (mothership Harbor) | `cloudinit-control-plane.tftpl:680-694` | P0 |
| 3 | 38 OCI HelmRepository urls = `oci://ghcr.io/openova-io` | every `clusters/_template/bootstrap-kit/*.yaml` | P0 |
| 4 | `catalyst-api` hardcodes `https://github.com/openova-io/openova` as env fallback | `provisioner.go`, `marketplace_settings.go` | P0 |
| 5 | `flux-system/ghcr-pull` Secret seeded for private GHCR pulls | cloud-init | P0 |
| 6 | Crossplane provider packages from `xpkg.upbound.io` | provider package URLs | P1 |
| 7 | Catalyst-authored images = `ghcr.io/openova-io/openova/*` | `products/catalyst/chart/values.yaml` + `clusters/_template/...` | P0 |
| 8 | OS package mirrors during cloud-init (apt, `get.k3s.io`) | one-time during cloud-init | P2 |

A Sovereign that retains any of tethers 1–7 after handover is not a franchise — it is a managed replica of OpenOva. That defeats the franchise model, exposes the customer to OpenOva's availability profile, and breaks the political contract of "your data, your cluster, your control plane."

### 2.2 The rate-limit reality

Founder direction 2026-05-04: *"the sovereign must be a true sovereign with no dependencies to openova"* — and the corollary: *"I am fine with sovereign cloud instances to temporarily use during the initial provision the openova harbor to avoid any unwanted rate limiting if there is such risk. The independence from openova must be post-handover process."*

Docker Hub anonymous-pull limits sit at 100 pulls per 6 hours per IP (200 with auth). The bootstrap-kit alone references ~15 docker.io images via `bp-cnpg`, `bp-keycloak`, `bp-cilium`, `bp-falco`, and friends. Concurrent provisions, DoD-loop rebuilds, or back-to-back tear-down/re-provision cycles will reliably exhaust this budget and produce 429-shaped `ImagePullBackOff` failures during Phase 0 + Phase 1.

The mothership Harbor at `harbor.openova.io` already runs proxy-cache projects for ghcr / docker / k8s / gcr / quay / xpkg / ecr. Routing the cold-start image pulls through it absorbs the rate-limit risk for the ~10–30 minute provisioning window. **Cold-start tether is acceptable; permanent tether is not.**

### 2.3 Why a Phase-1.5 cutover is wrong

An earlier draft of #790 proposed cutting over to local Gitea + local Harbor *during* provisioning (between bootstrap-kit slot 06 and slot 16). This was rejected for two reasons:

1. **Rate-limit exposure during the cutover itself.** Harbor proxy-cache warmup pulls upstream images. If that warmup runs while the rest of the cluster is still rolling out (60+ HelmReleases in-flight), the contention on docker.io anon limits becomes acute and produces flaky provisioning.
2. **Chicken-and-egg.** Some of the components that perform the cutover (Gitea, Harbor, the cutover Job itself) are themselves blueprints pulled through registries that are about to be swapped. Performing this swap mid-roll means the in-flight HelmReleases see a registry change underneath them.

Cutover after Phase-1 is stable, after handover is acknowledged, on operator demand — that's when the cluster is quiet enough to swap registries cleanly.

### 2.4 Why "manual operator runbook" is wrong

Pivoting eight infrastructure tethers without a progress UI is a footgun. An operator who runs `kubectl patch gitrepository`, then forgets to patch the 38 HelmRepositories, then reboots the cluster, has bricked their franchise with no rollback path. A first-class blueprint with sequential Jobs, status ConfigMap, SSE event stream, and console UI is the only way to make the cutover safe to run in customer environments.

## 3. Decision

### 3.1 Introduce `bp-self-sovereign-cutover`

A new platform Blueprint published as `oci://ghcr.io/openova-io/bp-self-sovereign-cutover:<semver>`. It is added to the bootstrap-kit at slot **06a** and reconciles **dormant** during Phase 1 — the chart installs JobTemplate ConfigMaps + RBAC + status ConfigMap, but does **not** create the eight Jobs until the operator triggers cutover.

This matches Inviolable Principle #3 (Helm-via-Flux is the only K8s manifest packaging unit) and Inviolable Principle #1 (event-driven, never polling) — see ADR-0001 §2.

### 3.2 Trigger model — operator-driven, post-handover

Cutover is initiated by:

- Operator clicks **"Achieve True Sovereignty"** on the admin console after handover lands (delivered in #793), **OR**
- `catalyst-api` auto-fires after the first successful operator login on a freshly handed-over Sovereign, after a configurable grace period (default off — operator-explicit by default; field can be flipped per-customer)

The button POSTs to `POST /api/v1/sovereign/cutover/start` (delivered in #792). `catalyst-api` translates that into a sequence of K8s Job creations from the JobTemplate ConfigMaps the chart installed. Progress streams to the UI via the existing SSE endpoint pattern (consistent with ADR-0001 §6).

### 3.3 The eight cutover steps (delivered in #791)

```
01  gitea-mirror              git clone --mirror github.com/openova-io/openova
                              → push to local gitea/openova/openova

02  harbor-projects           Harbor v2 API: create proxy-ghcr, proxy-docker,
                              proxy-k8s, proxy-gcr, proxy-quay, proxy-xpkg,
                              proxy-ecr projects on the local Harbor

03  harbor-prewarm            Pull-through-cache every image referenced by
                              clusters/_template/bootstrap-kit/*.yaml so the
                              local Harbor has bytes before traffic flips

04  registry-pivot            DaemonSet rewrites /etc/rancher/k3s/registries.yaml
                              on every node (mothership Harbor → local Harbor),
                              triggers containerd config reload, sentinel pod
                              confirms a pull through the new path succeeds

05  flux-gitrepository-patch  Patch flux-system GitRepository.url
                              github.com/openova-io/openova
                              → http://gitea-http.gitea:3000/openova/openova

06  helmrepo-patches          Patch all 38 OCI HelmRepositories
                              oci://ghcr.io/openova-io/* → oci://harbor.<sov>/openova-io/*

07  catalyst-api-env-patch    Patch catalyst-api Deployment env
                              CATALYST_GITOPS_REPO_URL → local Gitea URL
                              (no upstream fallback after this point)

08  egress-block-test         NetworkPolicy deny-egress to github.com,
                              ghcr.io, harbor.openova.io for 10 min;
                              all reconciles must remain green;
                              this is the DoD proof of independence
```

Each step writes its result into the chart's status ConfigMap. Step 08 holding green for 10 min is the **only** condition under which `cutoverComplete=true` is set.

### 3.4 Documentation

- ADR-0002 (this document) — the architectural decision.
- `ARCHITECTURE.md` §11 — Phase-2 cutover added to the Catalyst-on-Catalyst section as the canonical post-handover sequence.
- `INVIOLABLE-PRINCIPLES.md` Principle #11 — independence post-handover is non-negotiable.
- `SOVEREIGN-PROVISIONING.md` — to be updated by a follow-up ticket to wire Phase-2 into the provisioning runbook.

## 4. Consequences

### 4.1 Pre-cutover

A freshly handed-over Sovereign behaves exactly as today:

- Image pulls go through `harbor.openova.io` (mothership) — rate-limit safe
- Flux reconciles from `github.com/openova-io/openova` — read-only public clone
- HelmRepositories pull from `oci://ghcr.io/openova-io` — public artefacts
- `catalyst-api` `CATALYST_GITOPS_REPO_URL` falls back to the upstream repo

This soft-tethered window is the **provisioning safety mode**. Customers who never run the cutover keep working, but they are not yet sovereign.

### 4.2 Post-cutover

After step 08 passes:

- Image pulls go through the customer's local Harbor; mothership Harbor is unreachable and the cluster does not care
- Flux reconciles from the customer's local Gitea
- HelmRepositories pull from the customer's local Harbor
- `catalyst-api` reads the customer's Gitea URL with no upstream fallback
- The customer can black-hole `github.com`, `ghcr.io`, `harbor.openova.io` at their firewall and the Sovereign continues operating

This is the only state in which OpenOva can truthfully describe a Sovereign as franchised rather than managed.

### 4.3 Operator experience

The "Achieve True Sovereignty" button is a one-click action with a progress card showing eight steps, current step name, percentage complete, error (if any), and the deny-egress holding-time countdown. The operator has full visibility; no opaque scripts, no kubectl pasting.

### 4.4 Reversibility

The cutover is not designed to be reversed. Once the Sovereign is independent, "going back" means re-tethering to the mothership — which has no business reason to exist. If a customer ever needs to recover (e.g., local Gitea data loss), the recovery path is restore-from-backup, not roll-back-the-cutover.

### 4.5 Failure handling

If a cutover step fails:

- The step records its failure in the status ConfigMap
- The SSE stream surfaces the error to the UI
- The cluster is left in a hybrid state (some tethers swapped, others still pointing at mothership) — this is **functionally equivalent to pre-cutover** for the un-swapped tethers, so the Sovereign remains operational
- The operator can re-run cutover; each step is idempotent (e.g., `git clone --mirror` becomes `git remote update`; `harbor-projects` checks before creating; `flux-gitrepository-patch` is a no-op if the URL is already correct)

### 4.6 Audit trail

Every cutover step publishes a CloudEvents-shaped envelope on NATS JetStream (`catalyst.cutover.*` subjects), consistent with ADR-0001 §6. The operator action that triggered the cutover, the operator identity (Keycloak token), and the eight step results land in the audit log stream.

## 5. Alternatives Considered

### 5.1 Phase-1.5 cutover during provisioning

**Rejected.** Cutting over mid-provision exposes the cluster to docker.io rate-limit failures during Harbor warmup (the warmup itself pulls upstream). Concurrent provisions or fast tear-down/re-provision cycles compound the risk. The mothership Harbor proxy is a known-good rate-limit absorber for the provisioning window; pulling that out before the window closes is gratuitous. (Original Agent B in #790 was cancelled for exactly this reason.)

### 5.2 Sovereign-built-in mirror (chicken-and-egg)

**Rejected.** A pure "Sovereign serves its own mirror from day one" design requires Gitea + Harbor to come up *before* the bootstrap-kit pulls images for Gitea + Harbor. Solving the bootstrap of the bootstrap is a research project; the cold-start tether to mothership Harbor neatly avoids the loop and lets us ship today.

### 5.3 Manual operator-driven cutover (runbook, no chart)

**Rejected.** Eight steps with cross-step dependencies + idempotency requirements + RBAC + status reporting + UI integration is exactly the surface a Helm chart + Job pattern is designed to package. A 30-page runbook with `kubectl patch` snippets is a footgun in a customer environment, and it cannot deliver the live progress card that operators expect.

### 5.4 Crossplane Composition for the cutover

**Rejected.** Per Inviolable Principle #3 (ADR-0001 §2.3), Crossplane stays in its lane: cloud-provider APIs. The cutover is K8s-to-K8s composition (creating Jobs, patching CRs, applying NetworkPolicies). That is Flux + a thin chart, not Crossplane.

### 5.5 Auto-fire on first login by default

**Rejected as default; available as opt-in.** Auto-firing the cutover the moment the operator logs in is operator-hostile — the operator may want to inspect the freshly handed-over Sovereign before pivoting eight infrastructure tethers. Default is operator-explicit (button click). Auto-fire is a per-customer field for installations that want zero-touch sovereignty.

---

## 6. Implementation pointers

| Concern | Where |
|---|---|
| Chart source | `platform/self-sovereign-cutover/chart/` (delivered by #791) |
| Bootstrap-kit slot | `clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml` |
| API handlers | `products/catalyst/bootstrap/api/internal/handler/cutover.go` (delivered by #792) |
| UI surface | `products/catalyst/bootstrap/ui/` admin console card + button (delivered by #793) |
| Audit stream | NATS JetStream subject `catalyst.cutover.*` |
| Status surface | ConfigMap `self-sovereign-cutover-status` in `flux-system` namespace |

---

*Part of [OpenOva](https://openova.io). Read in conjunction with [ADR-0001](0001-catalyst-control-plane-architecture.md), [`ARCHITECTURE.md`](../ARCHITECTURE.md) §11, and [`INVIOLABLE-PRINCIPLES.md`](../PRINCIPLES.md) Principle #11.*
