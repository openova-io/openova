# WALK-RUNBOOK-2026-05-20.md — Bulk-closure runbook for the 2026-05-19/20 trust-recovery cycle

**Author**: hatiyildiz (Claude session 2026-05-20)
**Status**: PRE-walk artifact. Substrate (mothership catalyst-api) is wedged on TBD-V15 (#2020). All 42 PRs listed here are currently 🔴 UNVERIFIED. This doc tells the operator EXACTLY what to click / grep / curl to flip each PR to 🟢 VERIFIED-PASS the moment substrate is restored.

**Audience**:
- The human operator walking a fresh prov.
- A read-only Playwright verification agent (per CLAUDE.md §4 rule 7: agents NEVER ship fixes to make their own walk pass).

**Scope**: 42 PRs merged into `openova-io/openova` between **2026-05-19T20:05:10Z** (PR #1987) and **2026-05-20T05:23:19Z** (PR #2061).

> **CLAUDE.md anti-theater rule**: this document is itself NOT a verification. Closing an issue requires `screenshot + non-empty XHR + working downstream artifact` per CLAUDE.md §4. Use this runbook as the choreography; the screenshots / log lines / kubectl outputs are the evidence.

---

## §1 — How to use this runbook

### 1.1 Pre-conditions (gate-keeper checks)

Before running ANY walk in §2:

```bash
# 1. Substrate state — is the orchestrator reachable?
curl -sk -o /dev/null -w "%{http_code}\n" https://console.openova.io/sovereign/api/v1/deployments
# Must return 200, not 502. If 502 → TBD-V15 (#2020) is still active. STOP.

# 2. Mothership CPU headroom
kubectl --kubeconfig ~/.kube/config top nodes
# Need at least ~500m headroom for catalyst-api Pod (100m request) plus rolling buffer.

# 3. No Pending Pods on mothership
kubectl --kubeconfig ~/.kube/config get pods -A --field-selector=status.phase=Pending | grep -v "No resources"
# Empty = good. Otherwise CPU/memory still over-committed.

# 4. Substrate-restore issue authorised
gh issue view 2020 --repo openova-io/openova --json state,labels --jq .
# state should be CLOSED OR labels should include status/in-progress with Path-A authorisation
# comment from founder. If still status/in-progress without authorisation → STOP.
```

If ANY gate-check fails → DO NOT walk. Add a comment to #2020 with the failure mode; wait for fix.

### 1.2 Substrate-restore one-liner (Path A — recommended)

Per TBD-V15 (#2020) §"Fix options" Option A — bump mothership node cpx52 → cpx62. Alternative (Path B — short-term unblock without resize):

```bash
# Scale down the iogrid Deployments wedging the scheduler — frees ~300m CPU.
# Authorised by founder ONLY. NEVER do this without explicit go-ahead.
kubectl --kubeconfig ~/.kube/config -n iogrid scale deploy --all --replicas=0
kubectl --kubeconfig ~/.kube/config -n catalyst rollout status deploy/catalyst-api --timeout=180s
curl -sk -o /dev/null -w "%{http_code}\n" https://console.openova.io/sovereign/api/v1/deployments  # → 200
```

### 1.3 Chart pins the walk MUST target

Provision a fresh prov on **t39** (or t40 — bump with each wipe) with the following pin set live on `main` at time of walk:

| Chart | Min pin | Source PR(s) |
|---|---|---|
| `bp-catalyst-platform` | **1.4.226** (or later) | #1993 / #1996 / #2004 / #2007 / #2008 / #2009 / #2010 / #2011 / #2017 / #2018 / #2029 / #2036 / #2037 / #2043 / #2044 / #2046 / #2050 / #2052 / #2053 |
| `bp-sandbox` | **0.3.6** (or later) | #1987 / #1988 / #1992 / #2017 / #2037 / #2049 / #2051 / #2052 / #2054 |
| `bp-newapi` | **1.4.33** (or later) | #2007 / #2017 / #2037 / #2050 |
| `bp-self-sovereign-cutover` | **0.1.37** (or later) | #2018 / #2036 / #2039 / #2041 / #2045 |
| `bp-flux` (stuck-hr-recovery) | **1.2.4** (or later) | #1991 / #1998 |
| `bp-valkey` | **1.0.2** (or later) | #2007 |
| `bp-kyverno-policies` | **1.0.0** | #2022 / #2023 |

Verify the running pins before walking:

```bash
kubectl --kubeconfig $KUBECONFIG get hr -A -o jsonpath='{range .items[*]}{.metadata.name}{"@"}{.spec.chart.spec.version}{"\n"}{end}' | sort -u
```

If a pin is behind, **the walk's PASS/FAIL for that row is meaningless** — wait for the next bootstrap-kit pin push.

### 1.4 Domain canon (CLAUDE.md §0 — NEVER `openova.io` in tests)

| Layer | Pattern | Example for t39 |
|---|---|---|
| Sovereign | `t<NN>.omani.works` | `console.t39.omani.works` |
| Marketplace | `marketplace.t<NN>.omani.works` | `marketplace.t39.omani.works` |
| Tenant Org (default TLD) | `<slug>.omani.homes` | `console.walk39.omani.homes` |
| Pool TLD alternates | `.omani.rest`, `.omani.trade` | per `sovereign_parent_domains.go:72-77` |

### 1.5 The canonical 2-phase walk (CLAUDE.md §0)

| Phase | Step | URL |
|---|---|---|
| 0a | Operator login | `https://console.t39.omani.works` (PIN to `emrah.baysal@openova.io`) |
| 0b | Navigate **BSS menu** (NOT `admin.<fqdn>`) | sidebar → BSS |
| 0c | Issue voucher | `POST /api/v1/billing/vouchers/issue` |
| 1a | Voucher email received | recipient inbox |
| 1b | Customer redeems → checkout → picks Postgres-backed bundle | `https://marketplace.t39.omani.works/redeem/?code=<CODE>` |
| 1c | Org provisioned | `https://console.<orgslug>.omani.homes` |
| 2a | Tenant login | `console.<orgslug>.omani.homes` |
| 2b | Open Sandbox | sidebar → Sandbox |
| 2c | Pick `qwen-code` agent | dropdown |
| 2d | MCP auto-mount | xterm panel renders; `mcp` listing returns 49 tools |
| 2e | Prompt qwen-code to provision an additional Postgres-backed app | new app reachable at `<newapp>.<orgslug>.omani.homes` |

Each row in §2 below cites which step it advances.

### 1.6 Evidence shape required by EVERY closure

For every walk in §2 you MUST produce:

1. A **screenshot** in `.playwright-mcp/t<NN>-<surface>-<YYYY-MM-DD>.png`.
2. A **log line OR shell output** captured to `.playwright-mcp/t<NN>-<surface>-<YYYY-MM-DD>.log`.
3. A `gh issue comment` citing both artifacts.

No comment without ALL THREE.

---

## §2 — Per-PR walk steps

### Pillar 1 ships (Marketplace + voucher onboarding on Nova Cloud)

#### Row 1 — PR #2038 — feat(marketplace-ui): render configSchema fields on AppDetail

| Field | Value |
|---|---|
| Refs | #2026 (TBD-V18) |
| Pillar/step | **Pillar 1 / Step 2** ("App card opens; configSchema renders") |
| Pre-condition | Fresh prov with marketplace catalog seeded; Postgres-backed bundle visible |
| Walk steps | 1. Anon GET `https://marketplace.t39.omani.works/`<br>2. Click the canonical Postgres-backed bundle card<br>3. Observe AppDetail.svelte renders form widgets under Description/Features for `replicas` / `disk_gb` / `backups_enabled` / etc. |
| Wire-level check | `curl -s https://marketplace.t39.omani.works/api/apps \| jq '.[].config_schema'` returns non-null arrays of `{key, label, type, default, min, max}` |
| Code refs | `core/marketplace/src/lib/api.ts` (`ConfigField` interface), `core/marketplace/src/components/AppDetail.svelte` (widget renderer) |
| Screenshot | `.playwright-mcp/t39-marketplace-appdetail-configschema.png` |

#### Row 2 — PR #2043 — feat(marketplace-ui): thread configSchema form values into install POST

| Field | Value |
|---|---|
| Refs | #2026 (TBD-V18) |
| Pillar/step | **Pillar 1 / Step 2** (extends Row 1 — wire customer choices into the POST body) |
| Pre-condition | Row 1 form renders |
| Walk steps | 1. On AppDetail, change `replicas: 1 → 3` and `disk_gb: 2 → 10`<br>2. Click "Add to cart"<br>3. Proceed to checkout<br>4. Browser DevTools → Network → `POST /api/tenant/orgs`<br>5. Inspect request body |
| Wire-level check | POST body MUST contain `app_configs.postgres.{replicas: 3, disk_gb: 10, backups_enabled: …}` byte-for-byte |
| Code refs | `core/marketplace/src/lib/cart.ts` (`CartState.appConfigs`), `core/marketplace/src/components/CheckoutStep.svelte`, `core/services/tenant/store.Tenant.AppConfigs` |
| Screenshot + capture | `.playwright-mcp/t39-marketplace-appconfigs-post.png` + `t39-marketplace-appconfigs-post.har` |
| Caveat | Per PR body §"Scope NOT in this PR": HelmRelease-values BINDING is gated on TBD-V26 (#2040). Row 12 (PR #2053) below covers the binding. |

#### Row 3 — PR #2053 — feat(provisioning): thread Org.spec.appConfigs into per-app HelmRelease.values

| Field | Value |
|---|---|
| Refs | #2042 (TBD-V27) |
| Pillar/step | **Pillar 1 / Step 5** (provisioned PG cluster honours customer's picks) |
| Pre-condition | Rows 1 + 2 green; a tenant Org has been created with non-default `app_configs` |
| Walk steps | 1. After tenant materialisation, target the tenant vCluster:<br>`kubectl --context vc-walk39-omani-homes get hr -n apps postgres -o yaml`<br>2. Verify `spec.values.replicas` matches what was picked on AppDetail<br>3. `kubectl get deploy -n apps -l app=postgres -o jsonpath='{.items[0].spec.replicas}'` matches the same number<br>4. `kubectl get pvc -n apps -l app=postgres -o jsonpath='{.items[0].spec.resources.requests.storage}'` matches the picked `disk_gb` |
| Log grep | `kubectl -n catalyst logs deploy/provisioning \| grep "generatePostgres.*replicas="` |
| Code refs | `core/services/provisioning/manifests/postgres.go`, `core/services/billing/handlers/order.go:dispatchOrderPlaced`, `GET /tenant/internal/tenants/{id}/app-configs` |
| Screenshot | `.playwright-mcp/t39-provisioning-appconfigs-applied.png` (yq output of the rendered HR) |

#### Row 4 — PR #2029 — fix(catalyst-ui): wizard StepSuccess CTA → BSS menu in operator console

| Field | Value |
|---|---|
| Refs | (no explicit `Closes`; doc-comment anti-canon ref) — internal "Pillar-1 BSS audit 2026-05-20" |
| Pillar/step | **Pillar 1 / Step 0c** (Operator wizard final-step CTA points at canonical BSS, not `admin.<fqdn>`) |
| Pre-condition | First-install wizard StepSuccess reachable (fresh prov) |
| Walk steps | 1. After Phase-0 first-time setup wizard finishes, observe StepSuccess page<br>2. Click "Issue first voucher" CTA<br>3. URL MUST be `https://console.t39.omani.works/bss/vouchers` — NEVER `admin.t39.omani.works/...` |
| DOM check | `curl -s https://console.t39.omani.works/bss/vouchers -o /dev/null -w "%{http_code}\n"` returns 200 |
| Code refs | `products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepSuccess.tsx`, `router.tsx:1576` (`/bss/vouchers` → `VouchersPage`) |
| Screenshot | `.playwright-mcp/t39-wizard-stepsuccess-bss-cta.png` |

#### Row 5 — PR #1993 — fix(tenant-route): restore console.<slug>.<parent> prefix + drop .openova.io hardcode

| Field | Value |
|---|---|
| Closes | #1990 (TBD-A67) |
| Pillar/step | **Pillar 1 / Step 1c** (tenant console reachable at canonical URL shape) |
| Pre-condition | Tenant Org `walk39` provisioned |
| Walk steps | 1. `kubectl get httproute -n tenant-walk39 -o yaml \| grep "hostname"`<br>2. Hostname MUST be `console.walk39.omani.homes` — NOT `walk39.omani.homes`, NOT anything `*.openova.io`<br>3. Browser GET `https://console.walk39.omani.homes/` → 200 with tenant dashboard |
| Email-side check | Onboarding email body MUST contain `WorkspaceURL=https://console.walk39.omani.homes` (read sent-mail log: `kubectl -n catalyst logs deploy/notification \| grep WorkspaceURL`) |
| Code refs | `core/controllers/organization/internal/controller/tenant_route.go:113`, `products/catalyst/chart/templates/sme-services/tenant-public-routes.yaml:82`, `core/services/notification/handlers/enrich.go` |
| Screenshot | `.playwright-mcp/t39-tenant-console-canonical-url.png` |

#### Row 6 — PR #1996 — fix: purge 5 .openova.io leaks (tenant users reach Sovereign not mothership)

| Field | Value |
|---|---|
| Closes | #1994 (TBD-A68) |
| Pillar/step | **Pillar 1 + Pillar 5** (no tenant-facing URL leaks to mothership) |
| Pre-condition | Tenant Org provisioned |
| Walk steps | 1. Trigger PIN email login: `curl -s -X POST https://console.t39.omani.works/api/v1/auth/pin/start -d '{"email":"test@example.com"}'`<br>2. Read PIN email body (Stalwart IMAPS `mail.openova.io:993`, user `emrah.baysal@openova.io`)<br>3. Login URL MUST be `https://console.t39.omani.works/login` — NEVER `console.openova.io/sovereign/login` (that's only emitted on the literal mothership) |
| Wire-level check | `kubectl get cm -n catalyst sme-services-config -o yaml \| grep MARKETPLACE` — values must reference `marketplace.t39.omani.works` |
| Code refs | `products/catalyst/bootstrap/api/internal/handler/auth.go:pinEmailLoginURL`, `core/console/src/lib/config.ts`, `products/catalyst/chart/templates/sme-services/configmap.yaml` |
| Screenshot | `.playwright-mcp/t39-pin-email-canonical-url.png` (email body) |

#### Row 7 — PR #2010 — fix(marketplace): post-checkout redirects to console.<slug>.<pool-tld>

| Field | Value |
|---|---|
| Closes | #2001 (TBD-V10) |
| Pillar/step | **Pillar 1 / Step 1c** (post-checkout lands on tenant console, not operator) |
| Pre-condition | Voucher redemption flow available; tenant slug chosen |
| Walk steps | 1. Anon GET `https://marketplace.t39.omani.works/redeem/?code=<CODE>` (canonical URL — slash before `?` per `templates.go:295`)<br>2. Enter tenant slug `walk39`<br>3. Complete signup → checkout<br>4. After successful checkout, `window.location.href` MUST become `https://console.walk39.omani.homes/...` — NOT `https://console.t39.omani.works/...` |
| LocalStorage check | DevTools → Application → Local Storage: `sme-active-org-slug = "walk39"` |
| Code refs | `core/marketplace/src/lib/config.ts::deriveConsoleURL(slug?)`, `composeTenantConsoleURL`, `CheckoutStep.svelte` |
| Screenshot | `.playwright-mcp/t39-marketplace-post-checkout-redirect.png` (URL bar visible) |

#### Row 8 — PR #2011 — fix(billing): transactional voucher redemption — only decrement on order.placed success

| Field | Value |
|---|---|
| Closes | #2000 (TBD-V9) |
| Pillar/step | **Pillar 1 / Step 1b** (failure-mode hardening — voucher not double-spent on Stripe-503 paths) |
| Pre-condition | Voucher `WALK-T39-NEG` issued with `times_redeemed=0, max=1`; Stripe unconfigured (the t38 walk path) |
| Walk steps | 1. Customer attempts `/redeem` + `/checkout` with the voucher on a 50-OMR order — Stripe 503 fires<br>2. Verify NO promotion + NO voucher decrement:<br>`PGPASSWORD=… psql -h sme-db -U billing -c "SELECT times_redeemed, used FROM promo_codes WHERE code='WALK-T39-NEG';"` → `times_redeemed=0, used=false`<br>3. `psql … "SELECT * FROM promo_redemptions WHERE code='WALK-T39-NEG';"` → 0 rows<br>4. `psql … "SELECT * FROM credit_ledger WHERE reason='promo:WALK-T39-NEG' AND order_id IS NULL;"` → 0 rows<br>5. Idempotency: retry same flow — STILL no decrement |
| Code refs | `core/services/billing/store/promo.go::RollbackPromoCodeRedemption`, `core/services/billing/handlers/Checkout` |
| Screenshot | `.playwright-mcp/t39-voucher-rollback-pgrows.png` (psql output) |

#### Row 9 — PR #2009 — fix(sme-notification): align JWT signing secret with catalyst-api bridge (voucher email)

| Field | Value |
|---|---|
| Closes | #1999 (TBD-V8) |
| Pillar/step | **Pillar 1 / Step 1a** (voucher email actually delivered) |
| Pre-condition | Operator issues voucher to `hatice.yildiz@openova.io` (use a non-emrah inbox; **NEVER touch emrah.baysal@ secrets** per memory rule) |
| Walk steps | 1. Operator POST `https://console.t39.omani.works/api/v1/billing/vouchers/issue` with `recipient=hatice.yildiz@openova.io`<br>2. HTTP 200 returned<br>3. `kubectl -n catalyst logs deploy/notification --tail=200 \| grep -i voucher` MUST show `email sent ... template=voucher-issued status=200`<br>4. IMAP poll of recipient mailbox shows the email landed |
| Anti-pattern (failure mode) | If log shows `status=401` → billing→notification Authorization header still missing → PR ineffective. |
| Code refs | `core/services/billing/handlers/vouchers.go::sendVoucherIssuedEmail`, `core/services/shared/middleware/jwt.go`, `core/services/billing/main.go` (`JWTSecret` wire) |
| Screenshot | `.playwright-mcp/t39-voucher-email-sent-200.png` (notification log + IMAP screenshot) |

---

### Pillar 4 ships (Sandbox + qwen-code + auto-mounted MCP with full org knowledge)

#### Row 10 — PR #1987 — fix(sandbox-controller): emit canonical SANDBOX_* env vars for MCP plugin

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) — sub-break **B4** |
| Pillar/step | **Pillar 4 / Step 2d** (MCP env var contract honoured) |
| Pre-condition | Sandbox CR created for tenant `walk39`, controller reconciles |
| Walk steps | 1. `kubectl get sandbox -n catalyst-system walk-qwen-39 -o yaml` — CR exists<br>2. `kubectl get sts -n sandbox-<owner-uid> pty-server -o yaml \| grep -E 'SANDBOX_(ORG_ID\|SOVEREIGN_FQDN\|SANDBOX_ID\|NAMESPACE\|TENANT_ID\|GITEA_BASE_URL\|GITEA_TOKEN\|KEYCLOAK_ADMIN_URL\|STORAGE_S3_ENDPOINT\|MARKETPLACE_API_URL)'` — ALL 10 canonical env vars MUST be present<br>3. Cross-check against the reader: `products/sandbox/mcp-server/internal/tools/env.go` |
| Code refs | `core/controllers/sandbox/internal/gitops/manifests.go`, `products/sandbox/mcp-server/internal/tools/env.go` |
| Screenshot | `.playwright-mcp/t39-sandbox-mcp-envblock.png` (kubectl describe) |

#### Row 11 — PR #1988 — feat(sandbox-pty-server): bundle qwen-code + claude-code + aider + opencode in agent-runner image

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) — sub-break **B1** |
| Pillar/step | **Pillar 4 / Step 2c** (`qwen-code` binary actually present on PATH) |
| Pre-condition | pty-server image tag matches PR #1988 SHA (verify in chart values `sandbox.image.tag` or via `kubectl describe sts pty-server`) |
| Walk steps | 1. `kubectl exec -n sandbox-<uid> sts/pty-server -- which qwen-code claude-code opencode aider` MUST return 4 paths<br>2. `kubectl exec -n sandbox-<uid> sts/pty-server -- qwen-code --version` returns a real version, NOT ENOENT |
| Anti-pattern | Image still on `distroless/static-debian12:nonroot` → binaries missing → ENOENT exit code 127 |
| Code refs | `products/sandbox/pty-server/Dockerfile` (base `node:22-bookworm-slim`) |
| Screenshot | `.playwright-mcp/t39-sandbox-agent-cli-binaries.png` (terminal output) |

#### Row 12 — PR #1992 — feat(sandbox-pty-server): agent catalogue + lazy-spawn on attach

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) — sub-break **B3** |
| Pillar/step | **Pillar 4 / Step 2b / 2c** (lazy-spawn on `WS /sessions/<id>/attach`) |
| Pre-condition | Sandbox CR created with `spec.agentCatalogue: [qwen-code]`; pty-server Pod running |
| Walk steps | 1. Open `https://console.walk39.omani.homes/sandbox/walk-qwen-39`<br>2. xterm.js panel renders within ~3s (not a blank canvas)<br>3. `kubectl -n sandbox-<uid> logs sts/pty-server \| grep -E "agentcatalog: lookup slug=qwen-code"`<br>4. Browser DevTools → WS: `/sessions/walk-qwen-39/attach` returns 101 (NOT 404) |
| Code refs | `products/sandbox/pty-server/internal/agentcatalog/agentcatalog.go`, `products/sandbox/pty-server/internal/server/routes.go::createRequest.agent` |
| Screenshot | `.playwright-mcp/t39-sandbox-xterm-attach.png` (xterm prompt visible) |

#### Row 13 — PR #2051 — fix(sandbox-controller): remove MCP Deployment, launch via subprocess from agent

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) — sub-break **B2** EOF-crash |
| Pillar/step | **Pillar 4 / Step 2d** (MCP server runs as agent subprocess via stdio, not as broken Deployment) |
| Pre-condition | Sandbox CR created |
| Walk steps | 1. `kubectl get deploy -n sandbox-<uid>` MUST NOT return any MCP Deployment (no `mcp-deployment`)<br>2. `kubectl exec -n sandbox-<uid> sts/pty-server -- ls /usr/local/bin/openova-sandbox-mcp` → file present<br>3. Inside an agent session: `cat /workspace/.mcp.json \| jq .mcpServers["openova-sandbox-mcp"]` shows command `/usr/local/bin/openova-sandbox-mcp`<br>4. After agent launches, `pgrep -f openova-sandbox-mcp` finds a CHILD process of the agent |
| Code refs | `core/controllers/sandbox/internal/gitops/manifests.go` (delete `mcpDeploymentTemplate`), `products/sandbox/pty-server/Dockerfile` (binary bundled) |
| Screenshot | `.playwright-mcp/t39-mcp-subprocess-tree.png` (ps tree) |

#### Row 14 — PR #2049 — feat(sandbox-controller): inject mcp.json config so agents auto-discover openova-sandbox-mcp

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) |
| Pillar/step | **Pillar 4 / Step 2d** (mcp.json mounted at every canonical agent config path) |
| Pre-condition | Sandbox CR created |
| Walk steps | 1. `kubectl get cm -n sandbox-<uid> sandbox-mcp-config -o yaml` — single `mcp.json` key, canonical MCP `mcpServers` schema<br>2. `kubectl exec -n sandbox-<uid> sts/pty-server -- ls -la /workspace/.mcp.json /home/node/.claude.json /home/node/.qwen/settings.json /workspace/.cursor/mcp.json` — ALL 4 mount points present (aider intentionally inert) |
| Code refs | `core/controllers/sandbox/internal/gitops/manifests.go` (`sandbox-mcp-config` ConfigMap + subPath projections) |
| Screenshot | `.playwright-mcp/t39-mcp-config-mounts.png` |

#### Row 15 — PR #2052 — feat(sandbox-controller): dispatch on Sandbox.spec.agent for per-agent runtime

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4) — A4 audit finding |
| Pillar/step | **Pillar 4 / Step 2c** (FE agent dropdown is no longer cosmetic for non-claude-code slugs) |
| Pre-condition | Sandbox CR with `spec.agentCatalogue=["qwen-code"]` |
| Walk steps | 1. `kubectl get sts -n sandbox-<uid> pty-server -o yaml \| grep -A1 SANDBOX_DEFAULT_AGENT` → value `qwen-code`<br>2. WS attach lands inside a `qwen-code` REPL, not blank xterm or `claude` REPL<br>3. Repeat for `claude-code`, `opencode`, `aider` — each should spawn its own REPL |
| Code refs | `core/controllers/sandbox/internal/gitops/manifests.go::Inputs.DefaultAgent`, `core/controllers/sandbox/internal/controller/sandbox_controller.go` (project `sb.Spec.AgentCatalogue[0]`) |
| Screenshot | `.playwright-mcp/t39-sandbox-qwen-code-repl.png` (xterm with qwen prompt) |

