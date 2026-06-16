# UAT walkthrough — TOPOLOGY/DR: one vocabulary, built end-to-end, region-kill proven

**Issue:** #3375
**Slug:** `topology-dr-one-vocabulary-built-and-region-kill-proven`
**Env under test:** a fresh 2-region active-hot-standby Sovereign (`console.<fqdn>`, kubeconfig of the primary region). Where a step needs a deliberately-broken case, it is provisioned separately ("capped-region" Sovereign — region-B intentionally not provisioned).
**Acceptance rule:** the founder walks every row below. Each row is ONE action. A box is ticked only when the "You should see" column is observed LIVE on the current env (per memory: each new env flushes all prior evidence — no carried ✅). Automated `go test`/`kubectl` rows are the proof, but the *acceptance* is the founder observing the screens.

This walkthrough covers EVERY scenario folded into #3375: one-vocabulary create/strip (was #3648), one placement model with `replicas:0` on the fanned-out passive HR (was #3675), per-app Continuum CRs + a generic stateless promoter (was #3666), the journey-built 2-region pair (was #3680), the live-DR UI honesty (was #3684), the region-absence integrity gate (was #3688), the cross-region `-mesh-rw` / openbao-snapshot / guacd wiring on a 2-region env (was #3629), and the full region-kill within the agreement RTO/RPO (Pillar 3 / D31).

---

## Section A — ONE vocabulary, end-to-end (was #3648 / #3675 UI)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| A1 | `https://console.<fqdn>/catalog/bp-postgres` | click **New instance** → topology `<select>` | options are exactly the canonical set the blueprint supports — `singleton`, `active-hot-standby` (NOT `single-region`/`active-hotstandby`) | ☐ |
| A2 | same | pick `active-hot-standby`, name it, **Provision** | the create succeeds — NO `topology "active-hotstandby" not in supported [...]` (invalid-topology) error | ☐ |
| A3 | `https://console.<fqdn>/catalog/bp-grafana` → New instance | pick `active-hot-standby`, provision | succeeds, no invalid-topology error | ☐ |
| A4 | `https://console.<fqdn>/catalog/bp-gitea` → New instance | pick `active-hot-standby`, provision | succeeds, no invalid-topology error | ☐ |
| A5 | `https://console.<fqdn>/app/grafana` → **Topology** tab | read the placement strip vs the page header | strip offers exactly grafana's `SupportedTopologies`; the header "Effective / Supported" matches the strip — NO contradiction, NO `single-region/active-active/active-hotstandby` strip against an `active-hot-standby·singleton` header | ☐ |
| A6 | `https://console.<fqdn>/app/openbao` → Topology tab | open the mode picker | `active-passive` **is selectable** (today `ALL_MODES` omits it) | ☐ |
| A7 | `https://console.<fqdn>/app/cilium` → Topology tab | open the mode picker | `singleton` is selectable; the editor no longer hides two of the four canonical classes | ☐ |
| A8 | terminal | `grep -rn "active-hotstandby\|'single-region'" products/catalyst/bootstrap/ui/src/lib/fleet.api.ts` and the create wizard | every topology POST site routes through the canonicaliser — no raw divergent value posted (audit proof) | ☐ |

---

## Section B — ONE placement model: standby `replicas:0` reaches the fanned-out HRs (was #3675)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| B1 | terminal (primary kubeconfig) | `kubectl get hr -A -l catalyst.openova.io/app=grafana` | the mgmt-A (active) HR AND the mgmt-B (passive) HR both present | ☐ |
| B2 | terminal | `kubectl get hr -A -l catalyst.openova.io/app=grafana -o yaml \| grep -A2 -i replicas` | the mgmt-B **passive** HR carries `replicas: 0` in its values; the mgmt-A active HR carries N (≥1) — NOT byte-identical hot copies | ☐ |
| B3 | terminal | inspect the Gitea catalog repo manifests for grafana | exactly **ONE** manifest set per Application — no parallel Axis-B per-region manifest set | ☐ |
| B4 | terminal | `grep -rn "placementSchema" core/` | **zero** controller consumers (only a deprecated-field shim if kept) — `EffectiveDefault` reads `topology.defaults` | ☐ |
| B5 | terminal | repeat B2 for a STATELESS app (`sso-bridge`) and `git diff` the fan-out change | the passive copy is scaled per its blueprint with **zero per-app code** — the scale-down keys on the role label, not the app name (generality) | ☐ |

---

