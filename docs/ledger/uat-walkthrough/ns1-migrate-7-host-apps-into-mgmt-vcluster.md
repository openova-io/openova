# NS#1 — migrate the 7 host-placed apps into the mgmt vCluster (UAT walkthrough)

## Status — format: browser-walk (agreed standard), last revamped 2026-06-17 on hw158

> **Prior curl/kubectl-format runbook REPLACED.** The earlier version drove this walk with
> `curl`/`kubectl`/`git grep` against `/tmp/hw158-kc.yaml` and `placement.yaml` line numbers — banned
> per the agreed UAT standard (**100% browser, no curl, no kubectl, no git, no command output**). This
> revamp rewrites every check as a clickable browser action on the live console, status reset to `☐`
> pending a fresh browser walk.

> **Issue:** #3642 · **North Star #1** (founder, verbatim): *"every app IN a vCluster."*
> **Env:** `console.hw158.omani.works` (deployment `ab2135d4cf2d01e4`, primary region `me-east-215-a`).
> **No prior-env evidence is carried over** (each new env flushes all evidence; an absent / wrong-block
> tile = FAIL, never a carried ✅).
>
> **The single binary headline:** on the **`/dashboard` treemap with LAYER 1 = vCluster**, all 7 named
> apps — **grafana, harbor, keycloak, gitea, openbao, newapi, guacamole** — must each render as a tile
> sitting **under the `mgmt` block, NOT under the `host` block**, alongside loki / mimir / nats / tempo.
> A tile under `host` (or absent from `mgmt`) is a **FAIL** for that app.

---

## The one browser surface this ticket is checked on

The whole migration is provable from a single screen: **`/dashboard` → set LAYER 1 grouping to
`vCluster`**. That regroups the treemap into one block per vCluster (`host` / `mgmt` / `rtz` / `dmz`).
Reading which block each of the 7 app tiles falls into IS the acceptance test — a `mgmt`-block tile = the
app runs inside the mgmt vCluster; a `host`-block tile = it never moved. No terminal, no kubeconfig.

---

## Sign-in (once, zero-click)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/auth/handover](https://console.hw158.omani.works/auth/handover) | Load the handover URL (with the operator's handover token). Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right) — **no login form**. A redirect to a Keycloak login screen here = FAIL. | ☐ | `docs/sessions/2026-06-17/evidence/3642-signin.png` |

---

## PART A — set up the treemap (LAYER 1 = vCluster)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Dashboard renders the cluster treemap and the grouping controls (LAYER 1 / LAYER 2 comboboxes) are visible. Login-screen redirect = FAIL. | ☐ | `docs/sessions/2026-06-17/evidence/3642-dashboard.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Click the **LAYER 1** grouping combobox and select **`vCluster`**. The treemap regroups into one labelled block per vCluster: **`host`**, **`mgmt`** (plus `rtz` / `dmz` if present). The `mgmt` block is visible and clickable. | ☐ | `docs/sessions/2026-06-17/evidence/3642-layer1-vcluster.png` |

---

## PART B — each of the 7 app tiles sits under the `mgmt` block (the decisive check)

With LAYER 1 = vCluster set, read where each app's tile lands. **One row per app — PASS only if the tile
sits inside the `mgmt` block; a tile inside the `host` block is a FAIL.**

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **grafana** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-grafana-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **harbor** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-harbor-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **keycloak** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-keycloak-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **gitea** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-gitea-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **openbao** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-openbao-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **newapi** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-newapi-mgmt.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | On the LAYER1=vCluster treemap, the **guacamole** tile must sit inside the **mgmt** block, **not** the **host** block. | ☐ | `docs/sessions/2026-06-17/evidence/3642-guacamole-mgmt.png` |

---

