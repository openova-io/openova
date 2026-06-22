# jobs + compliance + sandbox + meta surfaces — live walk on omantel.biz — 2026-06-22

Walked READ-ONLY via Playwright.

## Jobs (`/jobs`) — `evidence/jobs-list.png`
- PASS — live job stream re-attaches from catalyst-api every 5s; 28/31 rows render.
- Filters present: STATUS (running/pending/succeeded/failed/healthy/degraded/failing), KIND (cron/lifecycle/step/task), APP (per-app), PARENT (Cutover / Provision Huawei / Reconcilers).
- Structured finite-job table with columns NAME / KIND / APP / DEPS / PARENT / STATUS / RUNS / STARTED / DURATION / ACTIONS. Examples: Trivy Security Scan (task), syft-sbom, powerdns-zone-bootstrap, kyverno-migrate-resources, gitea/harbor/guacamole-pg initdb, openbao-host-token-reviewer-export, bp-flux-stuck-hr-recovery.

## Compliance (`/sre/compliance`) — `evidence/compliance-dashboard.png`
- PASS — SRE Lead Compliance Dashboard renders: Fleet view Sovereign x Organization x Application x score; scoring domains security/sre/baseline/reliability; cells sized by policy weight, colored by pass-rate; SSE live updates; drill-in to `/admin/compliance/policy/<policyName>` per-policy violations. No login wall.

## Sandbox (`/sandbox`) — `evidence/sandbox-launcher.png`
- PASS(launcher) — agent picker renders: Aider, Claude Code (+ Connect Claude Max), Cursor Agent, Little Coder, OpenCode, Qwen Code, each with a "Start session" button.
- WARN(live session) — no live `sandboxes.sandbox.openova.io` CR exists yet; an actual launched session + auto-mounted openova-sandbox-mcp not exercised read-only.

## Settings (`/settings`) — `evidence/settings.png`
- PASS — sections render: Organization / Sovereign / API tokens / Cloud credentials / DNS / Domain mode / Marketplace / Notifications / Members / Danger zone / Sovereignty.
- Sovereign panel reads FQDN `omantel.biz`, region `me-east-215-a`, Capacity `m7n.xlarge.8`, Deployment ID `4635277cae4ffed9`, Created 6/22/2026, Status `ready`.
- "Sovereignty" section present (cutover surface). "Domain mode" + "DNS" sections cover parent-domains config.
