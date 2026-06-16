# NS#1 (#3642) — migrate the 7 host-placed apps into the mgmt vCluster — UAT walk

> **Env: `<re-stamp the CURRENT live env, e.g. hw151.omantel.biz>` · deployment `<id>` · `<date>`.**
> No prior-env evidence carries over — every ✅ below MUST trace to a `<env>-*` screenshot under
> `docs/sessions/<date>/evidence/`. Run on a **fresh, zero-touch prov** (the placement flip + the
> host-bridge slot render must reconcile hands-off).

**North Star #1 (founder, verbatim, 2026-06-12):** *"every app IN a vCluster."*

**What this ticket lands (#3642):** the **OSS-native HOST-BRIDGE** — the generic mechanism that
moves a route/secret/CNPG app INTO a vCluster **without a loft.sh license** (so the Pillar-5 cutover
600s deny-egress hold still passes). The app's HTTPRoute + ExternalSecret (+ CNPG `Cluster`) stay
**host-rendered** (declared as data under the slot's `hostBridge:` in `placement.yaml`, transcribed
into the slot by `scripts/render-slot-placement.py`); only the workload Deployment/StatefulSet
re-homes into the vCluster via `spec.kubeConfig.secretRef → vc-mgmt`. `backendRef`s point at the
OSS-synced syncer-mangled Service `<svc>-x-<innerNs>-x-mgmt-vcluster`.

**This PR's proven set:** **keycloak (09)** + **grafana (25)** are flipped to `vcluster: mgmt` with a
`hostBridge:` block and MOVE into the mgmt vCluster. keycloak proves the **route-only** class (with a
bare-URL redirect filter, no ExternalSecret, bundled PG in-vCluster); grafana proves the
**route + ExternalSecret** class. The remaining 5 (openbao route+secret; harbor/gitea/newapi/guacamole
route+secret+CNPG) are host-bridge-READY follow-on rows — the SAME one-field flip + `hostBridge:`
declaration (see `placement.yaml` §4 RESOLUTION block + PR body).

**Sign-in (once):** open the handover URL `https://console.<env>/auth/handover?token=<JWT>`
→ lands `/dashboard` signed in as the owner, no login form.

---

## PART A — placement declares `vcluster: mgmt` (data, zero hand-edits)

| Go to (URL / cmd) | Then do | You should see | Result |
|---|---|---|---|
| `clusters/_template/bootstrap-kit/placement.yaml` | `git diff` the keycloak (09) + grafana (25) rows | both flipped `vcluster: host → mgmt`, each carrying a `hostBridge:` block | ☐ |
| terminal | `python3 scripts/render-slot-placement.py check` | exits **0** — "placement check PASSED … 11 in vClusters" (slots regenerated, zero hand-edits) | ☐ |
| terminal | `python3 scripts/tests/render_slot_placement_hostbridge_test.py` | "host-bridge generator render-test PASSED" (11 checks) | ☐ |

## PART B — the apps RUN in the mgmt vCluster (treemap + live syncer suffix)

| Go to (URL / cmd) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<env>/dashboard` | set **LAYER 1** combobox → `vCluster` | treemap regroups into `host` / `mgmt` / `rtz` / `dmz` blocks | ☐ |
| `https://console.<env>/dashboard` | read the **mgmt** block | the mgmt block now contains **`keycloak`** + **`grafana`** tiles (alongside loki/mimir/tempo/nats) | ☐ |
| `https://console.<env>/dashboard` | read the **host** block | the host block contains **neither keycloak nor grafana** (only substrate + the not-yet-moved 5) | ☐ |
| `kubectl --kubeconfig <kc> get pods -A \| grep keycloak` | — | `keycloak-0-x-keycloak-x-mgmt-vcluster` (was 0 before) | ☐ |
| `kubectl --kubeconfig <kc> get pods -A \| grep grafana` | — | `grafana-*-x-grafana-x-mgmt-vcluster` (was 0 before) | ☐ |
| terminal | `python3 scripts/audit-placement-conformance.py live --kubeconfig <kc>` | exits **0** — "zero undeclared host workloads"; keycloak/grafana report `mgmt` | ☐ |
| `https://console.<env>/apps/<keycloak-id>` | open keycloak's app detail | placement reads **`mgmt`** (vCluster), not host | ☐ |
| `https://console.<env>/apps/<grafana-id>` | open grafana's app detail | placement reads **`mgmt`** (vCluster), not host | ☐ |

## PART C — public surfaces still serve (host-bridge route intact)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://auth.<env>/` | open in a fresh browser | 302 → `/admin/<realm>/console/`, the SOVEREIGN-realm Keycloak admin console (silent catalyst-pin, no master-realm login form) — served via the host-bridged HTTPRoute → `keycloak-x-keycloak-x-mgmt-vcluster` | ☐ |
| `https://auth.<env>/realms/<realm>/.well-known/openid-configuration` | — | 200, the OIDC discovery doc (the IdP every SSO client uses is reachable post-move) | ☐ |
| `https://grafana.<env>/` | open in a fresh browser | lands **signed in** (zero-click generic_oauth via the host-bridged ExternalSecret `grafana-sso-oidc-credentials`), owner is GrafanaAdmin — NOT a login form, NOT a 503 "no healthy upstream" | ☐ |
| `kubectl --kubeconfig <kc> get pods -A \| grep -i imagepull` | — | no `ImagePullBackOff` cluster-wide (harbor — not yet moved — still serves pulls; the keycloak/grafana move did not regress the registry) | ☐ |

## PART D — OSS-native + sovereignty-safe (no loft.sh tether)

| Go to (URL / cmd) | Then do | You should see | Result |
|---|---|---|---|
| `kubectl --kubeconfig <kc> -n mgmt get crd httproutes.gateway.networking.k8s.io -o yaml` (in-vCluster) | — | the structural CRD registered by the init-manifests deployer (`experimental.deploy.vcluster.manifests`), annotation `catalyst.openova.io/managed-by: bp-mgmt-vcluster` — NOT a loft.sh license | ☐ |
| `kubectl --kubeconfig <kc> get ccnp,netpol -A \| grep loft` | — | **no** `admin.loft.sh` egress tether anywhere | ☐ |
| `https://console.<env>/dashboard` (or the cutover ledger) | drive the cutover to completion | `cutoverComplete=true` AND the **600s deny-egress hold passes** with keycloak + grafana in-vCluster (no `admin.loft.sh` tether breaks it) | ☐ |

## PART E — generality (one flip + one shared template, zero per-app code)

| Go to (URL / cmd) | Then do | You should see | Result |
|---|---|---|---|
| terminal | `git diff clusters/_template/bootstrap-kit/09-keycloak.yaml clusters/_template/bootstrap-kit/25-grafana.yaml` | EVERY placement-owned change (HR ns→mgmt, kubeConfig→vc-mgmt, storageNamespace, createNamespace, the bp-mgmt-vcluster runtime edge, the suppress values, the `HOST-BRIDGE BEGIN…END` block) is **`render-slot-placement.py`-generated** from the placement flip — **zero per-app bespoke Go/slot code** | ☐ |
| terminal | `git grep -n "host-bridge" scripts/render-slot-placement.py` | the host-bridge is ONE generic generator function (`hostbridge_block` / `effective_values`), driven by `placement.yaml hostBridge:` data — not a per-app branch | ☐ |
| `placement.yaml` §4 RESOLUTION | read the follow-on enumeration | the same recipe moves openbao + harbor + gitea + newapi + guacamole + catalyst-platform — a one-field flip + a `hostBridge:` declaration each | ☐ |

---

## Scope note (honest status)

- **MOVED this PR:** keycloak (09, route-only) + grafana (25, route + ExternalSecret) — the
  route/secret class proven end-to-end via the generic host-bridge.
- **Follow-on (host-bridge-READY, same recipe):** openbao (08, adds a 2-backend bare-URL landing
  route), powerdns-admin (11a), openova-flow-server (56); and the CNPG class — gitea (10), harbor
  (19), newapi (80), guacamole (52), catalyst-platform (13, LAST) — which additionally host-render
  their CNPG `Cluster` CR (§5d) and wire the in-vCluster Pod to the host PostgreSQL cross-cluster.
- **grafana SSO Secret reach:** the host ESO materialises `grafana-sso-oidc-credentials` host-side;
  the in-vCluster grafana Pod consumes it via the mgmt-vcluster Secret sync. If PART C grafana
  zero-click does not land on the first walk, confirm the host→vCluster Secret sync mapping (the one
  named follow-up gap on the route+secret class).