#### Row 16 — PR #2017 — fix(sandbox-controller): correct NEWAPI_BASE_URL to actual bp-newapi service name

| Field | Value |
|---|---|
| Refs | TBD-V14 — caught on t38 walk |
| Pillar/step | **Pillar 4 / Step 2c** (TokenMint succeeds — was the live t38 blocker) |
| Pre-condition | bp-newapi Service `newapi-bp-newapi.newapi.svc.cluster.local:3000` reachable in-cluster |
| Walk steps | 1. `kubectl get cm -n catalyst sandbox-controller-config -o yaml \| grep newapiBaseURL` → `http://newapi-bp-newapi.newapi.svc.cluster.local:3000` (NOT `newapi.newapi.svc.cluster.local:3000`)<br>2. Sandbox CR status: `Ready=True/Reconciled` — NOT `TokenMintFailed: dial tcp: lookup newapi.newapi.svc.cluster.local on 10.96.0.10:53: no such host`<br>3. `kubectl logs -n catalyst deploy/sandbox-controller \| grep "POST /admin/tokens/sandbox" \| tail -1` → 200 |
| Code refs | `platform/sandbox/chart/values.yaml:43`, `platform/sandbox/chart/templates/deployment.yaml:88-89`, `clusters/_template/bootstrap-kit/19a-bp-sandbox.yaml` |
| Screenshot | `.playwright-mcp/t39-sandbox-tokenmint-ready.png` (kubectl describe sandbox) |

