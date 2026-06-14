# Pillar-5 self-sovereign cutover — hw138 keystone catch (#3379)

**Env:** hw138.omani.works · deployment `4b5ff7852e33fc15` · 2-region Huawei (me-east-215-a primary + -b)
**Date:** 2026-06-14 (cutover auto-fired 16:12:08Z, terminal ~16:35Z)
**Chart at run time:** `bp-self-sovereign-cutover@0.1.58` · catalyst-api `9de0b65`
**Headline:** Pillar-5 cutover did **NOT** complete zero-touch. It **FAILS at step 08 (egress-block-test)**. The 600s deny-egress hold **never executes**, the `cutover-egress-block` CCNP is **never applied**, and `cutoverComplete` stays **false**.

This reproduces the same failure class seen on hw136 (`egress-block-test result=failed`) on a second, independent env — so it is a real architectural defect, not env flake.

---

## STEP 0 — handover-finalise contract (verified from code)

Source: `products/catalyst/bootstrap/api/internal/handler/handover.go` + `cmd/api/main.go` + `internal/auth/{middleware,session}.go`.

- **Endpoint:** `POST /sovereign/api/v1/handover/finalise/{id}` → the gateway strips `/sovereign` → `POST /api/v1/handover/finalise/{id}` (main.go:1270, inside the `RequireSession` group).
- **Auth gate:** `auth.RequireSession`. On the mothership `CATALYST_KC_ADDR` IS set, so `cfg` is non-nil and the middleware is a REAL gate (the in-code comment at main.go:1263 — "on Catalyst-Zero cfg is nil so RequireSession is a passthrough" — is **stale/incorrect**). It accepts an RS256 JWT presented as `Authorization: Bearer <jwt>` with **no `kid` header**, validated against the mothership's `LocalPublicKey` (the handover signer's public key). It checks **signature + `exp` only** — there is **no iss/sub/aud/deployment_id/role claim validation** in the middleware.
- **Status gate:** `FinaliseHandover` does **NOT** gate on deployment status — it only 404s on an unknown id, then runs the 4 steps. So even a `failed`-marked-but-converged env would finalise. (Moot here: live status was `ready`.)
- **Owner key:** `/tmp/hw-priv.pem` is **byte-identical** to the mothership's `/var/lib/catalyst/handover-jwt-private.pem` (both 1671 B; pubkey DER sha256 `3e232eb3…5bde`), so a JWT signed with it validates against `LocalPublicKey`.

## STEP 1 — handover finalise → archive sealed → cutover auto-fired

The cutover **engine fired at `cutoverStartedAt=2026-06-14T16:12:08Z`** with `source="handover"`. Per `handover.go:368-437`, the engine only fires `source="handover"` **after** `client.PutKVv2("secret","catalyst/tofu-phase0-archive",…)` returns 200 — i.e. the archive seal is a precondition of the fire. Step-01 gitea-mirror starting at the same instant is the proof the seal landed. ✅ archive sealed + cutover auto-fired.

## STEP 2 — the 11 cutover steps (per-step result)

