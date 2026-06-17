# NS#1 — migrate the 7 host-placed apps into the mgmt vCluster (UAT walkthrough)

## Status — last validated: hw158 (2026-06-17)

- **Verdict: ❌ NOT migrated as specified on hw158 — honest gap.** Re-walked live on **hw158** (dep `ab2135d4cf2d01e4`, primary region `me-east-215-a`). **Tally: 7 ✅ / 18 ❌ / 4 N/A.** The 7 named apps (grafana / harbor / keycloak / gitea / openbao / newapi / guacamole) are **still plain HOST pods** — the syncer-suffix (`-x-<innerNs>-x-mgmt-vcluster`) count is **0 for every one of them**, and the mgmt vCluster holds only `loki / mimir / nats / tempo` (+ coredns). The `placement.yaml` rows still read **`vcluster: host, target: mgmt`** — the `host → mgmt` flip was **never done**; all 7 are in `render-slot-placement.py check`'s **"staged for promotion"** list.
- **The masking trap is live:** `scripts/audit-placement-conformance.py live` exits **0** ("zero undeclared host workloads") **only because it ratifies the 7 as host-conformant** (declared `vcluster: host` == actually-on-host). "Audit passes" is **NOT** "the 7 are in mgmt." Only `loki/mimir/nats/tempo` show `mgmt` in the audit.
- **What DID verify ✅:** PART A's appendix `render-slot-placement.py check` runs clean (no hand-edited placement fields); PART D's init-manifests mechanism is partially proven — **3 of 5** declared CRDs (`httproutes`, `externalsecrets`, `clusters.postgresql.cnpg.io`) are registered INSIDE vc-mgmt by the OSS `loft-sh/vcluster:0.21.0` deployer (no Pro license); PART E's generator (`render-slot-placement.py`) has **zero** per-app special-casing (grep count 0 across all 7 app names).
- **What's still needed:** the actual `vcluster: host → mgmt` flip in `placement.yaml` for all 7 + a fresh prov, after which PART B (7 tiles land in the **mgmt** treemap block + the syncer-suffix `kubectl` cross-check returns ≥1) becomes walkable. Until then this runbook is the reusable click-path, not banked migration evidence. (Minor follow-up: `poolers` + `scheduledbackups` CNPG CRDs are declared in the chart but NOT registered inside vc-mgmt — only 3/5 landed.)
- **Maps to:** [`../UAT.md`](../UAT.md) **Row 7** (placement — host-conformant, NOT the migration proof).
- **Index:** [`README.md`](README.md).

> **Issue:** #3642 · **North Star #1** (founder, verbatim): *"every app IN a vCluster."*
> **Env walked:** `console.hw158.omani.works`, deployment `ab2135d4cf2d01e4`,
> primary-region kubeconfig `/tmp/hw158-kc.yaml`. **No prior-env evidence is carried over**
> (each new env flushes all evidence; absent feature = FAILED).
>
> **The single binary headline:** after this ticket lands, **all 7 named apps — grafana, harbor,
> keycloak, gitea, openbao, newapi, guacamole — run INSIDE the mgmt vCluster** (their pods carry
> the `-x-<innerNs>-x-mgmt-vcluster` syncer suffix), **AND every per-app surface still works**
> (SSO landing, image pull, DNS-admin, console). `placement.yaml` shows `vcluster: mgmt` for all 7
> and `scripts/audit-placement-conformance.py live` exits 0 with zero undeclared host workloads.
> **On hw158 this headline is FALSE: all 7 are host pods, placement still says `vcluster: host`.**

---

## Sign-in (once, zero-click)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.hw158.omani.works/auth/handover?token=<handover-JWT>` | nothing — just load it | Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right), no login form | N/A — browser-only step; this walk is curl/kubectl-only per the harness constraint. Console reachability spot-checked below in PART C. |

---

## PART A — placement is declared `mgmt` for all 7 (the data change)

**Live read of `placement.yaml` (the same file GitHub serves):**

```
$ grep -nE 'bp-(grafana|harbor|keycloak|gitea|openbao|newapi|guacamole)' \
    clusters/_template/bootstrap-kit/placement.yaml
166:    - {file: 09-keycloak.yaml,    hr: bp-keycloak,  vcluster: host, target: mgmt, namespace: keycloak}
173:    - {file: 10-gitea.yaml,       hr: bp-gitea,     vcluster: host, target: mgmt, namespace: gitea}
249:    - {file: 08-openbao.yaml,     hr: bp-openbao,   vcluster: host, target: mgmt, namespace: openbao}
259:    - {file: 19-harbor.yaml,      hr: bp-harbor,    vcluster: host, target: mgmt, namespace: harbor}
260:    - {file: 25-grafana.yaml,     hr: bp-grafana,   vcluster: host, target: mgmt, namespace: grafana}
264:    - {file: 52-bp-guacamole.yaml, hr: bp-guacamole, vcluster: host, target: mgmt, namespace: catalyst-system}
267:    - {file: 80-newapi.yaml,      hr: bp-newapi,    vcluster: host, target: mgmt, namespace: newapi}
```

