# NS#1 — migrate the 7 host-placed apps into the mgmt vCluster (UAT walkthrough)

> **Issue:** #3642 · **North Star #1** (founder, verbatim): *"every app IN a vCluster."*
> **Env to walk:** the CURRENT live prov (today: `console.hw150.omantel.biz`, deployment
> `catalyst-hw150-omantel-biz-1290a8ef`, kubeconfig `/tmp/hw150.kubeconfig`). Re-stamp the env
> id + screenshot prefix to whatever env is live when the walk runs — **no prior-env evidence is
> carried over** (each new env flushes all evidence; absent feature = FAILED).
>
> **The single binary headline:** after this ticket lands, **all 7 named apps — grafana, harbor,
> keycloak, gitea, openbao, newapi, guacamole — run INSIDE the mgmt vCluster** (their pods carry
> the `-x-<innerNs>-x-mgmt-vcluster` syncer suffix), **AND every per-app surface still works**
> (SSO landing, image pull, DNS-admin, console). `placement.yaml` shows `vcluster: mgmt` for all 7
> and `scripts/audit-placement-conformance.py live` exits 0 with zero undeclared host workloads.

---

## Sign-in (once, zero-click)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.hw150.omantel.biz/auth/handover?token=<handover-JWT>` | nothing — just load it | Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right), no login form | ☐ |

---

## PART A — placement is declared `mgmt` for all 7 (the data change)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/blob/main/clusters/_template/bootstrap-kit/placement.yaml` | Search the page for `bp-grafana` | The row reads `vcluster: mgmt, target: mgmt` (was `vcluster: host`) — **no `vcluster != target` backlog gap remains** for grafana | ☐ |
| same page | Search `bp-harbor` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |
| same page | Search `bp-keycloak` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |
| same page | Search `bp-gitea` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |
| same page | Search `bp-openbao` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |
| same page | Search `bp-newapi` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |
| same page | Search `bp-guacamole` | Row reads `vcluster: mgmt, target: mgmt` | ☐ |

> Cross-check (NOT acceptance, appendix): `python3 scripts/render-slot-placement.py check` → exit 0
> (slots are a pure function of placement.yaml — zero hand-edited placement fields).

---

## PART B — the 7 apps RUN in the mgmt vCluster (live pods carry the syncer suffix)

The acceptance surface is the **operator console treemap grouped by vCluster** + the per-app tile
landing in the `mgmt` block. Each row is one UI action.

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.hw150.omantel.biz/dashboard` | Set the **LAYER 1** grouping combobox → `vCluster` | The treemap regroups into four top-level blocks: **`host`**, **`mgmt`**, **`rtz`**, **`dmz`** | ☐ |
| same page | In the **mgmt** block, read the tiles | The mgmt block now contains **`grafana`**, **`harbor`**, **`keycloak`**, **`gitea`**, **`openbao`**, **`newapi`**, **`guacamole`** — ALONGSIDE the already-mgmt `loki`/`mimir`/`tempo`/`nats` | ☐ |
| same page | In the **host** block, look for the 7 app tiles | NONE of grafana/harbor/keycloak/gitea/openbao/newapi/guacamole appear under `host` any more — host carries only substrate (cilium, flux, cnpg-operator, kyverno, the vCluster runtimes, …) | ☐ |
| `https://console.hw150.omantel.biz/apps` | Open the **`keycloak`** card → read its detail | The app detail shows placement **`mgmt`** (vCluster), not host | ☐ |

> Cross-check (NOT acceptance, appendix — run from a shell with the live kubeconfig):
> ```
> for a in grafana harbor keycloak gitea openbao newapi guacamole; do
>   kubectl --kubeconfig /tmp/hw150.kubeconfig get pods -A --no-headers | grep "$a" | grep -c x-mgmt-vcluster
> done
> ```
> Every app returns **≥1** (its pods carry the `-x-<innerNs>-x-mgmt-vcluster` suffix). Before this
> ticket, every app returned **0** (plain host pods).
> `scripts/audit-placement-conformance.py live --kubeconfig /tmp/hw150.kubeconfig` → exit 0
> ("zero undeclared host workloads"; declared == actual).

---

## PART C — every per-app surface still WORKS after the move (no regression)

Re-homing must not break the app's wire. The host-bridge keeps each app's HTTPRoute + ExternalSecret
(+ CNPG Cluster) host-rendered while the pod moves into mgmt; the route's backendRef points at the
syncer-mangled Service. Each row proves the public surface still serves.

