# Orchestrator State — Catalyst-Zero Waterfall

**Updated:** 2026-04-29. **Live state, not aspirational.**
**Latest commit on main:** `934e519b` (post-Reconcile-Pass-2 batch — admin landing surface replaces the DAG provision view at `/sovereign/provision/$deploymentId` with the application card grid + per-app tabs `4047ba1d`/`a75414a7`, all 11 bp-* charts converted to umbrella v1.1.0 with proper upstream subchart payloads `43aff202`/`e42799fa`, catalyst-api per-component HelmRelease watch via new `internal/helmwatch/` package emitting `phase: "component"` SSE events `5be6bcba`, deployments persisted to RWO PVC `catalyst-api-deployments` 1Gi via new `internal/store/` `418cead0`, cloud-init flux-bootstrap split into `bootstrap-kit` + `infrastructure-config` Kustomizations `34c8de84`/`2da4c43c`, Cilium installed BEFORE Flux in cloud-init with `k8sServiceHost=127.0.0.1` to break the CNI bootstrap deadlock `e571ec7a`/`54872009`, bp-* HelmRepository url corrected to `oci://ghcr.io/openova-io` + `secretRef: ghcr-pull` `efa41803`, kube-system Namespace dropped from `01-cilium.yaml` + `05-sealed-secrets.yaml` `2022e1af`, OpenTofu CLI v1.11.6 + `infra/hetzner/` module bundled into the catalyst-api image with SHA256 verification `9b6c297d`/`61c61226`, `null_resource.dns_pool` removed from `infra/hetzner/main.tf` (PDM is the single owner of pool-domain Dynadot writes) `330211d2`, CPX SKU family accepted in tofu validation regex + empty `worker_size` allowed for solo Sovereigns `c6cbfe68`, Containerfile build stage bumped go 1.23 → 1.26 to match `go.mod` `586f0dc2`, blueprint-release CI hollow-chart guards verifying upstream subchart presence at every step `54418bd5`/`35dcb84d`/`bdeb0f54`). **catalyst-build CI:** ✅ green; PDM build (`pool-domain-manager-build`) and PowerDNS blueprint-release CI both ✅ green.

This file is the durable hand-off record for the multi-agent orchestration of the Catalyst-Zero consolidation + first-Hetzner-Sovereign waterfall. Read first when resuming work.

---

## Ticket-board snapshot (live numbers)