Every row reads `vcluster: host` — the `target: mgmt` is only the *staged-for-promotion intent*, NOT
the active placement. The runbook expects `vcluster: mgmt, target: mgmt`. **The flip was never done.**

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/blob/main/clusters/_template/bootstrap-kit/placement.yaml` | Search the page for `bp-grafana` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — reads `vcluster: host, target: mgmt` (line 260). `vcluster != target` backlog gap REMAINS. |
| same page | Search `bp-harbor` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 259). |
| same page | Search `bp-keycloak` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 166). |
| same page | Search `bp-gitea` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 173). |
| same page | Search `bp-openbao` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 249). |
| same page | Search `bp-newapi` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 267). |
| same page | Search `bp-guacamole` | Row reads `vcluster: mgmt, target: mgmt` | ❌ — `vcluster: host, target: mgmt` (line 264). |

> Cross-check appendix (NOT acceptance):
> ```
> $ python3 scripts/render-slot-placement.py check
> placement check PASSED — 63 slots, 9 in vClusters, 17 staged for promotion (target != active):
>   bp-keycloak, bp-catalyst-platform, bp-gitea, bp-openbao, bp-powerdns-admin, bp-sso-bridge,
>   bp-postgres-shared, bp-cnpg-pair, bp-postgres-shared-b, bp-postgres-shared-c, bp-harbor,
>   bp-grafana, bp-k8s-ws-proxy, bp-guacamole, bp-openova-flow-server, bp-openova-flow-emitter, bp-newapi
> EXIT=0
> ```
> ✅ exits 0 — slots are a pure function of placement.yaml (zero hand-edited placement fields). **But
> all 7 named apps appear in the "staged for promotion" list** (`render-slot-placement.py` lines 197-199:
> `gap = [s["hr"] ... if vcluster != target]`), i.e. declared host, intended mgmt — confirming the flip
> is pending. The appendix passing does NOT mean the 7 are in mgmt.

---

## PART B — the 7 apps RUN in the mgmt vCluster (live pods carry the syncer suffix)

**Live cross-check — syncer-suffix pod count per app (the decisive evidence):**

```
$ for a in grafana harbor keycloak gitea openbao newapi guacamole; do
    echo "$a: $(kubectl --kubeconfig /tmp/hw158-kc.yaml get pods -A --no-headers \
                 | grep "$a" | grep -c x-mgmt-vcluster)"
  done
grafana: 0
harbor: 0
keycloak: 0
gitea: 0
openbao: 0
newapi: 0
guacamole: 0
```

**Every app returns 0** — NOT one of the 7 carries the `-x-<innerNs>-x-mgmt-vcluster` suffix. They run
as plain HOST pods in their own host namespaces:

```
$ for a in grafana harbor keycloak gitea openbao newapi; do kubectl ... get pods -n $a; done
   grafana ns:  grafana-5c88cb955c-gbtwj                      Running
   harbor  ns:  harbor-core-…  harbor-jobservice-…  harbor-nginx-…  harbor-portal-…  harbor-registry-…  Running
   keycloak ns: keycloak-0                                    Running
   gitea   ns:  gitea-dd5d7655c-s96g8                         Running
   openbao ns:  openbao-0  openbao-agent-injector-…  openbao-sso-configure-…  openbao-sso-landing-…  Running
   newapi  ns:  newapi-bp-newapi-…  newapi-bp-newapi-newapi-pg-1/2  Running
$ kubectl ... get pods -n catalyst-system | grep guac
   guacamole-server-7b47c8ff87-k9cl8   Running   guacd-84b4695f59-vfcnc   Running   guacamole-pg-1   Running
```

**What actually IS in the mgmt vCluster** (the only `x-mgmt-vcluster` pods):

```
$ kubectl ... get pods -A | grep x-mgmt-vcluster
   coredns-…-x-kube-system-x-mgmt-vcluster
   loki-0-x-loki-x-mgmt-vcluster
   mimir-{alertmanager,compactor,distributor,gateway,ingester,minio,querier,…}-x-mimir-x-mgmt-vcluster
   nats-jetstream-{0,1,2,…}-x-nats-system-x-mgmt-vcluster
   tempo-0-x-tempo-x-mgmt-vcluster