#### Row 17 — PR #2037 — fix(sandbox-controller): add 4 missing SANDBOX_* env vars + LLM_GATEWAY_TOKEN case fix

| Field | Value |
|---|---|
| Refs | #2032 (TBD-V21) |
| Pillar/step | **Pillar 4 / Step 2d** (MCP tools `marketplace.*` + JWT auth gate work) |
| Pre-condition | Sandbox CR + per-Sandbox Secret `sandbox-tokens` with `LLM_GATEWAY_TOKEN` key |
| Walk steps | 1. `kubectl get sts -n sandbox-<uid> pty-server -o jsonpath='{.spec.template.spec.containers[0].env}' \| jq '.[] \| select(.name \| startswith("SANDBOX_"))'` → MUST include `SANDBOX_TOKEN`, `SANDBOX_JWT_SECRET`, `SANDBOX_REPOS` (SANDBOX_KUBECONFIG intentionally absent — see PR body)<br>2. Inside agent: `mcp call marketplace.apps.list` returns a non-error catalog list (NOT `SANDBOX_TOKEN not set`)<br>3. Verify `secretKeyRef.key` for `LLM_GATEWAY_TOKEN` is uppercase (not lowercase `llm-gateway-token`) |
| Code refs | `products/sandbox/mcp-server/internal/tools/env.go`, `core/controllers/sandbox/internal/gitops/manifests.go` |
| Screenshot | `.playwright-mcp/t39-sandbox-token-env.png` |

