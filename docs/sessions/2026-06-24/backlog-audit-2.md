# Backlog audit #2 — open-issue true-state sweep (2026-06-24)

**Scope:** the ~24 OPEN issues on `openova-io/openova`. Goal: an honest backlog — no stale-open, no false-open. Each issue cross-referenced against the merged 215-row UAT walk on main (`docs/ledger/UAT.md`), merged PRs, and a live check (mothership `catalyst-api` pod + Sovereign kubeconfig `/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml`, omantel.biz dep `4635277cae4ffed9`).

**Constraints honored:** did NOT touch the founder-parked agenity/chepherd issues (#4010/#4079/#4111/#4180), nor the in-flight fixes being re-walked by the parallel agent (#3925/#3896/#4193/#3785/#4158/#4143/#4179). Closes require live evidence — none closed to shrink the count.

## Actions taken

| Issue | Verdict | Action | Evidence |
|---|---|---|---|
| **#4091** Console: sophisticated YAML/IaC code editor | **DONE** | **CLOSED** | PR #4092 merged; `widgets/code/CodeView.tsx`+`CodeMirrorEditor.tsx` on main, wired via `YamlEditor`/`InstallPage`/`UpgradeDialog`/`TopologyEditor`; live-walk 2026-06-22 (`.cm-editor`, line gutter, 23 highlight tokens, `isBareTextarea:false`). Deployed `catalyst-ui:d1bd840`. |
| **#4085** Per-resource Compliance tab | **DONE** | **CLOSED** | PR #4087 merged; `cloud-list/ResourceComplianceTab.tsx` on main, rendered by `ResourceDetailPage.tsx`; live-walk 2026-06-22 (scoped "Per-resource policy compliance" table for `helmrelease/flux-system/bp-cilium`, not the global view). |
| **#4002** ARCH P0: OpenTofu→Crossplane adoption seam | **OPEN (not built)** | **Label fix** (removed stale `status/uat`) + confirm comment | Live: `providers.pkg.crossplane.io` → No resources found; `cloudadoption -A` → 0; Crossplane owns 0 cloud resources. UAT rows 206/207 ⛔ feature-not-built. `status/uat` was misleading (implies code-done); it is unimplemented architectural work. |
| **#4206** Region-B per-app CNPG PVCs Pending | **OPEN (reproduces)** | Confirm comment | Live region-B kubectl: `guacamole-pg-1`/`gitea-pg-1`/`harbor-pg-1` STILL `Pending`, `storageClassName=""`, 46h. `evs-ssd` IS now the default SC but a default only applies at PVC-creation time, so the pre-existing empty-class PVCs do not retro-bind. |
| **#4018** Path B: provisioning instantiates Crossplane XRCs | **OPEN (not realized)** | Confirm comment | Producer code landed (`provisioner/adoption.go` called at `provisioner.go:2051`) but live `cloudadoption -A` → 0. Path A (#4019) shipped; Path B's XRC instantiation unproven live. Same root as #4002. |
| **#3914** Provisioning UX: 'Bootstrapping cluster' timeline phase | **OPEN (scaffold-only)** | Confirm comment | Commit `7ee69904a (Refs #3914)` landed `BootstrapProgress.tsx`+`bootstrap-phases.ts`+`useProvisioningStream.ts`, BUT `BootstrapProgress` is imported NOWHERE — orphan component, not wired into the provision timeline (PR #1918 scaffold-only shape). The ~30-min void is not actually filled. No UAT row. |

## Left as-is (verdict confirmed correct, recent audit comment already present)

| Issue | State | Why correct |
|---|---|---|
| **#3374** SSO bare-URL signed-in (EPIC) | OPEN `status/uat` | Mostly green (console/grafana/gitea/harbor/openbao/keycloak/newapi all ✅, UAT rows 26-45) but a named live ❌ remains (row 27 avatar, tracked by closed #4187 follow-up). Multi-surface EPIC; correct OPEN with 2026-06-24 status comment. |
| **#3376** Funnel: stranger ends signed-in in OWN org console (EPIC) | OPEN `status/uat` | Green through checkout (rows 72-85 ✅) but terminal "signed-in in own org console with app RUNNING" blocked (rows 86/89/90/93/94/95 ❌ → #4179/#3785/#4188). Correct OPEN. |
| **#3379** Sovereignty cutover durability + deny-egress proof (EPIC) | OPEN `status/uat` | Cutover never walked end-to-end on the permanent env; `/jobs` renders no cutover group (rows 162-166 ❌). Per-step fixes landed but the durable cutoverComplete + egress-block proof is unmet. Correct OPEN. |
| **#3740** bp-cnpg-pair RPO=0 (sync replica) | OPEN `status/uat` | Chart on main now does synchronous replication (`synchronous_standby_names = FIRST 1`, PR #4201 merged 2026-06-23), but NO live region-kill RPO=0 proof row exists. Code done, region-kill UAT pending — `status/uat` accurate. |
| **#3829** Every app/activity shows TRUE live state (EPIC) | OPEN (no status label) | The live-state DR backbone is absent: `kubectl get continuum -A` → No resources found on the permanent env, so topology/DR live-state cannot be exercised. Master tracker for the true-live-state work. Correct OPEN. |
| **#3969** EPIC: Application-centric Placement | OPEN `status/in-progress` | Model + multi-region-derive shipped & validated (PRs #3972/#3984/#3989/#4001/#4029), consolidation not yet 100% per the ticket's own close rule. Adjacent to in-flight #3925. Left untouched. |
| **#4181** UAT-215: drive every row to ✅/❌ | OPEN (no status label) | The walk landed (182✅/37❌/3⛔/0⚠️, PR #4195) but the ticket's end goal is 0 ❌ too; recon rows 195/196/197 corrected ✅→❌. Tracks the residual ❌ backlog. Correct OPEN. |

## Live cross-reference facts (gathered this audit)

- `kubectl get continuum -A` → **No resources found** (region-A) — the DR/topology backbone is absent; blocks #3829, #3740 region-kill, #3969 recon-status live-proof.
- `kubectl get cloudadoption -A` → **No resources found**; `providers.pkg.crossplane.io` → **No resources found** — Crossplane owns 0 resources; blocks #4002, #4018.
- Region-B `guacamole-pg-1`/`gitea-pg-1`/`harbor-pg-1` PVCs → **Pending 46h** with empty storageClass — #4206 reproduces.

## Result

- **Closed (DONE, with evidence):** #4091, #4085 — 2 issues.
- **Label fixed:** #4002 (removed stale `status/uat`).
- **Current-state confirm comments posted:** #4206, #4018, #3914, #4002.
- **Open count: 24 → 21.**

### The genuine remaining backlog (21 open)

**Not-built / architectural:** #4002 (Crossplane adoption seam), #4018 (Path B XRC instantiation), #3914 (Bootstrapping timeline — wire the orphan widget).

**Live defect reproduces:** #4206 (region-B CNPG PVCs Pending).

**Big EPICs, code largely landed, blocked on a named live ❌ / missing live backbone:** #3374 (SSO), #3376 (funnel terminal step), #3379 (cutover end-to-end), #3829 (true live-state / Continuum CRs absent), #3740 (RPO=0 region-kill walk), #3969 (placement consolidation), #4181 (residual UAT ❌).

**In-flight (parallel agent — untouched):** #3925, #3896, #4193, #3785, #4158, #4143, #4179.

**Founder-parked (agenity/chepherd — untouched):** #4010, #4079, #4111, #4180.