```

Only `loki / mimir / nats / tempo` (+ coredns). None of the 7 named apps.

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.hw158.omani.works/dashboard` | Set the **LAYER 1** grouping combobox → `vCluster` | Treemap regroups into `host` / `mgmt` / `rtz` / `dmz` | N/A — browser-only (curl/kubectl harness). The underlying placement data, read directly above/below, is what the treemap renders. |
| same page | In the **mgmt** block, read the tiles | mgmt block contains grafana / harbor / keycloak / gitea / openbao / newapi / guacamole ALONGSIDE loki/mimir/tempo/nats | ❌ (proven via API, not browser) — only `loki/mimir/nats/tempo` are mgmt-resident; the 7 are absent from mgmt (syncer-suffix count 0 each, above). |
| same page | In the **host** block, look for the 7 app tiles | NONE of the 7 appear under `host` | ❌ — all 7 ARE still under host (plain host pods in grafana/harbor/keycloak/gitea/openbao/newapi/catalyst-system namespaces, above). |
| `https://console.hw158.omani.works/apps` | Open the **`keycloak`** card → read its detail | placement shows **`mgmt`** | ❌ — `placement.yaml` line 166 declares keycloak `vcluster: host`; the live keycloak-0 pod is host-resident with no syncer suffix. |

> Cross-check appendix (NOT acceptance — run with the live kubeconfig):
> The per-app syncer-suffix loop above returns **0 for all 7** (decisive ❌). Before this ticket every
> app returned 0 (plain host pods) — and that is STILL the state on hw158.
> ```
> $ python3 scripts/audit-placement-conformance.py live --kubeconfig /tmp/hw158-kc.yaml | tail
>   ✓ bp-loki: mgmt (ns=mgmt)
>   ✓ bp-mimir: mgmt (ns=mgmt)
>   ✓ bp-nats-jetstream: mgmt (ns=mgmt)
>   ✓ bp-tempo: mgmt (ns=mgmt)
>   ✓ zero undeclared host workloads
> EXIT=0
> ```
> ⚠ **THE MASKING TRAP:** this exits 0 NOT because the 7 are in mgmt, but because they are declared
> `vcluster: host` AND are actually on host → "conformant." Only `loki/mimir/nats/tempo` resolve to
> `mgmt` here. **"audit passes" ≠ "7 actually IN mgmt."**

---

## PART C — every per-app surface still WORKS after the move (no regression)

The apps never moved, so they are still serving from host. Public surfaces spot-checked (curl, HTTP
status; "lands signed-in" requires a browser, so these prove *reachability/redirect-to-SSO*, not the
full zero-click landing):

```
$ for h in auth gitea harbor grafana bao newapi guacamole; do curl -sk -o /dev/null \
    -w '%{http_code}' "https://$h.hw158.omani.works<path>"; done
auth.hw158.omani.works/realms/sovereign/account   -> 302   (KC realm reachable, redirects to login flow)
gitea.hw158.omani.works/                          -> 303   (SSO redirect)
harbor.hw158.omani.works/                         -> 404   (portal root 404 — see note)
grafana.hw158.omani.works/                        -> 302   (SSO redirect)
bao.hw158.omani.works/ui/                          -> 302   (SSO redirect)
newapi.hw158.omani.works/                         -> 200
guacamole.hw158.omani.works/guacamole/             -> 200
```

| App | Go to (URL) | Then do | You should see | Result |
|---|---|---|---|---|
| **keycloak** | `https://auth.hw158.omani.works/realms/sovereign/account` | load it | sovereign-realm account console renders | ❌ for the ticket's claim (route reaches a **host** keycloak, not "mgmt-synced") — but the realm IS reachable (302 to login). No mgmt move to regress. |
| **gitea** | `https://gitea.hw158.omani.works/` | load it | Gitea lands signed-in | ❌ as specified ("in-vCluster gitea") — gitea is host-resident; surface reachable (303 SSO redirect). Full zero-click landing needs a browser. |
| **harbor** | `https://harbor.hw158.omani.works/` | load it | Harbor lands signed-in, projects render | ❌ as specified — harbor is host-resident; root returns **404** (portal path-quirk, not a move regression). |
| **harbor (pull path)** | `https://console.hw158.omani.works/dashboard` | confirm no `ImagePullBackOff` | cluster-wide pulls resolve | ✅ — cluster-wide pulls resolve against harbor; the `vc-mgmt-crd-probe` pod pulled `harbor.openova.io/proxy-dockerhub/bitnamilegacy/kubectl:1.31.4` successfully (phase Succeeded), and core workloads are Running. Registry path intact. |
| **grafana** | `https://grafana.hw158.omani.works/` | load it | Grafana lands signed-in | ❌ as specified ("in-vCluster grafana") — grafana is host-resident; surface reachable (302 SSO). |
| **openbao** | `https://bao.hw158.omani.works/ui/` | load it | OpenBao UI renders | ❌ as specified — openbao host-resident; UI reachable (302 SSO). |
| **newapi** | `https://newapi.hw158.omani.works/` | load it | newapi lands signed-in | ❌ as specified — newapi host-resident; surface reachable (200). |
| **guacamole** | `https://guacamole.hw158.omani.works/guacamole/` | load it | Guacamole lands signed-in, connections render | ❌ as specified ("in-vCluster guacamole") — guacamole host-resident in catalyst-system; surface reachable (200). |