#### Row 18 — PR #2054 — feat(sandbox/pty-server): configurable ring buffer, default 1 MiB

| Field | Value |
|---|---|
| Refs | #1986 (TBD-P4 F1) |
| Pillar/step | **Pillar 4** (supporting — multi-device "close laptop, open phone" handoff per Scene 6) |
| Pre-condition | Sandbox session running with active output |
| Walk steps | 1. `kubectl get sts -n sandbox-<uid> pty-server -o yaml \| grep SANDBOX_RING_BUFFER_BYTES` — if controller's `Inputs.RingBufferBytes` set, env present; else default 1 MiB applies in pty-server<br>2. `kubectl exec ... -- sh -c 'echo $SANDBOX_RING_BUFFER_BYTES'` — non-empty if overridden<br>3. Generate ~600 KiB of agent output, detach, re-attach → replay buffer survives (would have rolled at old 256 KiB) |
| Anti-pattern | Set `SANDBOX_RING_BUFFER_BYTES=999999999` → must clamp to 16 MiB ceiling + log warn |
| Code refs | `products/sandbox/pty-server/internal/session/session.go::DefaultRingBytes` (1 MiB), `MaxRingBytes` (16 MiB) |
| Screenshot | `.playwright-mcp/t39-sandbox-ring-buffer-replay.png` |

#### Row 19 — PR #2008 — fix(bp-sme): wait for gitea user-bootstrap before provisioning starts

| Field | Value |
|---|---|
| Closes | #2002 (TBD-V11) |
| Pillar/step | **Pillar 1 / Step 1c + Pillar 4 supporting** (first tenant journey no longer 401s on Gitea bootstrap placeholder token) |
| Pre-condition | Fresh prov with bp-self-sovereign-cutover step 09 (gitea-token-mint) NOT yet fired |
| Walk steps | 1. `kubectl get pod -n sme -l app=provisioning` — Pod MUST be `Init:0/1` until step 09 fires (NOT 1/1 prematurely)<br>2. `kubectl describe pod -n sme -l app=provisioning \| grep -A5 wait-for-cutover-token` — init container running, polling Secret `sme/provisioning-github-token` annotation `catalyst.openova.io/token-source: self-sovereign-cutover-step-09`<br>3. AFTER step 09 fires (manual `kubectl annotate secret ...` or wait for cutover): Pod transitions to 1/1 Running<br>4. First tenant Org creation now succeeds without `HTTP 401 user does not exist [uid: 0, name: ""]` |
| Code refs | `products/sme/chart/templates/provisioning.yaml` (init container `wait-for-cutover-token`) |
| Screenshot | `.playwright-mcp/t39-sme-provisioning-init-guard.png` |

#### Row 20 — PR #2004 — fix(chart): bump organization-controller pin 72e3f08 -> c9b58ea

| Field | Value |
|---|---|
| Closes | #1997 (TBD-A68 follow-up) |
| Pillar/step | **Pillar 1 / Step 1c** (Organization CR Ready, not GiteaOrgFailed) |
| Pre-condition | Tenant Org CR `walk39` created |
| Walk steps | 1. `kubectl describe organization walk39` — `status.conditions[Ready] = True`, NOT `Ready=False, reason=GiteaOrgFailed`<br>2. `kubectl logs -n catalyst deploy/organization-controller \| grep "EnsureOrg"` → POSTs `/api/v1/orgs` (NOT `/api/v1/admin/orgs`); HTTP 201, NOT 405<br>3. Image tag verification: `kubectl get deploy -n catalyst organization-controller -o jsonpath='{.spec.template.spec.containers[0].image}'` includes `c9b58ea` (NOT `72e3f08`) |
| Code refs | `products/catalyst/chart/values.yaml:369` (image.tag), upstream code in PR #1910 |
| Screenshot | `.playwright-mcp/t39-org-controller-ready.png` |

#### Row 21 — PR #2050 — fix(catalyst-bootstrap-api): wire CATALYST_NEWAPI_ADMIN_TOKEN + correct CATALYST_NEWAPI_ADDR

| Field | Value |
|---|---|
| Refs | #2021 (ADR-0003 §3.2 NewAPI admin hook) |
| Pillar/step | **Pillar 4 supporting** (`POST /api/v1/sme/users` now lights up — was returning the `newapi client not wired` sentinel) |
| Pre-condition | bp-newapi running; Secret `catalyst-newapi-admin-token` reflected into `catalyst` ns |
| Walk steps | 1. `kubectl get deploy -n catalyst catalyst-api -o yaml \| grep -E "CATALYST_NEWAPI_(ADDR\|ADMIN_TOKEN)"` — both present; ADDR literal `http://newapi-bp-newapi.newapi.svc.cluster.local:3000`<br>2. `kubectl get secret -n catalyst catalyst-newapi-admin-token -o jsonpath='{.metadata.annotations.reflector\.v1\.k8s\.emberstack\.com/reflects}'` — present (reflector working)<br>3. `curl -sk -X POST https://console.t39.omani.works/api/v1/sme/users -H "Authorization: Bearer …" -d '{"email":"test@x"}'` — NOT `newapi client not wired`; returns 201 with user shape |
| Code refs | `products/catalyst/chart/templates/api-deployment.yaml`, `platform/newapi/chart/templates/external-secret.yaml` (reflector annotations) |
| Screenshot | `.playwright-mcp/t39-catalyst-api-newapi-wired.png` |

---

### Pillar 5 ships (Sovereign independence post-cutover — Principle #11)

#### Row 22 — PR #2039 — feat(self-sovereign-cutover): add step 10 — pivot vCluster HelmReleases to Sovereign Harbor

| Field | Value |
|---|---|
| Refs | #2034 (TBD-V24 MISS-1) |
| Pillar/step | **Pillar 5** (no tether to `harbor.openova.io` for vCluster control-plane images post-cutover) |
| Pre-condition | bp-self-sovereign-cutover chart 0.1.37+; cutover has reached/passed step 10 |
| Walk steps | 1. `kubectl get cm -n catalyst self-sovereign-cutover-status -o yaml \| grep -E "step\.vcluster-registry-pivot"` → result=success<br>2. `for hr in bp-mgmt-vcluster bp-rtz-vcluster bp-dmz-vcluster; do kubectl get hr $hr -A -o yaml \| grep "image:"; done` → ALL must read `harbor.t39.omani.works/proxy-ghcr/loft-sh/vcluster:...` (NOT `harbor.openova.io/...`)<br>3. The Phase-2 Gitea commit landed: `git -C /var/lib/gitea/repositories/openova/openova/clusters/t39.omani.works/` `log --oneline` shows the `crossplane-provider-pivot` / `vcluster-registry-pivot` commits |
| Code refs | `platform/self-sovereign-cutover/chart/templates/10-vcluster-registry-pivot-job.yaml` |
| Screenshot | `.playwright-mcp/t39-cutover-step10-vcluster-pivot.png` |

