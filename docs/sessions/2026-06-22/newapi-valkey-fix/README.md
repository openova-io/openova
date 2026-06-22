# bp-valkey image-ref fix + live recovery — omantel.biz (kom4dc, dep `4635277cae4ffed9`)

**Issue:** [#4136](https://github.com/openova-io/openova/issues/4136) · **PR:** [#4137](https://github.com/openova-io/openova/pull/4137) · **Date:** 2026-06-22

## Root cause (confirmed live)

`valkey-primary-0-x-valkey-x-rtz-vcluster` (the rtz-region valkey, inside the `rtz`
vCluster) sat **Init:ImagePullBackOff for 15h (4091 backoffs)**. The bp-valkey chart
(wrapping the upstream Bitnami valkey 5.5.1 subchart) rendered the **upstream-default
bare dockerhub image refs unchanged**:

- main container: `registry-1.docker.io/bitnami/valkey:latest`
- volumePermissions init: `registry-1.docker.io/bitnami/os-shell:latest`

Bitnami relocated its free images to the `bitnamilegacy/*` archive in 2025-08, so
`bitnami/valkey:latest` no longer exists and the Sovereign node registry mirror 404s
the bare dockerhub path. bp-valkey was the last Bitnami-based chart in the repo still
shipping bare refs (flux / openbao / cnpg / cert-manager / keycloak / guacamole /
external-secrets were all converted to the Harbor proxy long ago).

### Downstream cascade (why it was a P0)

valkey down → its Service `valkey-primary-x-valkey-x-rtz-vcluster` (ClusterIP
10.96.190.40:6379) had **no endpoint** → **bp-newapi** (mgmt vCluster,
`REDIS_CONN_STRING=redis://valkey-primary-x-valkey-x-rtz-vcluster.rtz.svc.cluster.local:6379`)
CrashLooped with `[FATAL] Redis ping test failed: … connection refused` (180+ restarts)
→ the newapi `/console` static shell 200'd but `/api/user/self` + `/api/oauth/state`
returned **503**. Broke **UAT rows 37, 38, 114**.

## The chart fix (PR #4137 — the proven repo-wide pattern, mirrors bp-keycloak #3263)

`platform/valkey/chart/values.yaml` under the `valkey:` subchart namespace:

| key | value |
|---|---|
| `valkey.image` | `harbor.openova.io/proxy-dockerhub/bitnamilegacy/valkey:8.1.3-debian-12-r3` |
| `valkey.volumePermissions.image` | `harbor.openova.io/proxy-dockerhub/bitnamilegacy/os-shell:12-debian-12-r50` |
| `global.security.allowInsecureImages` | `true` (bitnami/charts#30850 render guard — proxy-dockerhub is a read-through cache of the identical bitnamilegacy bytes) |

All tags verified present in the on-Sovereign proxy via skopeo / registry-v2. `helm
template` renders only the proxy refs (zero bare `registry-1.docker.io` / `:latest`).
Chart `1.1.3 → 1.1.4` + `blueprint.yaml` + bootstrap-kit slot-17 pin bumped in lockstep
(chart-only — no catalyst-api/ui image change → no deploy-bot collision). Tests green:
`observability-toggle.sh`, `contexts-render.sh`, `check-bootstrap-kit-pin-sync.sh`
(bp-valkey chart=1.1.4 pin=1.1.4 OK), `check-catalog-seed-lockstep.sh`.

## Live recovery (this permanent env converged NOW, ahead of the merge/sync)

The valkey StatefulSet lives inside the `rtz` vCluster (vCluster does NOT sync
StatefulSets to the host). Reached it via an in-Sovereign debug pod mounting the
`vc-rtz` kubeconfig Secret. driftDetection.mode is off, so a raw STS patch survives
Flux reconciles until the merged chart syncs through Gitea.

1. `kubectl set image statefulset/valkey-primary valkey=harbor.openova.io/proxy-dockerhub/bitnamilegacy/valkey:8.1.3-debian-12-r3` (+ `valkey-replicas`).
2. The new image pulled cleanly (51 MB in 37s), but valkey then hit a **runtime**
   crash: `Bad file format reading the append only file appendonly.aof.1.base.rdb`
   — a stale AOF on the PVC, written by the old image, incompatible with valkey 8.1.3.
   Since valkey is an **ephemeral cache**, scaled the STS to 0, mounted the
   `valkey-data-valkey-primary-0` PVC in a cleaner pod, wiped `/data/appendonlydir`
   (PVC + STS identity preserved), scaled back to 1.
3. valkey-primary-0 came up **1/1 Running** — "Ready to accept connections tcp",
   version 8.1.3, fresh AOF. (The `vcluster-rewrite-hosts` alpine init already pulled
   via `harbor.openova.io/proxy-dockerhub/library/alpine:3.20` — the vCluster syncer
   rewrites it through the proxy.)

## Verified recovery chain (live)

| Check | Result |
|---|---|
| valkey-primary-0 (in vCluster) | **1/1 Running**, 0 restarts, valkey 8.1.3, "Ready to accept connections" |
| host-synced pod `valkey-primary-0-x-valkey-x-rtz-vcluster` | **1/1 Running** |
| Service `valkey-primary-x-valkey-x-rtz-vcluster` endpoint | **10.42.4.47:6379** (was empty) |
| bp-newapi pods (mgmt) | `newapi-bp-newapi-564fcd6668-7rcgl` **3/3 Running, 0 restarts** (both `newapi` + `metering-sidecar` containers); main log `[SYS] Redis is enabled`, no FATAL |
| `https://newapi.omantel.biz/api/user/self` | **401** (auth-required, was **503**) |
| `https://newapi.omantel.biz/api/oauth/state` | **200** (was 503) |
| `https://newapi.omantel.biz/api/status` | **200** + full JSON |

## UAT walk (Playwright, live, omantel.biz)

- **Row 37** — bare `newapi.omantel.biz/` (1st hit) → auto-redirects into the SSO OAuth
  flow (`auth.omantel.biz/realms/sovereign/.../auth?kc_idp_hint=catalyst-pin&client_id=newapi-admin`)
  → lands on `/console/token` **signed in as `sovereign_2`** (display `emrah.baysal@openova.io`).
  No "Unknown OAuth provider", no login form. `/api/user/self` → **200, role 100** (admin).
  Evidence: `evidence/newapi-console-signed-in.png`.
- **Row 38** — bare URL (2nd hit, re-entry) → "Signing you in…" → lands on
  `/console/token` signed-in again. NOT an "already bound" / re-link / `/setup` wizard.
  Evidence: `evidence/newapi-console-reentry-signed-in.png`.
- **Row 114** — newapi opens signed in, its main console renders (full sidebar:
  Dashboard / Token Management / Usage Logs / Personal Settings; Token Management grid
  renders). No login form, no upstream-connect error. Evidence: same two screenshots.

`/api/user/self` (authed, from the live browser session): `status=200, username=sovereign_2, role=100`.
