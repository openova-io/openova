# #3373 PLACEMENT — per-app user acceptance walk (web UI)

**The contract (`clusters/_template/bootstrap-kit/placement.yaml`, founder-ratified §4 law):** every application must run in its **target** vCluster. **26 apps target a vCluster** — 17 → `mgmt`, 8 → `rtz`, 1 → `dmz` — and the rest are the **host minimum** (the only things §4 allows to stay on host: CNI, admission, GitOps, the vCluster runtimes themselves, etc.). The migration is **not finished**: most vCluster-targeted apps still render `vcluster: host`. This walk proves placement **per app**, one row each — no collapsing.

**How a user proves placement (100% web UI, no terminal):** open the **[Dashboard](https://console.hw133.omani.works/dashboard)** treemap and set its **Layer 1 combobox to `vCluster`** (the layer options are Sovereign / Region / Cluster / vCluster / Family / Namespace). The treemap then groups every workload under its vCluster — `mgmt-vcluster`, `dmz-vcluster`, `rtz-vcluster`, or `host`. The user reads off which group each app sits in. Cross-check via **[Apps → Deployments](https://console.hw133.omani.works/apps)**: open an app card and read its namespace / placement. **Honest gap:** the per-app detail page may NOT expose a placement field — if so, the treemap vCluster layer **is** the proof surface; the row stays, the method is the treemap.

**Live env:** [hw133.omani.works](https://console.hw133.omani.works/). **Status legend:** `☐` = unwalked (the walk agent fills ✅/❌). Rows pre-marked **❌ expected-fail (still on host)** are apps that `placement.yaml` shows as `target=vcluster` but `now=host` — the founder sees the per-app migration reality up front.

---

## 1. Should be in `mgmt-vcluster` (17 apps)

Each row: open the [Dashboard](https://console.hw133.omani.works/dashboard) treemap → set Layer 1 = **vCluster** → find `<app>` → it must appear under **`mgmt-vcluster`**.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-nats-jetstream` must appear under **mgmt-vcluster** (now=mgmt — should PASS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-loki` must appear under **mgmt-vcluster** (now=mgmt — should PASS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-mimir` must appear under **mgmt-vcluster** (now=mgmt — should PASS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-tempo` must appear under **mgmt-vcluster** (now=mgmt — should PASS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-keycloak` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-catalyst-platform` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-gitea` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-openbao` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-powerdns-admin` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-sso-bridge` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-harbor` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-grafana` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-k8s-ws-proxy` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-guacamole` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-openova-flow-server` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-openova-flow-emitter` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-newapi` must appear under **mgmt-vcluster** — ❌ expected-fail (still on host) | ☐ | |

---

## 2. Should be in `rtz-vcluster` (8 apps)

Each row: open the [Dashboard](https://console.hw133.omani.works/dashboard) treemap → set Layer 1 = **vCluster** → find `<app>` → it must appear under **`rtz-vcluster`**.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-sandbox` must appear under **rtz-vcluster** (now=rtz — should PASS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-postgres-shared` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cnpg-pair` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-postgres-shared-b` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-postgres-shared-c` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-valkey` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-seaweedfs` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-vllm` must appear under **rtz-vcluster** — ❌ expected-fail (still on host) | ☐ | |

---

## 3. Should be in `dmz-vcluster` (1 app)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-coraza` must appear under **dmz-vcluster** (now=dmz — should PASS) | ☐ | |

---

## 4. Host minimum — must stay on `host` (37 apps)

§4 allows only the cluster substrate / admission / CNI / GitOps engine / day-2 plumbing / the vCluster runtimes themselves to remain on host. Each row: open the [Dashboard](https://console.hw133.omani.works/dashboard) treemap → set Layer 1 = **vCluster** → find `<app>` → it must appear under **`host`** (NOT inside any vCluster).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cilium` must appear under **host** (HOST-MINIMUM — CNI substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-gateway-api` must appear under **host** (HOST-MINIMUM — Gateway API CRDs) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cert-manager` must appear under **host** (HOST-MINIMUM — cluster TLS substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-flux` must appear under **host** (HOST-MINIMUM — the GitOps engine itself) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-crossplane` must appear under **host** (HOST-MINIMUM — day-2 infra plumbing) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-sealed-secrets` must appear under **host** (HOST-MINIMUM — secret decryption substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-reflector` must appear under **host** (HOST-MINIMUM — cross-namespace Secret mirror) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-self-sovereign-cutover` must appear under **host** (HOST-MINIMUM — host-credential surgery by design) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-powerdns` must appear under **host** (HOST-MINIMUM — authoritative DNS substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-external-dns` must appear under **host** (HOST-MINIMUM — DNS reconciler substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-crossplane-claims` must appear under **host** (HOST-MINIMUM — Crossplane claims plumbing) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-oidc-gate` must appear under **host** (HOST-MINIMUM — auth proxy on the gateway datapath) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-external-secrets` must appear under **host** (HOST-MINIMUM — ESO substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-external-secrets-stores` must appear under **host** (HOST-MINIMUM — ESO stores substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cnpg` must appear under **host** (HOST-MINIMUM — CNPG operator substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-opentelemetry-operator` must appear under **host** (HOST-MINIMUM — observability operator substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-opentelemetry` must appear under **host** (HOST-MINIMUM — observability collector substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-alloy` must appear under **host** (HOST-MINIMUM — node-level telemetry DaemonSet) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-network-policies` must appear under **host** (HOST-MINIMUM — cluster network policy substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-kyverno` must appear under **host** (HOST-MINIMUM — admission substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-kyverno-policies` must appear under **host** (HOST-MINIMUM — admission policies) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-reloader` must appear under **host** (HOST-MINIMUM — rollout substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-vpa` must appear under **host** (HOST-MINIMUM — autoscaling substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-trivy` must appear under **host** (HOST-MINIMUM — image-scanning substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-falco` must appear under **host** (HOST-MINIMUM — kernel-level runtime security) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-sigstore` must appear under **host** (HOST-MINIMUM — signature verification substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-syft-grype` must appear under **host** (HOST-MINIMUM — SBOM substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-velero` must appear under **host** (HOST-MINIMUM — cluster backup substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-velero-hcs` must appear under **host** (HOST-MINIMUM — cluster backup substrate, HCS) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cert-manager-powerdns-webhook` must appear under **host** (HOST-MINIMUM — ACME DNS01 webhook substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-cluster-autoscaler-hcloud` must appear under **host** (HOST-MINIMUM — node autoscaling substrate) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-dmz-vcluster` must appear under **host** (HOST-MINIMUM — the DMZ vCluster runtime itself) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-hcloud-ccm` must appear under **host** (HOST-MINIMUM — cloud controller manager) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-mgmt-vcluster` must appear under **host** (HOST-MINIMUM — the MGMT vCluster runtime itself) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-rtz-vcluster` must appear under **host** (HOST-MINIMUM — the RTZ vCluster runtime itself) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-vcluster-helmrepo` must appear under **host** (HOST-MINIMUM — vCluster chart source) | ☐ | |
| [Dashboard](https://console.hw133.omani.works/dashboard) | `bp-continuum` must appear under **host** (HOST-MINIMUM — DR/switchover controller, host reach by design) | ☐ | |

---

**Summary:** **6 of 26** vCluster-targeted apps are actually in their vCluster (nats-jetstream, loki, mimir, tempo → mgmt; sandbox → rtz; coraza → dmz); the remaining **20** are still on host (migration incomplete). All **37** host-minimum apps must stay on host.