#### Row 23 — PR #2041 — fix(self-sovereign-cutover): strip mothership-side auths from ghcr-pull Secret on cutover

| Field | Value |
|---|---|
| Refs | #2034 (TBD-V24 MISS-2) |
| Pillar/step | **Pillar 5** (`ghcr-pull` Secret carries auth for ONLY the local Sovereign Harbor) |
| Pre-condition | Cutover step 06 has run |
| Walk steps | 1. `kubectl get secret -n flux-system ghcr-pull -o jsonpath='{.data.\.dockerconfigjson}' \| base64 -d \| jq '.auths \| keys'` → MUST return `["harbor.t39.omani.works"]` ONLY<br>2. Specifically MUST NOT include `ghcr.io` or `harbor.openova.io`<br>3. Idempotent: re-run cutover step 06 — Secret resourceVersion bumps OR stays (no spurious churn) |
| Code refs | `platform/self-sovereign-cutover/chart/templates/06a-bp-self-sovereign-cutover.yaml` (Phase-0 jq strip), `.Values.harbor.mothershipAuthsToStrip` |
| Screenshot | `.playwright-mcp/t39-ghcr-pull-stripped.png` (jq output) |

#### Row 24 — PR #2045 — feat(self-sovereign-cutover): step 11 — pivot Crossplane Provider CRs to Sovereign Harbor xpkg mirror

| Field | Value |
|---|---|
| Refs | #2034 (TBD-V24 MISS-3) |
| Pillar/step | **Pillar 5** (Crossplane Providers fetch xpkg from local Harbor, not `xpkg.upbound.io`) |
| Pre-condition | Cutover step 11 has run |
| Walk steps | 1. `kubectl get providers.pkg.crossplane.io -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.package}{"\n"}{end}'` → ALL 3 Provider CRs MUST point at `harbor.t39.omani.works/proxy-xpkg/...` (NOT `xpkg.upbound.io/...`)<br>2. `kubectl get cm -n catalyst self-sovereign-cutover-status -o yaml \| grep -E "step\.crossplane-provider-pivot"` → result=success<br>3. `kubectl describe pod -n crossplane-system -l pkg.crossplane.io/provider \| grep "Pulling image"` references the local harbor |
| Code refs | `platform/self-sovereign-cutover/chart/templates/11-crossplane-provider-pivot-job.yaml` |
| Screenshot | `.playwright-mcp/t39-cutover-step11-crossplane-pivot.png` |

#### Row 25 — PR #2036 — fix(self-sovereign-cutover): correct totalSteps from "8" to "9" in status ConfigMap

| Field | Value |
|---|---|
| Refs | #2035 |
| Pillar/step | **Pillar 5** (operator-visible UI shows correct denominator) |
| Pre-condition | bp-self-sovereign-cutover 0.1.34+ |
| Walk steps | 1. IMMEDIATELY after chart install (pre-trigger) — `kubectl get cm -n catalyst self-sovereign-cutover-status -o jsonpath='{.data.totalSteps}'` → `"9"` (NOT `"8"`)<br>2. UI in operator console showing cutover progress reads `<N>/9` from t=0, never wrongly `<N>/8` |
| Code refs | `platform/self-sovereign-cutover/chart/templates/09-cutover-status-configmap.yaml:46` |
| Screenshot | `.playwright-mcp/t39-cutover-totalsteps-9.png` |

#### Row 26 — PR #2018 — fix(self-sovereign-cutover): make state-resume idempotent across orchestrator restart

