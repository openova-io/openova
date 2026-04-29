# Secret Rotation

The canonical list of credentials Catalyst-Zero handles, where each one
lives, and how to rotate it.

Per [INVIOLABLE-PRINCIPLES.md](./INVIOLABLE-PRINCIPLES.md) #10 (credential
hygiene): **passwords, tokens, API keys, client secrets, kubeconfig
contents, TLS private keys, and `.env` values are all credentials and
treated identically.** No credential is committed to git, ever. The
catalyst-api Pod's runtime env is the single source of truth for every
secret it consumes; persisted deployment records redact every one of them
via `internal/store.Redact`.

This document is the operator runbook for rotating each of those
credentials on the schedule below — and the rollback path if a rotation
breaks something live.

## Rotation Schedule

| Credential | Where it lives | Rotation cadence | Rollback window |
|---|---|---|---|
| GHCR pull token (`catalyst-ghcr-pull-token`) | K8s Secret in `catalyst` ns, key `token` | **Yearly** | 24h via 1Password version history |
| Hetzner Cloud API token (per Sovereign) | Wizard input → catalyst-api memory only | Per Sovereign apply | n/a — single-use, never persisted |
| Dynadot API key + secret (`dynadot-api-credentials`) | K8s Secret in `openova-system` ns, keys `api-key` + `api-secret` | **Yearly** (or on personnel change) | 24h via 1Password version history |
| Sovereign Admin SSO client secret (Keycloak `catalyst-admin` realm) | Per-Sovereign K8s Secret in `keycloak` ns | **Yearly** | 1h — Keycloak supports two active client secrets during rollover |
| SOPS / SealedSecrets cluster key (per Sovereign) | K8s Secret in `kube-system` ns | **Per Sovereign**, never rotated post-bootstrap | n/a — re-key requires migrating every existing SealedSecret |

The rest of this document is the per-credential procedure.

---

## GHCR pull token (`catalyst-ghcr-pull-token`)

**What it is.** A long-lived GitHub Personal Access Token (PAT) or
fine-grained token with the `packages:read` scope on the `openova-io`
organisation. The token authenticates the GHCR pulls Flux performs on
every freshly-provisioned Sovereign — every `HelmRepository` CR in
`clusters/<sovereign-fqdn>/bootstrap-kit/` references the
`flux-system/ghcr-pull` Secret, and that Secret's content comes from this
token.

**Why this token has its own runbook.** The bootstrap-kit pulls the bp-*
OCI artifacts from `ghcr.io/openova-io/`, which is a **private** registry
path. Without the token, the source-controller logs:

```
failed to get authentication secret 'flux-system/ghcr-pull':
  secrets "ghcr-pull" not found
```

…and Phase 1 stalls at bp-cilium. The fix that landed this runbook
(`fix(cloudinit): create flux-system/ghcr-pull secret on Sovereign so
private bp-* charts pull cleanly`) makes the cloud-init template write
the Secret BEFORE `kubectl apply -f flux-bootstrap.yaml`, but the token
itself is never in the template — OpenTofu interpolates it at apply time
from `var.ghcr_pull_token`, sourced from the catalyst-api Pod's env var
`CATALYST_GHCR_PULL_TOKEN`.

**Where the token must NEVER be:** git (any branch, any repo), the
bootstrap-kit YAMLs, the catalyst-api Pod logs, the Hetzner project
metadata, Slack/email/issue bodies. The provisioner stamps it onto the
Request struct in memory, writes `tofu.auto.tfvars.json` (mode 0600), and
that file is wiped when the per-deployment workdir is cleared. The
`json:"-"` tag on `Request.GHCRPullToken` keeps it out of the persisted
deployment records (see `internal/store.Redact`).

### Generation

Generate a fine-grained PAT (preferred over classic PATs):

1. https://github.com/settings/personal-access-tokens/new
2. Resource owner: **openova-io**
3. Repository access: **Public Repositories (read-only)** — this is
   sufficient because GHCR packages inherit the openova-io org's GHCR
   visibility settings; the token does not need repo-level access.
4. Permissions:
   - **Account → Packages → Read** (the only scope this token uses)
5. Expiration: **365 days** (next rotation date — write it on the
   1Password item).
6. Generate. **Copy the token to 1Password immediately** (the page
   shows it once); never paste it into a terminal or a chat window.

### Storage

1Password vault: **OpenOva — Production**
Item title: **Catalyst — GHCR pull token (catalyst-ghcr-pull-token)**
Tags: `catalyst`, `ghcr`, `rotation:yearly`

Notes field on the 1Password item must record:
- Generation date.
- Expiration date.
- Username paired with this token at the registry: `openova-bot` (the
  literal string the cloud-init template uses; GitHub validates the token,
  not the username, but this string lands in audit-trail JSON).
- Operator who generated it.

### Apply (the one-liner)

Replace `<GHCR_PULL_TOKEN>` with the token retrieved from 1Password —
**never** paste a real token into git, an issue, a commit message, or a
terminal session that will be transcribed.

