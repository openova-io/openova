# Agenity NEW dashboard deploy — demo (omantel.biz / kom4dc) — 2026-06-22

Deploy the post-#745 agenity dashboard to the openova demo so the founder
sees the current UI (not the pre-#745 "archaic" one).

## Root cause (founder-confirmed)
The newest dashboard work was stuck on unmerged `agenity-org/agenity` PR #745.
It is now merged to agenity `main` = commit `bcd38b158`. The openova `bp-agenity`
image builds the daemon FROM SOURCE pinned by `AGENITY_REF`, which was `v0.9.4`
— and v0.9.4 predates #745, so the demo served the old dashboard.

## What shipped
- `products/agenity/Containerfile`: `AGENITY_REF` v0.9.4 → `bcd38b158` (the #745
  merge). The fetch stage was rewritten from `git clone --branch` (rejects a
  bare SHA) to `git init` + `git fetch --depth 1 origin <SHA>` + `checkout
  FETCH_HEAD` (accepts a tag, branch, OR a full commit SHA). Still pinned.
- `.github/workflows/agenity-build.yaml`: `AGENITY_REF` default → `bcd38b158`,
  plus an overridable `workflow_dispatch` input.
- `products/agenity/chart/Chart.yaml`: 0.5.1 → 0.5.2 / appVersion 0.9.4 → 0.9.5.
- `products/agenity/chart`: new `daemon.authMode` (default `none`) wiring
  `CHEPHERD_AUTH_MODE` so the in-browser chat box no longer 401s (#4122 STEP 5).

## Image
`ghcr.io/openova-io/bp-agenity:0.9.5` (= `ad16855` = `latest`),
digest `sha256:81db1e15...`, built from `AGENITY_REF=bcd38b158` + openova-mcp
(with #4138's Tier-claim org-scoped-create fix baked in from this branch).

## Deploy (EVS-safe)
HR stays SUSPENDED. `kubectl patch sts` (strategic merge) set image + added
`CHEPHERD_AUTH_MODE=none`, preserving all MUST-PRESERVE env/secrets/volumes
(CHEPHERD_EXTRA_MCP_JSON with OPENOVA_MCP_CONTEXT=organization, OPENOVA_MCP_BEARER,
OPENOVA_MCP_RS256_PUBKEY_PEM, agenity-mcp-bearer secret, host.containers.internal
hostAlias, runAsUser/fsGroup 1000, claude-home + anthropic-creds volumes, the
seeded ~/.claude/.credentials.json, ANTHROPIC_API_KEY unset). Pod restarted.

### kom4dc cold-pull blocker + fix (the slow part)
`bp-agenity` is PRIVATE on ghcr. Harbor's anonymous `proxy-ghcr` proxy-cache
404s a private package's NEW layers, so containerd fell back to a DIRECT
ghcr.io pull which kom4dc's throttled IPv4 egress reset mid-blob (`read tcp …->
185.199.108.154:443: connection reset by peer`) — 15+ min, never completed.
Fix (the documented bastion-harbor-prewarm mechanism, platform/self-sovereign-
cutover step-03): a one-shot skopeo Job native-pushed
`ghcr.io/openova-io/bp-agenity:0.9.5` → `harbor.openova.io/openova-io/bp-agenity:
0.9.5` using the ghcr PAT (`--src-creds`) + Harbor admin (`--dest-creds`,
`--retry-times 5`). Repointed the StatefulSet to the harbor-hosted ref → pod
pulled it in **2.68s** (bastion-local) and went 1/1 Running.

## Running image
StatefulSet image: `harbor.openova.io/openova-io/bp-agenity:0.9.5`
(digest `sha256:81db1e15…`, identical to `ghcr.io/openova-io/bp-agenity:0.9.5`
= `ad16855` = `latest`).

## Evidence
- BEFORE (old dashboard): `agenity-BEFORE-old-dashboard.png` — header
  `… · runtime offline`, GitHub link `github.com/chepherd/chepherd`, every
  `GET /api/v1/*` → **401 Unauthorized**, chat Send disabled.
- AFTER (new dashboard): `agenity-AFTER-new-dashboard.png` — **no "runtime
  offline"**, GitHub link `github.com/agenity-org/agenity` (#4058 rename),
  Team Transcript shows `● operator` present, every `GET /api/v1/*` → **200 OK**.
- Browser chat-box auth (#4122 STEP 5): `agenity-AFTER-chatbox-working-201.png`
  — typed a message, Send enabled, `POST /api/v1/teams/default/messages` →
  **201 Created**, message rendered in transcript. NO 401 anywhere
  (trust mode `CHEPHERD_AUTH_MODE=none`).
- chat→provision prerequisites (#4138 not regressed): `agenity-AFTER-worker-
  spawned.png` + `agenity-worker-pty.png` — `+ spawn agent` spawned a live
  claude-code worker (pid, `● live`); the agent session `.mcp.json` carries the
  `openova` MCP server (`openova-mcp` binary), and the deployed image's
  openova-mcp is built from this branch (#4138 Tier→TierAdmin org-scoped create).
  All MUST-PRESERVE MCP env/secrets/OAuth verified present on the new pod.
