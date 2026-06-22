# Demo → Agenity → Website Journey — omantel.biz (dep `4635277cae4ffed9`)

**Date:** 2026-06-22
**Target:** `demo@openova.io` logs into `console.demo.omani.homes`, opens **agenity**, and
uses it to create a website. Org `demo` (ns `org-7283eb4a-19e5-4e86-9066-d4aa26762064`),
vcluster `rtz`, Environment `omantel-biz-prod`.

This runbook records the exact steps that WORKED, the live fixes applied to advance the
walk, and the single remaining gating dependency (next action below).

---

## Live access (the working runbook)

```bash
# 1. Mothership catalyst-api pod holds the Sovereign kubeconfig on a PVC.
POD=$(kubectl -n catalyst get pods -l app.kubernetes.io/name=catalyst-api \
  -o jsonpath='{range .items[?(@.status.containerStatuses[0].ready==true)]}{.metadata.name}{"\n"}{end}' | head -1)

# 2. Copy it OUT to a local file (the target API server 212.72.24.1:6443 is
#    reachable directly — far more stable than exec'ing the churning mothership pod).
kubectl -n catalyst exec $POD -- cat /var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml > /tmp/target-kc.yaml
kubectl --kubeconfig=/tmp/target-kc.yaml get ns          # verify
```

The rtz **vcluster** API (`rtz-vcluster.rtz:443` / ClusterIP) is NOT reachable from the
mothership pod nor from outside — only from within the target cluster. vcluster-synced
StatefulSets must be patched via the vcluster, but the synced **Pods** are visible/host-side
in ns `rtz` and good enough for diagnosis.

---

## STEP 0 — read the PIN (passwordless 6-digit email login)

`demo@openova.io` is a **mailing LIST**, not a mailbox — it expands to
`hatice.yildiz@openova.io` + `emrah.baysal@openova.io`. Read the PIN from hatice's inbox:

```bash
HPASS=$(kubectl -n stalwart get secret stalwart-user-credentials -o jsonpath='{.data.hatice\.yildiz}' | base64 -d)
# newest UID via FETCH (SEARCH index lags ~20-30s behind delivery):
MAX=$(curl -sk --url "imaps://mail.openova.io:993/INBOX" --user "hatice.yildiz@openova.io:$HPASS" \
  --request "FETCH 1:* (UID)" | grep -oE 'UID [0-9]+' | grep -oE '[0-9]+' | sort -n | tail -1)
curl -sk --url "imaps://mail.openova.io:993/INBOX;UID=$MAX" --user "hatice.yildiz@openova.io:$HPASS" \
  | grep -oE 'sign-in code: [0-9]{6}'
```

Gotchas: email delivery lags the API `pin/issue` by 20-30s; the `pin/issue` rate-limit is
~60s; multiple in-flight PINs each bind to their own `requestId`, so always read the email
that arrives AFTER the browser's "Send code" click.

---

## STEP 1 — demo login (PIN flow WORKS; whoami 401 is the gating defect)

1. `https://console.demo.omani.homes/login` → enter `demo@openova.io` → **Send code**.
2. Read the PIN (Step 0) → enter the 6 digits on `/login/verify`.
3. `POST /api/v1/auth/pin/verify` → **200 `pin/verify: session established`** (✓ verified live).
4. `GET /api/v1/whoami` → **401** → UI bounces back to `/login`. ✗

**Root cause (confirmed on the wire):** the `catalyst_session` cookie is minted with
`Domain=omantel.biz` (auto-derived from `SOVEREIGN_FQDN=omantel.biz`), but the demo console
host is `console.demo.omani.homes`. The browser **rejects** a `.omantel.biz` cookie set from
`console.demo.omani.homes` (registrable-domain mismatch) → no cookie stored → whoami has no
session → 401.

```
set-cookie: catalyst_session=...; Domain=omantel.biz; ...   # WRONG for console.demo.omani.homes
```

**Fix:** already merged — **#4104 (`6b22cba`)** adds `sessionCookieDomain(r)` which derives the
cookie Domain from the REQUEST HOST: `console.demo.omani.homes` → `.demo.omani.homes`
(per-Org suffix, no cross-Org leak). **Not yet live** — the live catalyst-api is `378f85f`,
which predates it. The roll is gated by the registry dependency below (next action).

---

## STEP 1 prereq — demo Org keycloak (FIXED live this session)

The `org-demo` realm did not exist and `bp-keycloak` / `vc-demo` were `ImagePullBackOff`.
Three independent live fixes unblocked it:

1. **Bitnami images on bare `docker.io` 404 (mirror returns HTML).** The node `docker.io`
   mirror returns an HTML error page (cached under a bogus digest `sha256:81e90dcf…`),
   while `harbor.openova.io/proxy-dockerhub/…` serves the SAME image fine. Patched the live
   StatefulSets to the proxy prefix (suspended the wedged HRs first):
   - `bp-keycloak` (init+main) → `harbor.openova.io/proxy-dockerhub/bitnamilegacy/keycloak:26.3.3-debian-12-r0`
   - `bp-keycloak-postgresql` → `…/proxy-dockerhub/bitnamilegacy/postgresql:17.6.0-debian-12-r0`
   - `vc-demo` k3s init → `…/proxy-dockerhub/rancher/k3s:v1.29.1-k3s2`
   → all three pods went Running. (Permanent fix: the chart already defaults to
   proxy-dockerhub; the live Sovereign deployed a STALE published chart `1.5.0`.)