## Section C — Per-app Continuum CRs + generic stateless promoter (was #3666)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| C1 | terminal | `kubectl get continuums.dr.openova.io -A` | **one Continuum CR per DR-capable Application** — `dr-grafana` AND `dr-sso-bridge` present alongside the cnpg-pair CR — NOT one total | ☐ |
| C2 | `https://console.<fqdn>/app/grafana` → Topology → **Switchover** | click | a real promotion result `applied:true` — NOT `"no live 2-region cnpg-pair backing"` 404 | ☐ |
| C3 | `https://console.<fqdn>/app/sso-bridge` → Topology → **Switchover** | click | `applied:true` via the **stateless promoter** (HTTPRoute weight-flip + DNS flip + lease swap, no state step) | ☐ |
| C4 | terminal | `git diff` the Promoter enum + the engine | the new `stateless` promoter is registered generically — **zero app-name literal** in `promoter.go`/`sequence.go` (generality proof) | ☐ |
| C5 | terminal | `grep -n "backingServices\|replication.backend" platform/grafana/blueprint.yaml` + run the blueprint validator on a deliberately-decorative blueprint | a `replication.backend: cnpg-pair` with NO `backingServices[]` of `type: postgres` and no cnpg-pair template is **rejected** by validation — the decorative-declaration hole is closed | ☐ |
| C6 | `https://console.<fqdn>/app/cilium` (singleton) → Topology tab | read | **no** Switchover button; "no cross-region failover" — the DR section does not render for a singleton | ☐ |

---

## Section D — The app journey BUILDS the 2-region pair (was #3680)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| D1 | `https://console.<fqdn>/catalog/bp-grafana` | **New instance** → select `active-hot-standby` → **Provision** | the wizard auto-creates the backing as its OWN card (per #3370); the consumer's Dependencies tab reads `Depends on: shared-pg (active-hot-standby) / db:grafana` | ☐ |
| D2 | `https://console.<fqdn>/apps` | — | the grafana card AND its backing postgres card, the backing showing an active-hot-standby badge | ☐ |
| D3 | terminal | `kubectl get cluster.postgresql.cnpg.io -A \| grep <grafana-backing>` | a **primary** Cluster (region-A) AND a **replica** Cluster (region-B) — a real 2-region pair, not a single-region Database | ☐ |
| D4 | terminal | `kubectl get cluster <replica> -o jsonpath='{.spec.externalClusters}'` and `psql -c "select * from pg_stat_wal_receiver"` on the replica | `externalClusters` non-empty AND a live WAL stream from the primary | ☐ |
| D5 | terminal | `kubectl get continuums.dr.openova.io -A \| grep <grafana>` | a Continuum CR with `applicationRef=<grafana>` and `cnpgPair` pointing at the generated pair | ☐ |
| D6 | `https://console.<fqdn>/catalog/bp-grafana` → New instance → **single-region** | provision a second instance single-region | `kubectl get cluster.postgresql.cnpg.io` for it shows a **single-region Database** (no pair) — the topology choice genuinely drives the backing shape | ☐ |
| D7 | terminal | provision a DIFFERENT postgres app hot-standby; `git diff` | it produces its OWN pair + Continuum with **zero per-app code** — topology propagates from the consumer's resolved variant, keyed on no app name (generality) | ☐ |

---

## Section E — UI mirrors LIVE DR state, never a build-time constant (was #3684)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| E1 | `https://console.<fqdn>/app/grafana` → Topology tab, on a prov where grafana has **no** live pair yet | read the DR section | the honest **"Declared active-hot-standby — no live DR backing on this Sovereign. Switchover unavailable…"** state — and **NO armed Switchover button** (today it shows an armed button that 404s) | ☐ |
| E2 | same | try to click Switchover | there is no clickable Switchover when no backing is live | ☐ |
| E3 | (after D3 provisions a real pair) `https://console.<fqdn>/app/grafana` → Topology | read the DR section | the **live** Continuum status, a **live** replication-lag number (seconds), and an **armed** Switchover | ☐ |
| E4 | same | confirm replication lag | a live numeric value (or "no replica" when none) — never the hardcoded `—` | ☐ |
| E5 | terminal | `git diff` TopologyTab | `showDR`/`hasSwitchover` gate on the live API response; the `continuumName` is the REAL CR name (or "none"→no button); **zero `TOPOLOGY_BY_ID`-driven render of the button** (generality) | ☐ |

---

## Section F — Integrity gate REFUSES a half-built topology (was #3688)

> This section uses a **separate, deliberately-broken Sovereign**: requested active-hot-standby with region-B capped so it never provisions. (Capture its cloud-init log first per DEBUG-BEFORE-WIPE.)

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| F1 | mothership wizard | request a Sovereign, pick **active-hot-standby**, 2 regions, but cap region-B so it cannot come up | provisioning starts one pass | ☐ |
| F2 | provisioning watch / deployment record | wait for the watch to resolve | the record is **`failed`, NOT `ready`**, with reason "standby region not provisioned" — the phase-1 watch log line names region-B missing | ☐ |
| F3 | terminal (capped Sovereign) | `kubectl get continuums.dr.openova.io -A` | the Continuum CR is **absent** OR `Degraded` with `reason=standby-region-absent` (not an empty message) | ☐ |
| F4 | `https://console.<capped-fqdn>/settings` (or Sovereign Topology) | read | a **red banner**: "Active-hot-standby requested; standby region `…-b` was not provisioned. Disaster-recovery is INACTIVE. This Sovereign is running single-region." — NEVER a green active-hot-standby badge | ☐ |
| F5 | `https://console.<capped-fqdn>/app/grafana` → Topology | read the Switchover control | the Switchover button is **disabled** with the region-missing reason — never armed against a phantom region | ☐ |
| F6 | `https://console.<capped-fqdn>/app/keycloak` → Topology | read | the SAME disabled Switchover + the same honest state on a SECOND agreed app — `git diff` shows the gate keys on `bcp_topology` + region presence + Continuum phase with **zero app-name literal** (generality) | ☐ |
| F7 | `https://console.<capped-fqdn>/cloud` | read region count | `Cluster 1/1` (the TRUE count) — no phantom region-B bubble | ☐ |
| F8 | a fresh **single-region** prov (control case) | `kubectl get hr -n flux-system bp-cnpg-pair -o jsonpath='{.spec.values.cnpgPair.enabled}'` and `kubectl get continuums.dr.openova.io -A` | `enabled=false` AND zero Continuum CRs — single-region renders zero DR (safe-default re-proven) | ☐ |