| App | Go to (URL) | Then do | You should see | Result |
|---|---|---|---|---|
| **keycloak** | `https://auth.hw150.omantel.biz/realms/sovereign/account` | load it | The Keycloak **sovereign realm** account console renders (the realm is reachable through the host Gateway → mgmt-synced keycloak Service) | ☐ |
| **gitea** | `https://gitea.hw150.omantel.biz/` | load it | Gitea lands **signed in** (zero-click SSO), top-right avatar present — the public route still reaches the in-vCluster gitea | ☐ |
| **harbor** | `https://harbor.hw150.omantel.biz/` | load it | Harbor lands **signed in** (zero-click SSO); the projects list renders | ☐ |
| **harbor (pull path)** | `https://console.hw150.omantel.biz/dashboard` | confirm every workload is **Running** (no `ImagePullBackOff`) | Cluster-wide image pulls still resolve against harbor (registry route intact after the move) | ☐ |
| **grafana** | `https://grafana.hw150.omantel.biz/` | load it | Grafana lands **signed in**; its loki/mimir/tempo datasources (already mgmt) resolve via plain in-vCluster Service DNS | ☐ |
| **openbao** | `https://bao.hw150.omantel.biz/ui/` | load it | OpenBao UI renders; its SSO landing reaches the in-vCluster openbao through the host route | ☐ |
| **newapi** | `https://newapi.hw150.omantel.biz/` | load it | newapi lands **signed in** (zero-click SSO) | ☐ |
| **guacamole** | `https://guacamole.hw150.omantel.biz/guacamole/` | load it | Guacamole lands **signed in**; the connection list renders (k8s-ws-proxy host DaemonSet still tunnels to the in-vCluster guacamole) | ☐ |

> Each "lands signed in" row is the same acceptance bar as #3374 (NS#3): typing the bare app URL →
> already authenticated, no login UI. The move must NOT regress SSO landing.

---

## PART D — the OSS-native CRD-registration mechanism is proven (sovereignty-safe)

The whole reason the 7 stayed host was the loft.sh-Free-tier CRD-sync wall. This ticket lands them
WITHOUT a loft.sh license (a license would break the Pillar-5 cutover deny-egress hold). The proof
that the mechanism is OSS-native:

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/blob/main/platform/bp-mgmt-vcluster/chart/values.yaml` | Search `experimental:` → read the `deploy.vcluster.manifests` block | Structural CRDs for `httproutes.gateway.networking.k8s.io`, `externalsecrets.external-secrets.io`, and the CNPG `clusters/poolers/scheduledbackups` are registered INSIDE vc-mgmt by the OSS `loft-sh/vcluster` init-manifests deployer — **no Pro/Platform license** | ☐ |
| `https://console.hw150.omantel.biz/cutover` (or the /jobs cutover rows) | Confirm the env reached `cutoverComplete=true` previously OR run a fresh cutover | The mgmt-resident 7 apps survive the **600s deny-egress hold** (the env never tethers to `admin.loft.sh`) — sovereignty preserved with all 7 in-vCluster | ☐ |

> Cross-check (NOT acceptance, appendix): from a shell on the in-vCluster apiserver context,
> `kubectl get crd httproutes.gateway.networking.k8s.io externalsecrets.external-secrets.io` →
> both present INSIDE vc-mgmt (the init-manifests registration), proving an in-vcluster app can
> CREATE those kinds without the loft.sh platform connection.

---

## PART E — generality proof (the mechanism is one recipe, not 7 special-cases)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/pull/<PR#>/files` | Diff the 7 slot files (`09-keycloak.yaml`, `10-gitea.yaml`, `19-harbor.yaml`, `25-grafana.yaml`, `08-openbao.yaml`, `80-newapi.yaml`, `52-bp-guacamole.yaml`) | EVERY placement-owned field change is `render-slot-placement.py`-generated from the ONE `vcluster: host → mgmt` flip in placement.yaml + the shared host-bridge values — **zero per-app bespoke placement code** | ☐ |
| same diff | Look for any per-app special-casing in Go / handlers | NONE — the move is pure placement.yaml data + the shared host-bridge slot-render template; no controller change, no per-app conditional | ☐ |

---

## DoD roll-up (all binary)

- [ ] PART A — placement.yaml declares `vcluster: mgmt` for ALL 7 named apps (7 boxes).
- [ ] PART B — the treemap `mgmt` block contains all 7; host carries none of them; live pods carry the syncer suffix.
- [ ] PART C — all 8 per-app public surfaces still serve / land signed-in (no regression).
- [ ] PART D — OSS-native init-manifests CRD registration proven; 600s deny-egress hold survives (no loft.sh tether).
- [ ] PART E — the move is generic (one placement flip + shared host-bridge; zero per-app code).

**Acceptance = the founder walking the clickable rows above on the live env.** Automated checks are
appendix only, demoted per the UAT format law.