| Field | Value |
|---|---|
| Refs | TBD-V13 (#2016) — caught on t38 |
| Pillar/step | **Pillar 5** (cutover survives catalyst-api Pod restart mid-flight) |
| Pre-condition | Cutover in flight (any step `result=running`) |
| Walk steps | 1. Mid-cutover, `kubectl rollout restart -n catalyst deploy/catalyst-api`<br>2. New Pod logs at startup: `kubectl logs -n catalyst deploy/catalyst-api --tail=200 \| grep -i "ResumeInterruptedCutover"` → fires, resets `running` rows to `""`, re-spawns `runCutover`<br>3. Cutover advances past the previously-stranded step (step 5 → step 6 → … → step 11) within ~5min<br>4. `kubectl get cm -n catalyst self-sovereign-cutover-status -o jsonpath='{.data.cutoverComplete}'` → `"true"` |
| Anti-pattern | If cutover stays at `currentStep=helmrepository-patches, result=running` forever after Pod restart → resume engine inactive. |
| Code refs | `products/catalyst/bootstrap/api/internal/handler/cutover.go::Handler.ResumeInterruptedCutover`, `cmd/api/main.go` (wire just before `ListenAndServe`) |
| Screenshot | `.playwright-mcp/t39-cutover-resume-advance.png` (sequential `cutoverComplete` transitions) |

---

### Infra / CI ships (build pipeline + cluster-state hygiene)

#### Row 27 — PR #1991 — fix(bp-flux-stuck-hr-recovery): detect+correct deployed-but-unknown-Ready HRs

| Field | Value |
|---|---|
| Refs | #1989 (TBD-A66) |
| Pillar/step | **Infra** (helm-controller Ready-Unknown self-heal) |
| Pre-condition | At least one HR in `Ready=Unknown` for >300s with `.status.history[0].status=deployed` |
| Walk steps | 1. Wait for CronJob `bp-flux-stuck-hr-recovery` to fire (every 5min by default)<br>2. `kubectl logs -n flux-system job/<latest> \| grep "\[A66\]"` → 3 structured lines: detection / success / failure<br>3. After fire: `kubectl get hr -A -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' \| grep -v True \| grep -v False` — empty (no Unknown HRs left) |
| Code refs | `platform/flux/chart/templates/helm-release-stuck-recovery.yaml` |
| Screenshot | `.playwright-mcp/t39-flux-a66-recovery.png` |

#### Row 28 — PR #1998 — fix(bp-flux-stuck-hr-recovery): grant helmreleases/status patch RBAC + log stderr

| Field | Value |
|---|---|
| Closes | #1995 |
| Pillar/step | **Infra** (PR #1991 actually visible — patch errors no longer silent) |
| Pre-condition | bp-flux 1.2.4+ |
| Walk steps | 1. `kubectl get clusterrole bp-flux-stuck-hr-recovery -o yaml \| grep -A2 "helmreleases/status"` → patch+update verbs present<br>2. CronJob log lines from a real fire MUST contain `[A66] HR <ns>/<name> patched to Ready=True` on success OR `[A66] HR <ns>/<name> patch FAILED: <stderr>` with NON-EMPTY stderr captured on failure |
| Code refs | `platform/flux/chart/templates/helm-release-stuck-recovery.yaml` (stderr to `mktemp /tmp/a66-patch-err.*`) |
| Screenshot | `.playwright-mcp/t39-flux-a66-rbac-stderr.png` |

#### Row 29 — PR #2007 — fix(bp-valkey): allow empty password — unblocks bp-newapi

| Field | Value |
|---|---|
| Closes | #2003 (TBD-V12) |
| Pillar/step | **Pillar 4 supporting** (newapi/MCP work) |
| Pre-condition | bp-valkey 1.0.2+; bp-newapi consuming the no-auth URL |
| Walk steps | 1. `kubectl get pod -n valkey -l app.kubernetes.io/name=valkey -o jsonpath='{.items[0].spec.containers[0].env}' \| jq '.[] \| select(.name=="ALLOW_EMPTY_PASSWORD")'` → `"yes"`<br>2. `kubectl get pod -n newapi -l app.kubernetes.io/name=newapi` → Running 2/2; restarts low (NOT CrashLoopBackOff with `NOAUTH Authentication required`)<br>3. `kubectl logs -n newapi sts/newapi --tail=20` → no Redis-ping failures |
| Code refs | `platform/valkey/chart/values.yaml` (`valkey.auth.enabled: false`), `core/services/shared/db/valkey.go` (no-auth fall-through) |
| Screenshot | `.playwright-mcp/t39-valkey-empty-pass-newapi-running.png` |

#### Row 30 — PR #2005 — fix(build-organization-controller): add missing auto-bump pipeline + pkg/** path filter

| Field | Value |
|---|---|
| Refs | #1997 |
| Pillar/step | **CI infra** (recurrence-guard for the #1910→#1997 18-hour stranding) |
| Pre-condition | Working in `openova-io/openova` repo |
| Walk steps | 1. Push a no-op change touching `core/controllers/pkg/gitea/` (e.g. comment edit)<br>2. `gh run watch <run-id>` — workflow `build-organization-controller.yaml` fires (path filter caught it)<br>3. After build, `git log -n 1 products/catalyst/chart/values.yaml \| grep "controllers.organizationController.image.tag"` → bumped automatically by the workflow<br>4. `TestCreateOrg_HitsOrgsEndpointWithAuth` test green in CI |
| Code refs | `.github/workflows/build-organization-controller.yaml` |
| Screenshot | `.playwright-mcp/t39-ci-org-controller-autobump.png` (GH Actions run) |

#### Row 31 — PR #2012 — chore(ci): add auto-bump-images + pkg/** path filter to all build-*-controller workflows

| Field | Value |
|---|---|
| Closes | #2006 (TBD-A69) |
| Pillar/step | **CI infra** (systemic fix for all 7 controller workflows) |
| Pre-condition | None (CI-only) |
| Walk steps | 1. For each of `build-{application,blueprint,continuum,environment,organization,sandbox,useraccess}-controller.yaml`: confirm presence of `core/controllers/pkg/**` in BOTH `push.paths` and `pull_request.paths`<br>2. Confirm `permissions.contents: write` + `actions: write`<br>3. Confirm awk-scoped `Bump controllers.<who>.image.tag` step exists<br>4. Smoke-test: push a no-op touching `core/controllers/pkg/` — `applicationController` image tag in `values.yaml` bumps |
| Code refs | All `build-*-controller.yaml` workflows |
| Screenshot | `.playwright-mcp/t39-ci-all-controllers-aligned.png` (grep results) |

#### Row 32 — PR #2013 — fix(blueprint-controller): align mode enum with bp-*-vcluster blueprint files

| Field | Value |
|---|---|
| Refs | (pre-existing CI red) |
| Pillar/step | **CI infra** (unblocks every PR touching `blueprint-controller`) |
| Pre-condition | None (test infra) |
| Walk steps | 1. `cd $REPO && go test ./core/controllers/blueprint/internal/validate/... -run TestValidate_ExistingBlueprintCorpus` → PASS<br>2. `kubectl explain blueprint.spec.placementSchema.modes` → enum lists `single-region`, `active-active`, `active-hotstandby`, `primary-only`, `secondary-only`, `every-region`<br>3. The 4 `bp-*-vcluster` blueprints validate against the live CRD: `kubectl apply --dry-run=server -f platform/bp-mgmt-vcluster/blueprint.yaml` → no validation error |
| Code refs | `core/controllers/blueprint/internal/validate/validate.go`, `products/catalyst/chart/crds/blueprint.yaml` |
| Screenshot | `.playwright-mcp/t39-blueprint-controller-test-green.png` |

#### Row 33 — PR #2014 — fix(continuum-witness/cfkv): stabilise RenewExtendsTTLAndBumpsGeneration

| Field | Value |
|---|---|
| Refs | (pre-existing CI red) |
| Pillar/step | **CI infra** (unblocks `build-continuum-controller`) |
| Pre-condition | None (test infra) |
| Walk steps | 1. `cd $REPO && go test ./continuum/internal/witness/cloudflarekv/... -count=10` → 10/10 PASS, no flakes<br>2. Specifically `TestCFKV_ContractSuite/RenewExtendsTTLAndBumpsGeneration` and `TestCFKV_ContractSuite/GenerationMonotonicityAcrossOps` GREEN |
| Code refs | `continuum/internal/witness/cloudflarekv/client.go::Renew` (drop client-side wall-clock check) |
| Screenshot | `.playwright-mcp/t39-cfkv-flake-fixed.png` |

#### Row 34 — PR #2031 — feat(ci): add build-projector workflow + publish to GHCR

| Field | Value |
|---|---|
| Refs | (TBD-V18-B audit finding) |
| Pillar/step | **CI infra** (unblocks `controllers.projector.enabled` flip; not flipped in this PR) |
| Pre-condition | None |
| Walk steps | 1. `crane manifest oci://ghcr.io/openova-io/openova/projector:latest` → returns a real manifest (NOT NAME_UNKNOWN)<br>2. After a no-op push touching `core/cmd/projector/`, `gh run list --workflow=build-projector.yaml --limit 1 --json conclusion --jq '.[0].conclusion'` → `success`<br>3. The chart slot `controllers.projector.image.tag` in `products/catalyst/chart/values.yaml` is auto-bumped |
| Code refs | `.github/workflows/build-projector.yaml`, `core/cmd/projector/Containerfile` |
| Screenshot | `.playwright-mcp/t39-projector-image-publishes.png` |

#### Row 35 — PR #2048 — fix(blueprint-controller): add missing COPY pkg/ so image actually builds

| Field | Value |
|---|---|
| Refs | #2047 (TBD-V28) |
| Pillar/step | **CI infra** (the blueprint-controller image actually publishes — has been broken since CC1 #1095) |
| Pre-condition | None |
| Walk steps | 1. `crane manifest oci://ghcr.io/openova-io/openova/blueprint-controller:latest` → returns a real manifest (NOT NAME_UNKNOWN)<br>2. `grep "COPY core/controllers/pkg" products/catalyst/bootstrap/blueprint-controller/Containerfile` → present<br>3. After a no-op push, `gh run list --workflow=build-blueprint-controller.yaml --limit 1` shows `conclusion: success` (NOT silently failing on import-missing) |
| Code refs | `products/catalyst/bootstrap/blueprint-controller/Containerfile` |
| Screenshot | `.playwright-mcp/t39-blueprint-controller-image-publishes.png` |

#### Row 36 — PR #2022 — feat(security/kyverno): split policies into bp-kyverno-policies@1.0.0 Blueprint

| Field | Value |
|---|---|
| Refs | #2019 (TBD-A48 successor) |
| Pillar/step | **Infra / Pillar 5 supporting** (CRD install-ordering race killed; 18 compliance policies actually present in `Audit` mode post-install) |
| Pre-condition | bp-kyverno (engine) installed FIRST; bp-kyverno-policies depends-on it |
| Walk steps | 1. `kubectl get clusterpolicies.kyverno.io -o name \| wc -l` → 19 (18 from bp-kyverno-policies + 1 useraccess-boundary from bp-crossplane-claims), NOT 1<br>2. `kubectl get kustomizations -n flux-system bp-kyverno-policies -o yaml \| grep -A2 dependsOn` → `bp-kyverno`<br>3. None of the 18 policies stuck at `enabled: false` in chart values |
| Code refs | `platform/kyverno-policies/chart/`, bootstrap-kit Kustomization HR-to-HR dependsOn |
| Screenshot | `.playwright-mcp/t39-kyverno-policies-18-installed.png` |

#### Row 37 — PR #2023 — fix(security/kyverno-policies): annotate chart catalyst.openova.io/no-upstream=true

| Field | Value |
|---|---|
| Refs | #2019 |
| Pillar/step | **Infra** (Blueprint-Release CI hollow-chart guard allows the chart to publish) |
| Pre-condition | bp-kyverno-policies@1.0.0 from PR #2022 |
| Walk steps | 1. `crane manifest oci://ghcr.io/openova-io/openova/bp-kyverno-policies:1.0.0` → real manifest<br>2. `grep "no-upstream" platform/kyverno-policies/chart/Chart.yaml` → annotation present<br>3. The Blueprint-Release workflow run for the merge commit MUST be `success` (NOT failing at hollow-chart guard) |
| Code refs | `platform/kyverno-policies/chart/Chart.yaml` |
| Screenshot | `.playwright-mcp/t39-kyverno-policies-publish-success.png` |

#### Row 38 — PR #2044 — fix(catalyst-api/kubeconfig): GET fallback to bare path when region == primary

| Field | Value |
|---|---|
| Refs | #1882 |
| Pillar/step | **Infra** (mothership-side kubeconfig fetch for the primary region works) |
| Pre-condition | A deployment with `Regions[0].CloudRegion` = e.g. `hel1`, primary kubeconfig at `<id>.yaml` on disk |
| Walk steps | 1. `curl -sk "https://console.openova.io/sovereign/api/v1/deployments/<id>/kubeconfig?region=hel1" -w "%{http_code}\n"` → 200 (NOT 409 `kubefile-missing`)<br>2. Same query with `?region=nonexistent` → 409 (regression guard — unknown regions still 409)<br>3. `curl … "?region=fsn1"` (where fsn1 is a real secondary) → 200 with the slot-suffix file |
| Code refs | `products/catalyst/bootstrap/api/internal/handler/kubeconfig.go::GetKubeconfig` (new else-if branch on `region == dep.Request.Region`) |
| Screenshot | `.playwright-mcp/t39-kubeconfig-primary-fallback.png` (3-row http_code matrix) |

---

### Docs ships

#### Row 39 — PR #2046 — chore(canon): purge openova.io test-data string leaks

| Field | Value |
|---|---|
| Refs | #2025 (TBD-V25) |
| Pillar/step | **Pillar 4 supporting** (docs/test-fixtures use canonical pool domains) |
| Pre-condition | None |
| Walk steps | 1. `grep -rE "openova\.(io\|cloud)" products/sandbox/docs/ products/sandbox/mcp-server/internal/` — only PRODUCT identifiers (`sandbox.openova.io` K8s API group, `openova.io/region` label keys), NOT example/fixture URLs<br>2. Spot-check `products/sandbox/docs/user-journey.md` Scene 1: URL is `console.t39.omani.works` (NOT `console.rzk7.openova.io`)<br>3. `git log --pretty=oneline products/sandbox/docs/user-journey.md \| head -5` includes commit SHA from PR #2046 |
| Code refs | 9 files per PR body table |
| Screenshot | `.playwright-mcp/t39-docs-canon-purge.png` (grep output before/after) |

#### Row 40 — PR #2056 — docs(security): align SECURITY.md/ARCHITECTURE.md with PR #665 SPIRE deferral

| Field | Value |
|---|---|
| Refs | #2055 (TBD-V29) |
| Pillar/step | **Docs alignment** (PR #665 SPIRE removal is reflected in canonical docs) |
| Pre-condition | None |
| Walk steps | 1. `grep -nE "SPIFFE\|SPIRE\|SVID" docs/SECURITY.md docs/ARCHITECTURE.md` — references frame SPIRE as deferred / re-enable-triggers section (NOT current state)<br>2. `grep -E "WireGuard" docs/SECURITY.md` — Cilium WireGuard documented as canonical east-west mesh<br>3. `docs/STATUS.md` SPIRE row → `⏸ Deferred` (NOT `📐 Design`) |
| Code refs | `docs/SECURITY.md`, `docs/ARCHITECTURE.md`, `docs/STATUS.md` |
| Screenshot | `.playwright-mcp/t39-docs-spire-deferred.png` (rendered docs) |

#### Row 41 — PR #2061 — docs(sweep): align 6 docs with PR #665 SPIRE deferral + PR #2056

| Field | Value |
|---|---|
| Refs | #2055 |
| Pillar/step | **Docs alignment sweep** (sister to #2056) |
| Pre-condition | PR #2056 merged |
| Walk steps | 1. `grep -nE "bp-spire" docs/archive/omantel-handover-wbs.md docs/ARCHITECTURE.md docs/RUNBOOKS.md` — references framed as historical / deferred, with PR #665 + TBD-V29 citation<br>2. `grep -c "WireGuard\|deferred" docs/ARCHITECTURE.md` — top-level callout present<br>3. Other 3 docs from PR diff: same pattern |
| Code refs | 6 files per PR body table |
| Screenshot | `.playwright-mcp/t39-docs-spire-sweep.png` |

#### Row 42 — PR #2060 — docs(sandbox): align user-journey.md + architecture.md with TBD-V30 card-protocol deferral

| Field | Value |
|---|---|
| Refs | #2057 (TBD-V30), #2058 |
| Pillar/step | **Docs alignment** (card-protocol claim demoted to match actual `/cards` stub state) |
| Pre-condition | None |
| Walk steps | 1. `grep -nE "card protocol\|card-translator\|cardStream" products/sandbox/docs/user-journey.md products/sandbox/docs/architecture.md` — references framed as deferred / TBD-V30 forward-pointer<br>2. `grep -rn "/cards\b\|mode=cards\|cardStream\|CardView" products/catalyst/bootstrap/ui/src/` → 0 hits (confirms FE has no consumer — the stub is honestly described)<br>3. Mobile surface section now reads "xterm.js attach + ring-buffer replay" — NOT "card protocol" |
| Code refs | `products/sandbox/docs/user-journey.md`, `products/sandbox/docs/architecture.md` |
| Screenshot | `.playwright-mcp/t39-docs-cards-deferred.png` |

---

## §3 — Bulk-closure command list

> Run each command ONLY after the corresponding row's walk produces all three evidence artifacts. Substitute `<NN>` with the actual prov number (likely `t39` or `t40`). Surface tag tracks the row's `.playwright-mcp/` screenshot stem.

### Direct `Closes #N` from a PR — issue auto-closes on PR merge, but only flips to VERIFIED-PASS after this comment

```bash
# Row 5 — PR #1993 — Closes #1990
gh issue comment 1990 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 5. Screenshot: .playwright-mcp/t39-tenant-console-canonical-url.png. VERIFIED-PASS."

# Row 6 — PR #1996 — Closes #1994
gh issue comment 1994 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 6. Screenshot: .playwright-mcp/t39-pin-email-canonical-url.png. VERIFIED-PASS."

# Row 28 — PR #1998 — Closes #1995
gh issue comment 1995 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 28. Screenshot: .playwright-mcp/t39-flux-a66-rbac-stderr.png. VERIFIED-PASS."

# Row 20 — PR #2004 — Closes #1997
gh issue comment 1997 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 20. Screenshot: .playwright-mcp/t39-org-controller-ready.png. VERIFIED-PASS."

# Row 9 — PR #2009 — Closes #1999
gh issue comment 1999 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 9. Screenshot: .playwright-mcp/t39-voucher-email-sent-200.png. VERIFIED-PASS."

# Row 8 — PR #2011 — Closes #2000
gh issue comment 2000 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 8. Screenshot: .playwright-mcp/t39-voucher-rollback-pgrows.png. VERIFIED-PASS."

# Row 7 — PR #2010 — Closes #2001
gh issue comment 2001 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 7. Screenshot: .playwright-mcp/t39-marketplace-post-checkout-redirect.png. VERIFIED-PASS."

# Row 19 — PR #2008 — Closes #2002
gh issue comment 2002 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 19. Screenshot: .playwright-mcp/t39-sme-provisioning-init-guard.png. VERIFIED-PASS."

# Row 29 — PR #2007 — Closes #2003
gh issue comment 2003 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 29. Screenshot: .playwright-mcp/t39-valkey-empty-pass-newapi-running.png. VERIFIED-PASS."

# Row 31 — PR #2012 — Closes #2006
gh issue comment 2006 --repo openova-io/openova --body "CI infra change verified per WALK-RUNBOOK-2026-05-20.md row 31. Screenshot: .playwright-mcp/t39-ci-all-controllers-aligned.png. VERIFIED-PASS."

# Rows 1+2 — PRs #2038 + #2043 — Closes #2026 (TBD-V18 umbrella)
gh issue comment 2026 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md rows 1+2. Screenshots: .playwright-mcp/t39-marketplace-appdetail-configschema.png + .playwright-mcp/t39-marketplace-appconfigs-post.png. VERIFIED-PASS."
```

### `Refs #N` (do NOT auto-close on merge — need explicit walk + comment + close)

```bash
# Row 38 — PR #2044 — Refs #1882
gh issue comment 1882 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 38. Screenshot: .playwright-mcp/t39-kubeconfig-primary-fallback.png. VERIFIED-PASS."

# Rows 10/11/12/13/14/15 — PRs #1987/#1988/#1992/#2051/#2049/#2052 — Refs #1986 (TBD-P4 umbrella)
gh issue comment 1986 --repo openova-io/openova --body "Pillar-4 sub-breaks B1/B2/B3/B4 + audit findings A4 + injection verified per WALK-RUNBOOK-2026-05-20.md rows 10-15. Screenshots: t39-sandbox-mcp-envblock.png, t39-sandbox-agent-cli-binaries.png, t39-sandbox-xterm-attach.png, t39-mcp-subprocess-tree.png, t39-mcp-config-mounts.png, t39-sandbox-qwen-code-repl.png. VERIFIED-PASS for B1/B2/B3/B4."

# Row 27 — PR #1991 — Refs #1989
gh issue comment 1989 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 27. Screenshot: .playwright-mcp/t39-flux-a66-recovery.png. VERIFIED-PASS."

# Row 16 — PR #2017 — Refs TBD-V14 #2015
gh issue comment 2015 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 16. Screenshot: .playwright-mcp/t39-sandbox-tokenmint-ready.png. VERIFIED-PASS."

# Row 26 — PR #2018 — Refs TBD-V13 #2016
gh issue comment 2016 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 26. Screenshot: .playwright-mcp/t39-cutover-resume-advance.png. VERIFIED-PASS."

# Rows 36+37 — PRs #2022 + #2023 — Refs #2019 (TBD-A48 successor)
gh issue comment 2019 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md rows 36+37. Screenshots: t39-kyverno-policies-18-installed.png + t39-kyverno-policies-publish-success.png. VERIFIED-PASS."

# Row 21 — PR #2050 — Refs #2021
gh issue comment 2021 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 21. Screenshot: .playwright-mcp/t39-catalyst-api-newapi-wired.png. VERIFIED-PASS."

# Row 39 — PR #2046 — Refs #2025 (TBD-V25)
gh issue comment 2025 --repo openova-io/openova --body "Audit verified per WALK-RUNBOOK-2026-05-20.md row 39. Screenshot: .playwright-mcp/t39-docs-canon-purge.png. VERIFIED-PASS."

# Row 17 — PR #2037 — Refs #2032 (TBD-V21)
gh issue comment 2032 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 17. Screenshot: .playwright-mcp/t39-sandbox-token-env.png. VERIFIED-PASS."

# Rows 22+23+24 — PRs #2039 + #2041 + #2045 — Refs #2034 (TBD-V24 umbrella)
gh issue comment 2034 --repo openova-io/openova --body "TBD-V24 MISS-1/MISS-2/MISS-3 verified per WALK-RUNBOOK-2026-05-20.md rows 22+23+24. Screenshots: t39-cutover-step10-vcluster-pivot.png + t39-ghcr-pull-stripped.png + t39-cutover-step11-crossplane-pivot.png. VERIFIED-PASS for all 3 misses."

# Row 25 — PR #2036 — Refs #2035
gh issue comment 2035 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 25. Screenshot: .playwright-mcp/t39-cutover-totalsteps-9.png. VERIFIED-PASS."

# Row 3 — PR #2053 — Refs #2042 (TBD-V27)
gh issue comment 2042 --repo openova-io/openova --body "Operator walk on fresh prov t39 verified per WALK-RUNBOOK-2026-05-20.md row 3. Screenshot: .playwright-mcp/t39-provisioning-appconfigs-applied.png. VERIFIED-PASS."

# Row 35 — PR #2048 — Refs #2047 (TBD-V28)
gh issue comment 2047 --repo openova-io/openova --body "Image-publish verified per WALK-RUNBOOK-2026-05-20.md row 35. Screenshot: .playwright-mcp/t39-blueprint-controller-image-publishes.png. VERIFIED-PASS."

# Rows 40+41 — PRs #2056 + #2061 — Refs #2055 (TBD-V29)
gh issue comment 2055 --repo openova-io/openova --body "Docs alignment verified per WALK-RUNBOOK-2026-05-20.md rows 40+41. Screenshots: t39-docs-spire-deferred.png + t39-docs-spire-sweep.png. VERIFIED-PASS."

# Row 42 — PR #2060 — Refs #2057, #2058 (TBD-V30)
gh issue comment 2057 --repo openova-io/openova --body "Docs alignment verified per WALK-RUNBOOK-2026-05-20.md row 42. Screenshot: .playwright-mcp/t39-docs-cards-deferred.png. VERIFIED-PASS."
gh issue comment 2058 --repo openova-io/openova --body "Docs alignment verified per WALK-RUNBOOK-2026-05-20.md row 42. Screenshot: .playwright-mcp/t39-docs-cards-deferred.png. VERIFIED-PASS."
```

### After ALL walks PASS — final `gh issue close` invocations

> Only run AFTER the `gh issue comment` above lands on each issue. The bulk-closer:

```bash
for N in 1882 1986 1989 1990 1994 1995 1997 1999 2000 2001 2002 2003 2006 2015 2016 2019 2021 2025 2026 2032 2034 2035 2042 2047 2055 2057 2058; do
  gh issue close "$N" --repo openova-io/openova \
    --comment "Bulk close: all walk rows PASS per WALK-RUNBOOK-2026-05-20.md. Closing per CLAUDE.md §4 (operator walk + screenshot completed)."
done
```

---

## §4 — Total expected closures

**Pre-walk open-ticket count target (rough estimate from session memory + tracker)**: ~21 issues open.

**If ALL 42 rows PASS** → **27 issues close** (listed in §3 loop above). The other 15 PRs (CI infra, build pipeline gap-fixes, blueprint validator) advance closed issues already, OR are sister-PRs of an umbrella that closes on a single row passing.

**Post-walk projected open count**: substrate-restore (#2020) + any new TBD-V## filed from walk-FAILures + the 4-5 TBD-V## that ship docs/CI scaffolds for follow-up work (e.g. #2040 HelmRelease-values binding follow-up).

**Net delta (best case)**: -22 open issues (from 21 → projected -1, with substrate restore closing TBD-V15 itself).

---

## §5 — Walk-FAIL handling

For ANY row in §2 whose walk FAILS:

1. **Do NOT post a VERIFIED-PASS comment.** The issue stays open.
2. **Do NOT close the PR's merged state.** The fix is in source; the deployment / regression is the new gap.
3. **File a new TBD on the issue** (or sister issue) describing:
   - Row number from §2
   - Specific assertion that failed (e.g. "step 3 of Row 19 — Pod transitioned 1/1 prematurely without step-09 annotation present")
   - Captured log/grep output
   - Suspected root cause (if any)
4. **Mark the row in TRUST.md as ⛔ VERIFIED-FAIL** with the new TBD number.
5. **Wait for the new TBD's fix PR to merge** before re-running the walk.
6. **Re-walk only that single row** in the next cycle; do NOT block other rows from being closed.

Concretely:

```bash
# Walk row 19 failed at step 3
gh issue create --repo openova-io/openova --title "TBD-Vxx: PR #2008 init-container guard does not bridge to step 09 transition on t39" \
  --label area/catalyst,status/in-progress \
  --body "Walk row 19 of WALK-RUNBOOK-2026-05-20.md FAILED. ... [evidence] ..."

# Update TRUST.md row inline with the new TBD ref
```

---

## §6 — Substrate-state checks recap

Quick-reference before any walk run:

```bash
# 1. No Pending Pods on mothership
kubectl --kubeconfig ~/.kube/config get pods -A --field-selector=status.phase=Pending

# 2. Mothership CPU headroom (need ≥500m free)
kubectl --kubeconfig ~/.kube/config top nodes

# 3. Orchestrator reachable (proves substrate-restore complete)
curl -sk -o /dev/null -w "%{http_code}\n" https://console.openova.io/sovereign/api/v1/deployments
# Must return 200, NOT 502.

# 4. TBD-V15 (#2020) substrate-restore authorisation
gh issue view 2020 --repo openova-io/openova --json state,comments --jq '.state, (.comments[-1].body | .[0:200])'

# 5. Pin set live on `main` matches §1.3 minima
for chart in bp-catalyst-platform bp-sandbox bp-newapi bp-self-sovereign-cutover bp-flux bp-valkey bp-kyverno-policies; do
  kubectl get hr "$chart" -A --no-headers 2>/dev/null | awk '{print $2"\t"$3}'
done
```

All five must report green. If ANY check fails → freeze the walk; comment on #2020 with the failure and the row that was about to be walked.

---

## Appendix A — PR-to-Pillar quick-map

| Pillar | PRs | Walk rows |
|---|---|---|
| **1 — Marketplace + voucher onboarding** | #2038 #2043 #2053 #2029 #1993 #1996 #2010 #2011 #2009 | 1-9 |
| **4 — Sandbox + qwen-code + MCP** | #1987 #1988 #1992 #2051 #2049 #2052 #2017 #2037 #2054 #2008 #2004 #2050 | 10-21 |
| **5 — Sovereign independence** | #2039 #2041 #2045 #2036 #2018 | 22-26 |
| **Infra/CI scaffolds** | #1991 #1998 #2007 #2005 #2012 #2013 #2014 #2031 #2048 #2022 #2023 #2044 | 27-38 |
| **Docs alignment** | #2046 #2056 #2061 #2060 | 39-42 |

## Appendix B — Anti-theater discipline checks for THIS runbook

Per CLAUDE.md §4:

1. ✅ Every row cites a concrete artifact (kubectl command / curl / file:line / screenshot stem).
2. ✅ Default closure language uses VERIFIED-PASS / FAIL / PARTIAL — never "shipped" or "done".
3. ✅ The runbook itself is `Refs`-style — closes nothing on merge of the doc PR; only walks-with-screenshots close issues.
4. ✅ READ-ONLY for the verification agent — closure commands listed are for the OPERATOR or a dedicated closure agent after walks complete.
5. ✅ Walk steps reference `origin/main` paths (`platform/...`, `core/...`, `products/...`) — not the local working tree.
6. ✅ Domain canon enforced — every example uses `t39.omani.works` / `<slug>.omani.homes`, never `openova.io` test data.
7. ✅ Falsifiable: every "PASS" criterion has a counter-example anti-pattern (e.g. "NOT 502", "NOT 401", "NOT ENOENT") that would force a FAIL.

End of WALK-RUNBOOK-2026-05-20.md