> NOTE: Every ❌ in PART C is "❌ as the ticket specifies it" — the rows assert the surface reaches an
> **in-vCluster** / **mgmt-synced** app. On hw158 the apps are host-resident, so there is nothing
> migrated to regress; the surfaces themselves still respond. This is reachability evidence, not the
> full browser "lands signed-in" acceptance.

---

## PART D — the OSS-native CRD-registration mechanism is proven (sovereignty-safe)

**Chart side — `experimental.deploy.vcluster.manifests` block (live read):**

```
$ grep -nE 'experimental|deploy:|manifests|httproutes|externalsecrets|clusters.postgresql|poolers|scheduledbackups' \
    platform/bp-mgmt-vcluster/chart/values.yaml
323:        clusters.postgresql.cnpg.io:
325:        poolers.postgresql.cnpg.io:
327:        scheduledbackups.postgresql.cnpg.io:
333:        httproutes.gateway.networking.k8s.io:
335:        externalsecrets.external-secrets.io:
369:  experimental:
370:    deploy:
371:      vcluster:
372:        manifests: |
376:          name: httproutes.gateway.networking.k8s.io
407:          name: externalsecrets.external-secrets.io
432:          name: clusters.postgresql.cnpg.io
```

**Live cross-check INSIDE vc-mgmt** (probe pod with the inner `vc-mgmt-vcluster` kubeconfig):

```
$ kubectl --kubeconfig <inner-vc-mgmt> get crd httproutes... externalsecrets... clusters.postgresql... poolers... scheduledbackups...
NAME                                   CREATED AT
httproutes.gateway.networking.k8s.io   2026-06-17T03:13:29Z   ✅ registered by init-manifests deployer
externalsecrets.external-secrets.io    2026-06-17T03:13:29Z   ✅
clusters.postgresql.cnpg.io            2026-06-17T03:13:29Z   ✅
Error (NotFound): poolers.postgresql.cnpg.io                  ❌ NOT registered inside vc-mgmt
Error (NotFound): scheduledbackups.postgresql.cnpg.io         ❌ NOT registered inside vc-mgmt

$ kubectl --kubeconfig <inner-vc-mgmt> get ns
   default kube-node-lease kube-public kube-system loki mimir nats-system tempo   (only the 4 mgmt apps + system)
$ kubectl --kubeconfig <inner-vc-mgmt> get pods -A | grep -iE 'grafana|harbor|keycloak|gitea|openbao|newapi|guacamole'
   NONE-OF-7-INSIDE
```

vCluster image: `harbor.openova.io/proxy-ghcr/loft-sh/vcluster:0.21.0` (OSS, **no Pro/Platform license**).

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/blob/main/platform/bp-mgmt-vcluster/chart/values.yaml` | Search `experimental:` → read `deploy.vcluster.manifests` | structural CRDs for httproutes, externalsecrets, CNPG clusters/poolers/scheduledbackups registered INSIDE vc-mgmt by OSS init-manifests — no Pro license | ✅ PARTIAL — the block exists (lines 369-432) and uses the OSS deployer; live, **3 of 5** CRDs landed inside vc-mgmt (httproutes, externalsecrets, clusters.postgresql). `poolers` + `scheduledbackups` are declared but NOT yet registered. |
| `https://console.hw158.omani.works/cutover` (or /jobs cutover rows) | Confirm `cutoverComplete=true` OR run fresh cutover; the mgmt-resident 7 survive the 600s deny-egress hold | the 7 survive without `admin.loft.sh` tether | N/A — the 7 are NOT mgmt-resident on hw158, so "the mgmt-resident 7 survive the deny-egress hold" is untestable here. The cutover egress-block proof is owned by the Pillar-5 runbook, not this one. |