## PART C — the `mgmt` block's contents + the `host` block is clean

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Click into the **mgmt** block (drill down one LAYER). Its tiles must include **all 7** named apps (grafana / harbor / keycloak / gitea / openbao / newapi / guacamole) **alongside** loki / mimir / nats / tempo. Missing any of the 7 = FAIL. | ☐ | `docs/sessions/2026-06-17/evidence/3642-mgmt-block-contents.png` |
| [console.hw158/dashboard](https://console.hw158.omani.works/dashboard) | Read the **host** block on the same LAYER1=vCluster treemap. **None** of the 7 named apps may appear under `host`. Any of the 7 showing under `host` = FAIL. | ☐ | `docs/sessions/2026-06-17/evidence/3642-host-block-clean.png` |
| [console.hw158/apps/keycloak](https://console.hw158.omani.works/apps/keycloak) | Open the **keycloak** app card and read its placement detail — it must show **`mgmt`** (the per-app placement readout mirrors the treemap block). A `host` readout = FAIL. | ☐ | `docs/sessions/2026-06-17/evidence/3642-keycloak-card-placement.png` |

---

## PART D — every per-app surface still WORKS after the move (no regression)

After the apps move into mgmt, each public surface must still load and land the user **signed in**
(zero-click via the sovereign SSO). A redirect to a standalone Keycloak login form = FAIL; a rendered,
authenticated app screen = ✅.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [auth.hw158/realms/sovereign/account](https://auth.hw158.omani.works/realms/sovereign/account) | The sovereign-realm account console renders for `emrah.baysal@openova.io` (no second login). | ☐ | `docs/sessions/2026-06-17/evidence/3642-keycloak-surface.png` |
| [gitea.hw158](https://gitea.hw158.omani.works/) | Gitea opens **already signed in** (avatar/menu shows the SSO user), repo list renders — no Gitea login form. | ☐ | `docs/sessions/2026-06-17/evidence/3642-gitea-surface.png` |
| [harbor.hw158](https://harbor.hw158.omani.works/) | Harbor opens **signed in**, the projects list renders — no Harbor login form. | ☐ | `docs/sessions/2026-06-17/evidence/3642-harbor-surface.png` |
| [grafana.hw158](https://grafana.hw158.omani.works/) | Grafana opens **signed in** (no Grafana login), the home dashboard renders. | ☐ | `docs/sessions/2026-06-17/evidence/3642-grafana-surface.png` |
| [bao.hw158/ui/](https://bao.hw158.omani.works/ui/) | The OpenBao UI renders **signed in** via OIDC — no manual token/unseal prompt blocking the landing. | ☐ | `docs/sessions/2026-06-17/evidence/3642-openbao-surface.png` |
| [newapi.hw158](https://newapi.hw158.omani.works/) | newapi opens **signed in**, its main console renders — no login form. | ☐ | `docs/sessions/2026-06-17/evidence/3642-newapi-surface.png` |
| [guacamole.hw158/guacamole/](https://guacamole.hw158.omani.works/guacamole/) | Guacamole opens **signed in**, the connections list renders — no Guacamole login form. | ☐ | `docs/sessions/2026-06-17/evidence/3642-guacamole-surface.png` |

---

## GAPS — surfaces with NO browser representation (findings, not test rows)

These are part of the ticket's intent but have **no operator-visible UI surface**, so they are recorded
as `GAP` findings rather than driven via a terminal. Closing them is engineering work; they are not
browser-walkable.

| Surface | Why it's a GAP | Status |
|---|---|---|
| In-vCluster CRD registration inside vc-mgmt (httproutes / externalsecrets / cnpg `clusters` / `poolers` / `scheduledbackups`) — proving the OSS init-manifests deployer registered them **inside** the mgmt vCluster | No console screen exposes the inner-vCluster CRD inventory. Previously checked with `kubectl --kubeconfig <inner-vc-mgmt> get crd` — banned. There is no dashboard widget for "CRDs registered inside vc-mgmt". | GAP |
| Per-app pod-level syncer suffix (`-x-<innerNs>-x-mgmt-vcluster`) on each migrated pod | The treemap block (PART B) is the operator-facing proxy for "runs in mgmt"; the literal pod-name suffix is a host-cluster `kubectl` detail with no UI surface. PART B's mgmt-block placement is the browser-checkable equivalent. | GAP |
| `cutoverComplete=true` survival of the 7 through the Pillar-5 600s deny-egress hold (no `admin.loft.sh` tether) | The deny-egress cutover proof is owned by the **Pillar-5 cutover runbook**, walked on its own `/cutover` (or `/jobs` cutover rows) surface — not duplicated here. | GAP (owned elsewhere) |

---

## DoD roll-up (browser-walk)

- [ ] **Sign-in** — handover lands on `/dashboard` signed-in, no login form. → ☐
- [ ] **PART A** — `/dashboard` renders, LAYER 1 set to `vCluster`, `mgmt` block visible. → ☐ (2 rows)
- [ ] **PART B** — all 7 app tiles (grafana / harbor / keycloak / gitea / openbao / newapi / guacamole) sit under the **mgmt** block on the LAYER1=vCluster treemap. → ☐ (7 rows)
- [ ] **PART C** — the mgmt block holds all 7 + loki/mimir/nats/tempo; the host block holds none of the 7; keycloak card reads `mgmt`. → ☐ (3 rows)
- [ ] **PART D** — all 7 per-app public surfaces still land **signed in** (no regression). → ☐ (7 rows)
- [ ] **GAPS** — 3 non-UI findings recorded (in-vCluster CRD registration, per-pod syncer suffix, cutover deny-egress survival owned by Pillar-5).

**Acceptance = the founder (or operator) walking the clickable rows above on the live env in a browser,
pasting the named screenshot per row.** PART B is the decisive set: every one of the 7 app tiles must
render under the **mgmt** block, never **host**. A login-screen redirect on any surface = FAIL; a
rendered, authenticated screen = ✅.