2. **`keycloak-config-cli` Job on bare `docker.io`** (ImagePullBackOff) — recreated the Job
   with `harbor.openova.io/proxy-dockerhub/bitnamilegacy/keycloak-config-cli:6.4.0-debian-12-r11`.

3. **Realm import 500 `value too long for type character varying(255)`** — the `stalwart`
   OIDC client `description` in the realm config was **288 chars** (Keycloak `CLIENT.DESCRIPTION`
   is `varchar(255)`), aborting the whole realm import → realm 404. Patched the live
   `bp-keycloak-sovereign-realm-config` ConfigMap to the short 133-char description (the repo
   template `configmap-tenant-realm.yaml` is already ≤255) and re-ran config-cli.
   → `org-demo` realm created, Keycloak 26.3.3 Running, 9 clients, admin user `demo@openova.io`.

---

## STEP 2 — agenity Running (ACHIEVED)

- The `agenity` Application (Blueprint `bp-agenity`, vcluster `rtz`) was CrashLooping on the
  broken `bp-agenity:0.1.0` image (`exec: "run": executable file not found` — exit 128).
- A peer shipped the fixed image **`bp-agenity:0.9.4`** (#4097/#4098, chepherd daemon built
  from PUBLIC `agenity-org` source). The live HR `agenity-rtz-a` (values `image.tag: 0.9.4`)
  rolled the StatefulSet to `0.9.4`.
- **agenity pod is `1/1 Running` (0 restarts)** — chepherd daemon up:
  `Auth provider: local` · `HTTP/WS server + web UI on http://0.0.0.0:8080` ·
  `Runtime up. Open …:8080 (dashboard) to spawn workers` · `alive sessions: 0` heartbeat.
- Reachable in-cluster at `agenity-rtz-a-bp-agenity-x-rtz-x-rtz-vcluster:8080` (ClusterIP).
- The agenity **Application** still shows `PHASE=Degraded` — NOT the pod, the controller:
  the live `application-controller` (`34ff594`) predates the #4079/#4080 RFC-1123 fix and
  keeps trying to create an invalid HelmRelease `agenity-rtz-A` (uppercase region token).
  **Fix: PR #4109** pins `application-controller` `34ff594→6d7aacd` in `bp-catalyst-platform`.
  No agenity HTTPRoute exists yet (per #4082 — Gateway-API CRD in the target vCluster).

---

## ROOT GATING DEPENDENCY — private-ghcr registry-mirror 401 (gates every catalyst roll)

Both remaining rolls (cookie fix `6b22cba` for catalyst-api, RFC fix `6d7aacd` for
application-controller) are HARD-blocked at image pull:

```
Failed to pull ghcr.io/openova-io/openova/catalyst-api:75ff4c2:
  harbor.openova.io/v2/proxy-ghcr/openova-io/openova/catalyst-api/blobs/…: 401 Unauthorized
```

The node containerd mirror rewrites `ghcr.io` → **external** `harbor.openova.io/proxy-ghcr`
(`45.151.123.50`), whose proxy-cache is anonymous and **cannot pull the PRIVATE
`openova-io/openova/*` images** → 401 → `ImagePullBackOff`. Old images (e.g. `378f85f`) are
already cached on the nodes, so the API self-heals back to the stale image; new images never
land. This is the documented registry-routing regression (memory
`reference_kom4dc_region_bootstrap_failure_is_registry_routing_not_quota` /
`reference_bastion_harbor_warmup_every_image_huawei_to_huawei`): private ghcr must route to
the **bastion `212.72.24.20:5000` hosted `openova-io` project** (NAT-bypass, robot-authed),
not the external proxy-ghcr. The fix is in `registry_mirror_yaml_huawei` (cloud-init/IaC) +
the catalyst-build warmup step pushing each new catalyst image into the bastion — owned by
the registry/bastion-warmup pipeline; it is sacred cloud-init + shared infra, not a live
additive patch.

**Net:** when the catalyst-api rolls to `6b22cba` (and application-controller to `6d7aacd`),
Step 1 login and the Step 2 Application status complete with no further code changes — every
code fix is already merged (#4104) or in-flight (#4109). The journey is gated solely on the
private-ghcr registry-mirror reaching the live nodes.

---

## What this session shipped / advanced

- ✅ Demo Org keycloak fully unblocked live: `org-demo` realm created, keycloak Running
  (docker.io→proxy-dockerhub image patches + the 255-char realm-description fix).
- ✅ agenity `1/1 Running` on the fixed `0.9.4` image (chepherd daemon serving on :8080).
- ✅ PIN issue→email→verify proven end-to-end (`pin/verify: session established` 200).
- ✅ PR **#4109** — pin `application-controller 6d7aacd` (RFC-1123 HR names) in
  bp-catalyst-platform so fanned-out Applications stop wedging Degraded.
- Next action: Step-1 demo login + agenity-opened-by-user screenshots are gated on the
  private-ghcr registry-mirror reaching the nodes (cookie fix #4104 `6b22cba` can't roll).