```bash
kubectl create secret generic catalyst-ghcr-pull-token \
  --namespace=catalyst \
  --from-literal=token='<GHCR_PULL_TOKEN>' \
  --dry-run=client -o yaml | \
  kubectl apply -f -
```

The `--dry-run=client … | kubectl apply -f -` form is idempotent: a fresh
install creates the Secret; a rotation overwrites the existing one
in-place. The catalyst-api Deployment must be rolled to pick up the new
value:

```bash
kubectl -n catalyst rollout restart deployment/catalyst-api
kubectl -n catalyst rollout status  deployment/catalyst-api
```

(`secretKeyRef`-mounted env vars are NOT auto-refreshed by the Pod —
only volume mounts are. The catalyst-api chart mounts the token as
`env.valueFrom.secretKeyRef`, so a rollout is required.)

### Verify

```bash
# The Secret exists with the expected key.
kubectl -n catalyst get secret catalyst-ghcr-pull-token \
  -o jsonpath='{.data.token}' | base64 -d | wc -c
# (Output: a non-zero byte count. NEVER append `; echo` — that prints
# the token to your terminal.)

# The catalyst-api Pod read it cleanly at startup.
kubectl -n catalyst logs deploy/catalyst-api | grep -i 'ghcr' || \
  echo "no ghcr-related warning — provisioner picked up the token"

# A fresh /api/v1/deployments POST validates without the
# 'CATALYST_GHCR_PULL_TOKEN missing' error (expected for managed-pool
# domain mode).
```

### Rollback

If the new token does not authenticate (typo, wrong scope, expired):

1. Open 1Password's item version history; copy the previous token.
2. Re-run the `kubectl create secret … --dry-run=client | kubectl apply`
   one-liner with the previous token.
3. `kubectl -n catalyst rollout restart deployment/catalyst-api`.
4. File a follow-up issue to investigate why the new token failed.

The previous token remains valid until the next yearly rotation —
GitHub does not invalidate replaced fine-grained tokens automatically.
**Revoke the broken token in the GitHub UI** as a hygiene step once
rollback succeeds.

---

## Hetzner Cloud API token (per Sovereign)

Captured by the wizard's StepProvider, lives in catalyst-api memory only
for the duration of one deployment. NEVER persisted (the
`Request.HetznerToken` field is `json:"-"`; `internal/store.Redact`
overwrites it with `<redacted>` for any record that ends up on disk).

Rotation: per-Sovereign apply. Each `tofu apply` accepts a fresh token;
once `tofu apply` returns, catalyst-api drops the value out of memory
(the Pod restart on next image roll loses the in-memory copy regardless).

If a Hetzner token is suspected of leaking: revoke at
https://console.hetzner.cloud/projects → Security → API tokens. The next
wizard run will accept a fresh one.

---

## Dynadot API key + secret (`dynadot-api-credentials`)

K8s Secret in `openova-system` namespace, keys: `api-key`, `api-secret`,
`domain` (legacy single-domain), `domains` (comma-separated list,
preferred).

**Yearly rotation** via the Dynadot account UI:
1. https://www.dynadot.com → My Account → API Settings → Regenerate.
2. Copy both halves to the 1Password item **Dynadot — OpenOva pool
   domains API credentials**.
3. Apply:

```bash
kubectl create secret generic dynadot-api-credentials \
  --namespace=openova-system \
  --from-literal=api-key='<DYNADOT_API_KEY>' \
  --from-literal=api-secret='<DYNADOT_API_SECRET>' \
  --from-literal=domains='omani.works' \
  --dry-run=client -o yaml | \
  kubectl apply -f -

kubectl -n catalyst         rollout restart deployment/catalyst-api
kubectl -n openova-system   rollout restart deployment/pool-domain-manager
```

The `domains` value is the comma-separated allowlist of pool domains
this account manages. Adding a third pool domain (e.g. `acme.io`) is a
secret update, not a code change — see
[INVIOLABLE-PRINCIPLES.md](./INVIOLABLE-PRINCIPLES.md) #4.

---

## Cross-cutting rules

1. **NEVER print a credential to a terminal.** All retrievals pipe to a
   file (`> /path && chmod 600`) or directly into `kubectl create secret
   --from-literal`. Session transcripts are durable.
2. **NEVER commit a credential.** Use this runbook's `kubectl create
   secret … | kubectl apply` one-liner; the value never touches a file
   the working tree tracks.
3. **NEVER skip the rollout restart.** `secretKeyRef` env vars are
   read at Pod start. A Secret update with no rollout is a silent
   half-rotation: existing Pods serve the old value, new Pods (post next
   evict) serve the new one. The catalyst-api is single-replica with
   strategy `Recreate`, so this is one step.
4. **Log only metadata, never the value.** `kubectl describe secret`
   shows `data: token: <not shown>` — that is intentional. Reading the
   value via `-o jsonpath` and piping to a file is the sanctioned
   confirmation path; piping to `cat`/`echo` is not.

If you accidentally expose a credential — printed to a terminal that
will be transcribed, committed it to a branch, posted it to an issue —
**rotate immediately** following this runbook. Do not try to "quietly
fix it" by editing history; assume the leaked value is captured.