| | Count |
|---|---|
| Closed (this session) | 74 |
| Open | 43 |
| Parent issue | [#43](https://github.com/openova-io/openova/issues/43) |

## Group status

| Group | Tickets | Status | Branch / commits |
|---|---|---|---|
| **A — Consolidation** | 9/9 | ✅ Closed | `3c2f7e4` |
| **B — SME backend services** | 10/10 | ✅ Closed | `7646840` |
| **C — Catalyst-Zero cutover** | 8 | 🟢 Unblocked — catalyst-build CI green at `333b859`. Branch `group-c-cutover-catalyst-zero` ready in **openova-private**. Awaits operator-confirmed merge + `kubectl annotate` on Contabo. | parallel `openova-public` GitRepository (does not orphan unrelated workloads) |
| **D — Wizard** | 10/10 | ✅ Closed | `854a063` + `e87913a` + `171ff9c` + `3440bf7` + `cf60bd7` |
| **E — Provisioner** | 13 | ✅ Mostly closed via Lesson #24 revert + OpenTofu canonical path | `e668637` (revert), `e7a74f0` (variables/main/outputs), `cf60bd7` (retry endpoint) |
| **F — Bootstrap-kit charts** | 14/14 | ✅ All 11 OCI artifacts published + cosigned + SBOM-attested | `8c0f766`, `441ebae`, `62d9c7d`, `8efc6e0`, `046e5eb` |
| **G — DNS multi-domain** | 6 | ✅ Superseded by PowerDNS authoritative (#167) + pool-domain-manager (#163, #168) + registrar adapters (#170 — Cloudflare/Namecheap/GoDaddy/OVH/Dynadot). Dynadot is now one of five registrar adapters inside PDM, not the authoritative DNS surface. | `0190c605` (#167), `2854d652` (#163), `f7773943` (#168), `567d7e1f` (#170), `67fdecb7` (#171 k8gb retire) |
| **H — Franchise + voucher** | 7/7 | ✅ Closed | `f2951af` |
| **I — Wizard UX** | 6/6 | ✅ Closed | `2bcf564` + `cf60bd7` |
| **J — Hetzner infra** | 6/6 | ✅ Closed (cx32→cx42 sizing-bug fix is real) | `e5550d7` |
| **K — Documentation** | 7/8 | ✅ 7 closed; #134 (omantel ✅) deferred to DoD | `dc3f50d` |
| **L — Testing** | 8/8 | ✅ Closed (Hetzner test gated on `HETZNER_TEST_TOKEN` repo secret) | `9519c1e` |
| **M — End-to-end DoD** | 9 | 📐 Blocked on operator-provided Hetzner credentials + Group C cutover merge | — |

## Architectural compliance (Lesson #24 closed)

- **OpenTofu** owns Phase 0 — `infra/hetzner/{versions,variables,main,outputs}.tf` + `cloudinit-{control-plane,worker}.tftpl`.
- **Crossplane** is the day-2 IaC — 5 XRDs + 5 Compositions at `platform/crossplane/compositions/` under canonical `compose.openova.io/v1alpha1` group (server, network, firewall, loadbalancer, pool-allocation — the latter wraps PDM `/v1/reserve`).
- **Flux** is the GitOps reconciler — `clusters/_template/` + `clusters/omantel.omani.works/` with HelmReleases in dependency order via `dependsOn`. The bootstrap-kit ships with bp-powerdns (#167) installed in `openova-system` on the mgt cluster.
- **Blueprints** are the install unit — 12 cosigned `bp-<name>:<semver>` OCI artifacts at `ghcr.io/openova-io/`. The 10 leaves + bp-catalyst-platform umbrella under `clusters/<sovereign-fqdn>/bootstrap-kit/` are all at **v1.1.0** with proper Helm umbrella shape (each declares its upstream chart under `dependencies:` so `helm dependency build` packages the upstream payload into the OCI artifact — the prior v1.0.0 / v1.0.1 artefacts are HOLLOW per [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) §11.1). bp-powerdns 1.1.0 deploys on Catalyst-Zero only (live HelmRelease `bp-powerdns@1.1.0+ef3c785bfd24`). CI fan-out via `.github/workflows/blueprint-release.yaml` enforces the umbrella shape via four hollow-chart guards on every publish.
- **DNS architecture** — bp-powerdns is authoritative for every Sovereign zone (per-Sovereign zone model, #168). pool-domain-manager (`core/pool-domain-manager/`) allocates pool subdomains and exposes registrar adapters for byo-api flow (#163, #170). k8gb is retired (#171).
- **Inviolable principles** anchored in `docs/INVIOLABLE-PRINCIPLES.md` + `~/.claude/.../memory/feedback_inviolable_principles.md` + global CLAUDE.md.

## What still needs to happen for DoD

1. **Operator confirms Group C cutover** in openova-private + runs `kubectl annotate gitrepository/{flux-system,openova-public}` on Contabo. (~5-min outage, fully reversible.)
2. **Operator provides real Hetzner Cloud API token + project ID** to the wizard at `https://console.openova.io/sovereign`.
3. **Operator provides SSH public key** (StepCredentials).
4. **Operator picks domain** — three modes (#169): pool (`omani.works` → subdomain `omantel`), byo-manual, or byo-api with a registrar token (Cloudflare/Namecheap/GoDaddy/OVH/Dynadot).
5. **Click Provision.** Wizard POSTs to `/api/v1/deployments` and redirects to the Sovereign Admin landing surface at `/sovereign/provision/$deploymentId` (route module [`AdminPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/sovereign/AdminPage.tsx); the legacy `ProvisionPage.tsx` is a 22-line re-export shim). The page renders the Application card grid (every Application installed on this Sovereign — bootstrap-kit + user-selected — with per-card status pill flipping `pending → installing → installed | failed | degraded` as the catalyst-api emits per-component SSE events; click any card for the per-Application page at `/sovereign/provision/$deploymentId/app/$componentId` with Overview / Logs / Dependencies / Status tabs). catalyst-api runs `tofu init && tofu apply` (Tofu CLI v1.11.6 + `infra/hetzner/` module bundled in the image — `9b6c297d`/`61c61226`). Cloud-init installs k3s with `--flannel-backend=none`, **then Cilium first via Helm** with `k8sServiceHost=127.0.0.1` to break the CNI bootstrap deadlock (`e571ec7a`/`54872009`), then Flux core, then applies the GitRepository plus two Kustomizations — `bootstrap-kit` (HelmReleases) and `infrastructure-config` (ProviderConfig + Crossplane Compositions, `dependsOn: bootstrap-kit`, `wait: true` per `34c8de84`/`2da4c43c`) — both pointing at `clusters/omantel.omani.works/`. Flux reconciles HelmReleases in dependency order. ~10 minutes total. Deployment state is persisted to the RWO PVC `catalyst-api-deployments` (1 Gi, `Recreate` strategy, `fsGroup: 65534`) so a Pod restart mid-Phase-1 doesn't lose the in-flight state — see `internal/store/` (`418cead0`) and the new endpoints `GET /api/v1/deployments/<id>/kubeconfig` + `GET /api/v1/deployments/<id>/events` for replay.
6. **DNS**: PDM `/v1/commit` creates the per-Sovereign PowerDNS zone, writes the canonical 6-record set via PowerDNS REST API, and updates the parent-zone NS delegation via the matching registrar adapter (Dynadot for pool; selected adapter for byo-api; skipped for byo-manual which polls customer-side propagation instead).
7. **TLS** auto-issued via cert-manager DNS-01 against the per-Sovereign PowerDNS zone (cert-manager-webhook-pdns) for wildcard certs, or HTTP-01 fallback.
8. omantel-admin logs into `console.omantel.omani.works`, issues a voucher via `/admin/billing`.
9. Tenant redeems at `omantel.omani.works/redeem?code=...`, creates Org, installs first App.

## Active parallel work

- Group G is closed-out by the new DNS architecture (#167/#163/#168/#170/#171).
- Open follow-ups: #175 (transitive-mandatory cascade UX), #173 (component-card logo render fix), #170/#168/#169/#167/#163/#162/#161/#171 awaiting UAT-to-completed move.

## Resume protocol

1. Read this file.
2. `cd /home/openova/repos/openova && git pull origin main`.
3. `gh issue list --repo openova-io/openova --state open --label area/platform` to see remaining tickets.
4. Dispatch parallel agents on independent tickets via Agent tool with `isolation: "worktree"`.
5. The user's standing instruction: 24-hour-no-stop, ≥5 parallel agents, never violate `INVIOLABLE-PRINCIPLES.md`.

## References

- `docs/INVIOLABLE-PRINCIPLES.md` — non-negotiable rules
- `docs/PROVISIONING-PLAN.md` — canonical 8-phase plan
- `docs/AUDIT-PROCEDURE.md` — on-demand validation
- `docs/RUNBOOK-PROVISIONING.md` — operator-level guide
- `docs/FRANCHISE-MODEL.md` — voucher mechanism