---

## Section G — Cross-region data wiring works on a 2-region env (was #3629)

> Run on the healthy 2-region Sovereign, against the secondary-region (region-b) workloads.

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| G1 | terminal (region-b) | `kubectl get pods -n grafana -o wide` and the grafana log | grafana **Running** (1/1) — no `lookup shared-pg-b-rw.shared-data.svc.cluster.local … no such host` crashloop; it resolves the write host via `<instance>-mesh-rw` | ☐ |
| G2 | terminal (region-b) | `kubectl get pods -n powerdns-admin` + log | powerdns-admin **Running** — no `could not translate host name "shared-pg-b-rw…"`; the CNPG-minted `uri` host is rebuilt onto `-mesh-rw` | ☐ |
| G3 | terminal (region-b) | `kubectl logs keycloak-0 -n keycloak \| grep -i JGroups` | no `java.net.UnknownHostException: shared-pg-rw.shared-data…` — keycloak's background DB host resolves | ☐ |
| G4 | terminal | `kubectl get svc -A \| grep -- -mesh-rw` and `kubectl get svc <instance>-mesh-rw -o yaml \| grep selectorType` | a **write-capable** (`selectorType: rw`) ClusterMesh-global `<instance>-mesh-rw` Service exists in BOTH regions | ☐ |
| G5 | terminal | `kubectl get jobs -n openbao -A \| grep snapshot` (both regions) | `openbao-snapshot-save-*` (region-a) + `openbao-snapshot-fetch-*` (region-b) **Complete** — no `CreateContainerConfigError` (the `seaweedfs-s3-secret` is mirrored into ns `openbao`) | ☐ |
| G6 | terminal (both regions) | `kubectl get pods -n catalyst-system \| grep guacd` | guacd **Running** in both regions — no `persistentvolumeclaim "guacamole-recordings" not found` (emptyDir fallback when `recordings.persistence=false`) | ☐ |

---

## Section H — Region-kill proven WITHIN the agreement RTO/RPO (Pillar 3 / D31, RTO ≤ 30 s, zero loss)

> The capstone. Real region kill (instance destroy or NetworkPolicy isolation per `docs/DOD.md` D31 §6) — NOT a Pod restart or a Deployment scale-down.

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| H1 | terminal | start the D31 counter-writer against the proven postgres app's `<instance>-mesh-rw` write endpoint (monotonic id insert loop) | ids increment, committed against the primary (region-A) | ☐ |
| H2 | `https://console.<fqdn>/app/grafana` → Topology | read before the kill | live Continuum `Ready`, lease held by region-A, standby region present | ☐ |
| H3 | terminal | **kill region-A** (the real region kill per D31 §6) | region-A unreachable within ~5 s | ☐ |
| H4 | terminal (counter-writer) | observe | switchover completes **≤ 30 s** (kill → counter writer reconnects to the new primary); the replica is promoted; `id = last_id + 1`, **zero gap** (zero transactions lost) | ☐ |
| H5 | `https://console.<fqdn>/app/grafana` → Topology | read after switchover | primary is now region-B; a switchover audit event is present; the app is reachable on its FQDN | ☐ |
| H6 | `https://console.<fqdn>/app/keycloak` | hit `auth.<fqdn>` after the kill | the keycloak realm/sessions survive the region-A kill — a second agreed app proven (generality across the agreed set) | ☐ |
| H7 | terminal | rejoin region-A | recovery completes without split-brain (one primary, the rejoined region becomes follower) — walked once | ☐ |

---

## Evidence index

Every ticked box above is backed by a screenshot committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`. Automated proofs (`go test -race` on `core/controllers/{continuum,application,pkg/backingservice}/…` + the phase-1 watch module; `vitest` on `TopologyTab`/`DRSection`/`TopologyEditor`; render/golden tests for the `replicas:0` passive HR, the generated pair + per-app Continuum, the `-mesh-rw` Service) are the appendix — they are automated, NOT the acceptance. Acceptance is the founder walking Sections A–H on the live env.

**Generality is proven, not asserted:** every generality row (B5, C4, D7, E5, F6, H6) carries a `git diff` showing zero app-name literal in the changed engine/gate/UI path, plus a second-app live demonstration. A mechanism that works for grafana but is hardcoded to it fails the ticket (founder rule #4).
