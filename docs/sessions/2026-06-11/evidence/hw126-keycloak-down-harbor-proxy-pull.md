# hw126 — REAL email+PIN SSO walk BLOCKED: Keycloak down cluster-wide (kyverno `harbor-proxy-pull` enforce omits the `keycloak` namespace)

**Walked:** 2026-06-11, live browser (Playwright) + kubectl read-only on hw126 (`c986326a77d391d4`, `hw126.omantel.biz`).
**Mission:** obtain a REAL console session as `emrah.baysal@openova.io` via genuine email+PIN (NOT the handover-JWT bypass the OIDC bridge rejects), then walk the zero-prompt SSO matrix the real way.

## Headline

The real email+PIN login path **works end-to-end up to the auth layer** — and the PIN-delivery route is now PROVEN reachable (contradicting the earlier "no real inbox" assumption): hw126's catalyst-api sends PINs via `mail.openova.io:587` as `noreply@openova.io` (the mothership Stalwart), so a PIN for `emrah.baysal@openova.io` lands in that real, IMAP-readable inbox (480 prior `Your OpenOva sign-in code: NNNNNN` emails already present).

**BUT the walk is hard-blocked: Keycloak is completely down on hw126.** Both auth paths (PIN-login `EnsureUser` AND the catalyst-pin silent-SSO OIDC bridge) route through Keycloak, which has **zero running pods**. This is NOT a route-around gate — it is a genuine, reproducible live-cluster outage.

## Live browser evidence (real login attempt)

1. `https://console.hw126.omantel.biz/login` → entered `emrah.baysal@openova.io` → clicked **Send code**.
2. Form returned the inline alert: **"could not provision user record"** (HTTP 502 `user-provisioning-failed`).
   - Screenshot: `hw126-pinlogin-FAIL-provision-record.png`
3. **No PIN email was sent** — the `emrah.baysal@openova.io` inbox `FROM noreply@openova.io` count stayed at **480** (unchanged from baseline), proving the failure occurred at `EnsureUser` *before* the SMTP send step.

## Root cause chain (kubectl, read-only)

catalyst-api log at the moment of the click:
```
pin/issue: EnsureUser failed  email=emrah.baysal@openova.io
err="keycloak.EnsureUser: service account token: keycloak: POST token:
  Post \"http://keycloak.keycloak.svc.cluster.local/realms/sovereign/protocol/openid-connect/token\":
  dial tcp 10.96.31.36:80: connect: connection refused"
POST .../api/v1/auth/pin/issue ... 502
```

Why "connection refused":
- `kubectl get pods -n keycloak` → **No resources found**. Both StatefulSets `keycloak` and `keycloak-postgresql` are **0/1**.
- `Service/keycloak` (10.96.31.36:80) has **no endpoints** → connection refused.
- `auth.hw126.omantel.biz/realms/sovereign/.well-known/openid-configuration` → **503** (public OIDC discovery doc unreachable — every silent-SSO app needs it).

Why the pods can't (re)start — kyverno **`Fail`-mode admission webhook** denies pod creation:
```
admission webhook "validate.kyverno.svc-fail" denied the request
resource Pod/keycloak/keycloak-0 was blocked due to the following policies
harbor-proxy-pull:
  image-from-harbor-proxy-pod: Container image does not match any allowed
  Harbor-source glob (proxy-cache `*/proxy-*/*` or native-push `*/openova-io/*`).
```
- keycloak STS image: `docker.io/bitnamilegacy/keycloak:26.3.3-debian-12-r0`
- keycloak-postgresql STS image: `docker.io/bitnamilegacy/postgresql:17.6.0-debian-12-r0`
- Neither matches the `harbor-proxy-pull` allowed globs → **denied at create**.

How the pods got deleted in the first place (the trigger):
- `bp-keycloak` HR attempted **upgrade 1.4.21 → 1.4.22** at 05:16Z today.
- The post-upgrade hook `keycloak-config-cli-job` was itself **denied by `harbor-proxy-pull`** → `UpgradeFailed`.
- Flux then tried to **roll back to 1.4.21** → `RollbackFailed: context deadline exceeded`. The rollback deleted `keycloak-0` + `keycloak-postgresql-0` (events: `SuccessfulDelete` / `Killing` ~22m before walk), then `FailedCreate` on recreation (kyverno denial). Net: 0 pods, stuck.

The smoking gun — **`keycloak` namespace is NOT in the policy's exclude list**:
```
ClusterPolicy harbor-proxy-pull  action=Enforce  created=2026-06-10T12:57:25Z
rule image-from-harbor-proxy-pod exclude namespaces:
  [kube-system, flux-system, cilium, cilium-gateway, cert-manager,
   openova-system, catalyst, kyverno, monitoring, ingress, dmz, mgmt, rtz, sso-bridge]
```
`sso-bridge` IS excluded but `keycloak` (and `harbor` itself, `gitea`, `grafana`, `guacamole`, `external-secrets-system`, etc.) are NOT. 121 already-running pods cluster-wide carry images that violate this same glob — they only survive because the policy bites on **pod (re)creation**, not running pods. The instant any of those workloads' pods are recreated (chart bump, node drain, OOM-restart), they hit the same wall. Keycloak hit it first via the 1.4.22 bump.

## Blast radius

Every `bp-keycloak`-dependent HR is wedged `Ready=False · dependency 'flux-system/bp-keycloak' is not ready`:
`bp-gitea`, `bp-grafana`, `bp-guacamole`, `bp-newapi`, `bp-powerdns-admin`, `bp-sso-bridge`.

→ The entire zero-prompt SSO matrix (S4: grafana/harbor/gitea/guacamole) **cannot be walked** — there is no Keycloak to authenticate against, and no way to even reach an authenticated console session via the real PIN path.

## Verdict

- Real email+PIN login path: **architecturally sound + PIN delivery PROVEN reachable** (mothership Stalwart inbox), but **BLOCKED** at `EnsureUser` because Keycloak is down.
- SSO matrix (S4): **BLOCKED** — same Keycloak outage gates the catalyst-pin OIDC bridge.
- Root cause: `harbor-proxy-pull` ClusterPolicy in **Enforce** mode does not exclude the `keycloak` namespace (nor harbor/gitea/grafana/guacamole/external-secrets), so kyverno denies recreating the bitnami-image keycloak pods after the 1.4.22 chart-bump rollback deleted them.

## Fix direction (for a follow-up shippable PR — this walk is READ-ONLY)

Add `keycloak` (and the other app namespaces running non-Harbor images: `harbor`, `gitea`, `grafana`, `guacamole`, `external-secrets-system`, `alloy`, `falco`, `crossplane-system`, `cnpg`/`cnpg-system`) to the `harbor-proxy-pull` rule exclude lists — OR flip the policy to `Audit` until the catalog images are genuinely re-tagged through the local Harbor proxy. Same class of incident as the `bp-network-policies default-deny` apiserver-entity outage (#3201) and the "safe-by-default single-region render is insufficient" wave (#3188): a CI-green Enforce policy that the live workloads violate, only surfacing on pod re-creation against a 2-region prov.