> Cross-check appendix (NOT acceptance): the in-vCluster CRD list above is the appendix. ✅ for the 3
> core kinds (httproutes + externalsecrets present → an in-vcluster app COULD create those without a
> loft.sh platform connection). The mechanism is OSS-native and partially landed; the two CNPG sub-CRDs
> are a gap to close before any CNPG-bearing app (none of the 7 are CNPG-only) could fully migrate.

---

## PART E — generality proof (the mechanism is one recipe, not 7 special-cases)

**Live grep — per-app special-casing in the slot generator:**

```
$ grep -ciE 'grafana|harbor|keycloak|gitea|openbao|newapi|guacamole' scripts/render-slot-placement.py
0
```

**Zero** occurrences of any of the 7 app names in `render-slot-placement.py` — the generator has no
per-app branch. The move is pure placement.yaml data + the shared host-bridge slot-render template.

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://github.com/openova-io/openova/pull/<PR#>/files` | Diff the 7 slot files (`09-keycloak.yaml`, `10-gitea.yaml`, `19-harbor.yaml`, `25-grafana.yaml`, `08-openbao.yaml`, `80-newapi.yaml`, `52-bp-guacamole.yaml`) | every placement field change is `render-slot-placement.py`-generated from ONE `vcluster: host → mgmt` flip — zero bespoke per-app code | ✅ (mechanism) — the generator carries zero per-app special-casing (grep count 0); slots are a pure function of placement.yaml (`render-slot-placement.py check` exits 0). The flip-PR itself hasn't landed (placement still `host`), so there is no `<PR#>` diff to open yet, but the *generality* is proven by the code. |
| same diff | Look for any per-app special-casing in Go / handlers | NONE — pure placement.yaml data + shared host-bridge template; no controller change | ✅ — `grep -ci '<7 app names>' scripts/render-slot-placement.py` = 0; no per-app conditional in the generator. |

---

## DoD roll-up (all binary)

- [ ] **PART A** — placement.yaml declares `vcluster: mgmt` for ALL 7 named apps (7 boxes). → **❌ 0/7** — all read `vcluster: host, target: mgmt`; flip never done.
- [ ] **PART B** — the treemap `mgmt` block contains all 7; host carries none of them; live pods carry the syncer suffix. → **❌** — syncer-suffix count 0 for all 7; mgmt holds only loki/mimir/nats/tempo; all 7 are host pods.
- [ ] **PART C** — all 8 per-app public surfaces still serve / land signed-in (no regression). → **❌ as specified** (host-resident, not in-vCluster) — surfaces DO respond (302/303/200; harbor root 404; pull path ✅). Nothing migrated to regress.
- [ ] **PART D** — OSS-native init-manifests CRD registration proven; 600s deny-egress hold survives (no loft.sh tether). → **✅ PARTIAL** — OSS deployer registers 3/5 CRDs inside vc-mgmt (httproutes, externalsecrets, clusters.postgresql); poolers + scheduledbackups NOT registered; deny-egress-survival untestable (7 not in mgmt). N/A for the cutover row.
- [ ] **PART E** — the move is generic (one placement flip + shared host-bridge; zero per-app code). → **✅** — generator has zero per-app special-casing (grep 0); slots pure function of placement.yaml.

**Tally: 7 ✅ / 18 ❌ / 4 N/A.**
- ✅ (7): PART A appendix `render-slot-placement.py check` exit 0; PART C harbor pull-path; PART D values.yaml block + 3/5 in-vc CRDs (counted as the one ✅ PARTIAL row); PART E both rows + the underlying grep-0 proof.
- ❌ (18): PART A 7 placement rows; PART B 4 rows (incl. the 0-count cross-check); PART C 7 "as-specified in-vCluster" rows.
- N/A (4): sign-in (browser-only); PART B 2 browser-treemap rows folded into the API proof are still scored ❌ above — the 4 N/A are: the sign-in row, PART B dashboard-grouping row, PART C is curl-not-browser (scored ❌ not N/A), PART D cutover row, and the PART E `<PR#>` diff (no PR yet — scored ✅ on the code proof). Net N/A bucket = {sign-in, PART B grouping-combobox UI action, PART D cutover-hold row, PART C "lands signed-in" full-browser bar}.

**Acceptance = the founder walking the clickable rows above on the live env.** This walk used
curl/kubectl (no browser per the harness constraint); the decisive migration evidence (syncer-suffix
count = 0 for all 7, placement.yaml still `vcluster: host`) does not require a browser and is
conclusive: **the 7 apps are NOT in the mgmt vCluster on hw158.** Automated checks are appendix only,
demoted per the UAT format law — and the placement-conformance audit's exit-0 is the masking trap, NOT
a migration proof.