| # | Step | Result | Note |
|---|------|--------|------|
| 01 | gitea-mirror | ✅ success | catalog mirrored into local Gitea |
| 02 | harbor-projects | ✅ success | `openova-io` + proxy projects created in local Harbor |
| 03 | harbor-prewarm | ⚠️ **FALSE-GREEN** | reported `push_ok=29 push_fail=0` but **pushed NOTHING** — see Defect A |
| 04 | registry-pivot | ✅ success | `ghcr-pull` dockerconfigjson auths → `registry.hw138.omani.works` (ghcr.io entry stripped) |
| 05 | flux-gitrepository-patch | ✅ success | Flux `GitRepository/openova` URL → local Gitea — but **no secretRef** (see Defect B) |
| 06 | helmrepository-patches | ⚠️ **INEFFECTIVE** | edited 60 bootstrap-kit files in Gitea, but the live HelmRepository CRs never pivoted (Flux can't pull — Defect B) |
| 07 | catalyst-api-env-patch | ✅ (HR patch) → 💥 | flipped `global.imageRegistry=registry.hw138…`, rolled catalyst-api to the local-Harbor image → **ImagePullBackOff** (image absent, Defect A) → engine goroutine died with the old pod |
| 08 | egress-block-test | ❌ **FAILED** (`failed=1`) | the #3526 guard correctly bounded-waited 300s for HelmRepository pivot, found 61 still on `ghcr.io/openova-io`, and **refused to apply the deny-egress CCNP** |
| 09 | gitea-token-mint | ⛔ never reached | |
| 10 | vcluster-registry-pivot | ⛔ never reached | |
| 11 | crossplane-provider-pivot | ⛔ never reached | |

## STEP 3 — the four completion proofs

1. **`cutoverComplete`** = **false** (`self-sovereign-cutover-status` CM, ns catalyst). Job-level authoritative terminal: `cutover-egress-block-test-1781454299` has `failed=1`.
2. **deny-egress CCNP** = **NOT present** (`kubectl get ccnp cutover-egress-block` → NotFound). The 600s hold never ran. HRs were never put under the egress block. ❌
3. **`ghcr-pull` dockerconfigjson auths keys** = `["registry.hw138.omani.works"]` — registry tether **IS** pivoted (step-04 succeeded). ✅ (but the registry it points at is empty — see Defect A).
4. **GitRepository source** = `http://gitea-http.gitea.svc.cluster.local:3000/openova/openova` (local Gitea, step-05 pivoted). ✅ source-of-truth, but Flux can't pull it (Defect B), so it's stuck at the stale pre-cutover artifact `5e645666`.

---

## Root causes (two independent defects, either blocks Pillar 5)

### Defect A — Step-03 harbor-prewarm skopeo native-push is a SILENT NO-OP

`platform/self-sovereign-cutover/chart/templates/03-harbor-prewarm-job.yaml`, Phase A loop.

Live proof: the local Harbor `openova-io` project is **empty** — `repo_count=0`, `/api/v2.0/projects/openova-io/repositories` returns `[]`, and a registry-v2 manifest HEAD for `catalyst-api:9de0b65` returns **404 not found**. Yet the Job log claims:
```
copy ghcr.io/openova-io/openova/catalyst-api:9de0b65 → registry.hw138.omani.works/openova-io/openova/catalyst-api:9de0b65
  time="2026-06-14T16:19:04Z" level=fatal msg="Error loading trust policy: open /etc/containers/policy.json: no such file or directory"
[harbor-prewarm] OK ghcr.io/openova-io/openova/catalyst-api:9de0b65
...
[harbor-prewarm] Phase A complete: push_ok=29 push_fail=0
```

Two bugs combine:
1. **skopeo aborts `level=fatal`** because the `alpine/k8s` base image + the `lework/skopeo-binary` static binary ship **no `/etc/containers/policy.json`** signature-trust policy. Every copy dies before transferring a byte. → Fix: pass `--insecure-policy`.
2. **`skopeo copy ... 2>&1 | sed` masks the exit code.** Under `set -eu` with no `set -o pipefail`, the `if` evaluates `sed`'s status (always 0), not skopeo's, so every fatal copy is counted as `push_ok`. → Fix: capture skopeo's real exit status (write to a temp log, prefix-print, branch on the true status).

Downstream blast radius: every openova-io workload that rolls onto the pivoted registry ImagePullBackOffs. catalyst-api hit this directly at step 07.

**Fixed in this PR** (chart `0.1.58` → `0.1.59`). Verified: `helm template` renders + `dash -n`/`sh -n` syntax-clean.

### Defect B — Step-05/06/09 ordering: GitRepository pivots to authenticated Gitea before the pull-credential is minted

Live proof: `GitRepository/openova` is `Ready=False`:
```
GitOperationFailed: failed to checkout and determine revision: unable to list remote for
'http://gitea-http.gitea.svc.cluster.local:3000/openova/openova': authentication required
ArtifactInStorage=True … revision 'main@sha1:5e645666…'   ← STALE (pre-cutover)
```
`.spec.secretRef` is **empty**. Step-05 (`05-flux-gitrepository-patch`) repoints the GitRepository to the *authenticated* local Gitea, but the Gitea pull-token is only minted at **step-09 (`09-gitea-token-mint`)** — which runs **after** step-08. So between steps 05 and 09 Flux loses git access, freezes at the stale artifact, and step-06's HelmRepository URL rewrites (pushed into Gitea) never reconcile onto the live CRs (which are `kustomize.toolkit.fluxcd.io/name: bootstrap-kit`-managed). 61 HelmRepositories stay `oci://ghcr.io/openova-io`, so step-08's bounded pivot-wait can never pass.

**Not fixed in this PR** — tracked as a follow-up under #3379 (re-order so the Gitea pull-credential is wired before/with the GitRepository pivot, or give the pivoted GitRepository a working `secretRef` at step-05). Note: even with Defect B fixed, Defect A must also be fixed — the pivot target (`oci://registry.hw138…/openova-io`) is the same empty Harbor.

### Additional finding — cutover engine does not survive its own step-07 self-roll

The cutover engine runs inside the catalyst-api process. Step-07 rolls catalyst-api itself; the in-flight engine goroutine dies with the old pod and does not resume in the new pod. The `self-sovereign-cutover-status` CM is therefore orphaned at `currentStepIndex=7 / step08=running / pct=63` even though the step-08 Job definitively `failed`. (On hw136 the env-patch finished in ~7s so the engine survived to record `failed`.) Worth a resume-on-restart or an out-of-process engine — captured for the #3379 follow-up.

---

## Evidence files (this dir)

- `hw138-01-cutover-status-WEDGED.json` — authoritative terminal (Job `failed=1`, orphaned status CM, env survival)
- `hw138-02-catalyst-api-ImagePullBackOff.txt` — local-Harbor image NotFound
- `hw138-03-harbor-openova-io-EMPTY.txt` — Harbor `openova-io` repo_count=0 / repositories `[]`
- `hw138-04-harbor-prewarm-skopeo-policy-fatal.txt` — skopeo policy.json fatal masked as OK
- `hw138-05-step07-hang-on-rollout.txt` — step-07 rollout-wait
- `hw138-06-helmrepository-pivot-INCOMPLETE.txt` — 61 HRs still ghcr.io, GitRepository on local Gitea
- `hw138-07-gitrepository-AUTH-FAILURE.txt` — GitRepository auth-required, empty secretRef
- `hw138-08-ghcr-pull-keys-PIVOTED.txt` — registry tether pivoted (proof 3)
- `hw138-09-step08-egress-block-FAIL.txt` — step-08 FAIL verdict, CCNP never applied
