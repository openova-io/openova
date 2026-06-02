# G117 — sub-EPIC agent briefing template

> Source: produced by G117 Wave-0 (`feat/g117-wave-0-preflight`, PR linked from issue #2738). Every Wave-1 (and beyond) agent receives a substitution of this template with the per-sub-EPIC fields filled in. **Do not edit the template inline for one-off dispatches** — instead, copy it into the dispatch briefing and fill the `{{var}}` slots.

---

## Identity

| Field | Value |
|---|---|
| **Sub-EPIC ID** | `{{sub_epic_id}}` (e.g. `G117.1`) |
| **Tracking issue** | https://github.com/openova-io/openova/issues/{{issue_number}} |
| **Parent EPIC** | #2737 |
| **Wave** | `{{wave_id}}` (e.g. `Wave-1`) |
| **Slot** | `{{slot_id}}` (e.g. `B1` — see `.claude/templates/G117-worktree-policy.md`) |
| **Dispatched by** | {{dispatcher_handle}} |
| **Worktree isolation** | `{{isolation}}` (`worktree` if Wave-1 slot touches `platform/*/blueprint.yaml` or `platform/keycloak/chart/templates/configmap-*-realm.yaml`; else `none`) |

## Wave dependencies — what you consume

You may consume ONLY artifacts produced by Wave-0 (or earlier-Wave-N sub-EPICs explicitly listed below). Touching files outside your file-touch matrix row is a cross-wave contract breach → STOP and reach out per the Escalation protocol.

- `core/pkg/apis/blueprint/v1alpha1/topology_types.go` — Go types
- `core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go` — same types as a controllers-importable mirror
- `platform/_schemas/blueprint-topology.json` — JSON-schema gate
- `examples/blueprint-grafana-fully-declared.yaml` — reference
- `docs/api/catalyst-api-openapi.yaml` — REST contract
- `products/catalyst/console/src/routes/**/+page.svelte` — UI scaffolds
- `products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml` — mock fixtures
- `docs/adr/0009-per-org-iac-repo-bootstrap.md` — IaC repo decision
- `tools/bootstrap-org-iac-repo.sh` — bootstrap script
- `tools/migrate-blueprints-topology.py` — Blueprint topology migration tool
- Wave-1 prior-slot artifacts: `{{wave_1_prior_artifacts_or_none}}`

## Acceptance DoD (locked per sub-EPIC, do not relax)

{{acceptance_dod_checklist}}

Each item below MUST evaluate true before you report done:

- [ ] One PR titled `{{pr_title}}` opened against `main`, body cites `Refs #{{issue_number}}` (NEVER `Closes`)
- [ ] PR diff scope is exactly your file-touch matrix row (no out-of-scope edits; no formatting churn outside)
- [ ] Unit tests added for every new function/method (Go: `*_test.go`; TS/JS: `*.spec.ts`)
- [ ] Integration tests added if the work crosses a service boundary
- [ ] Playwright spec added if the work touches a UI surface, named `{{playwright_spec_name}}` under `tests/e2e/playwright/tests/` or `products/catalyst/console/tests/e2e/`
- [ ] Docs update — at minimum `docs/STATUS.md` row + relevant section of `docs/ARCHITECTURE.md` / `docs/RUNBOOKS.md`
- [ ] Memory updates — if you discovered a non-obvious gotcha, ship `feedback_<topic>.md` under `~/.claude/projects/-home-openova-repos-openova/memory/` and add the index entry in `MEMORY.md` IN THE SAME PR
- [ ] New architectural choices not in the locked-decisions list → ship as new `docs/adr/00XX-<topic>.md` (numbered after the latest existing ADR)
- [ ] `docs/ledger/TRACKER.md` row for this sub-EPIC updated
- [ ] `gh pr checks` all green
- [ ] Self-verification evidence posted as a comment on the tracking issue (curl outputs, screenshot of green checks, Playwright report URL)
- [ ] Verifier sub-agent verdict posted on the tracking issue (you spawn it; it reports `status/uat-OK` or `status/uat-FAIL`)
- [ ] If `uat-OK`: move the issue label from `status/in-progress` to `status/uat`. **Operator (NOT you) flips to `status/completed`.**

## Self-verification protocol

You self-verify before claiming done. The verifier sub-agent is a second pair of eyes — it does NOT replace your own checks.

1. **Build/lint pass locally**
   - Go: `cd <module-dir> && go build ./... && go vet ./... && go test ./...`
   - JS/TS: `npm run build && npm test` (or per the package.json `scripts`)
   - OpenAPI: `spectral lint <path> --ruleset spectral:oas` — 0 errors (warnings ok if you justify)
   - JSON schema: `python3 -c "import json,jsonschema; jsonschema.validate(yaml.safe_load(open(X)), json.load(open(schema)))"`
   - Helm chart: `helm lint <chart>` + `helm template <chart> | wc -l > 5` (guards against hollow-chart per `feedback_hollow_chart_dual_annotation.md`)

2. **Post evidence**
   - At minimum a fenced block with the last 20 lines of EACH command + `exit=0`
   - For UI work: screenshot URL + Playwright report URL
   - For chart work: `helm template` excerpt showing rendered resources

3. **Run the issue checklist top-to-bottom** and tick each box manually in a PR comment

4. **Spawn a verifier sub-agent** with this prompt template:

   ```
   You are an INDEPENDENT verifier for {{sub_epic_id}} (PR <URL>).
   READ-ONLY mode — you may NOT push commits or close issues.
   Tasks:
     1. Pull the branch and re-run the build/lint suite from the brief
     2. Apply each acceptance DoD checkbox to the diff; flag missing items
     3. Apply the anti-theater red-flag list (see below) — return verdict
        even if all checks pass but a red flag is present
     4. Post one comment on issue #{{issue_number}} with:
        - PASS or FAIL verdict
        - Per-checkbox PASS/FAIL
        - Red-flag findings (PR-#NNNN-style references)
        - Recommended status/* label transition
   ```

## Failure-mode decision tree

Stuck > 30 minutes on the same error:

1. **Re-read memory** — `cat ~/.claude/projects/-home-openova-repos-openova/memory/MEMORY.md` and grep for keywords matching your sub-EPIC theme
2. **Spawn a verifier sub-agent** with fresh eyes
3. **If verifier confirms blocker**, call `chepherd.alert_human` with `kind=stuck`, `urgency=medium`, body = verbatim error + last 50 lines of context + your hypothesis
4. **Do NOT silently abandon, do NOT ship partial, do NOT claim done**

## Anti-theater rules (verbatim — fold into your own self-check)

Red flags that EITHER you or the verifier MUST surface:

- `Closes #N` in PR body when only this sub-EPIC is being shipped (NEVER auto-close from PR merge — only the operator closes after the walk)
- Null-guards on data that can never be null (PR #1185 shape)
- `enabled: false` defaults on features the deterministic test asserts present (PR #1138 shape)
- Click handlers missing on leaf cells (PR #1085 shape)
- Scaffold-only PRs with `Closes #N` and no operator-visible behavior change (PR #1918 shape)
- `kubectl --dry-run=server` as the sole validator on chart work (PR #1933 shape)
- Multi-region claim on a single-region prov (PR #1599 shape)
- `must_contain` token-passing tests (PR #1362/#1366/#1371/#1378 shape)
- Python `jsonencode()` simulation passed off as `tofu validate` (PR #1892 shape)
- `Refs #N` is the default in PR bodies, not `Closes #N`. Auto-close is the enemy.

## Commit signature

```bash
git -c user.name=hatiyildiz \
    -c user.email=269457768+hatiyildiz@users.noreply.github.com \
    commit -s -m "$(cat <<'EOF'
{{conventional_subject}}

{{commit_body}}

Refs #{{issue_number}}

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

- Conventional commit type from `{feat, fix, docs, chore, refactor, test}`
- `-s` (sign-off) required
- `Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>` trailer REQUIRED
- Default identity: `hatiyildiz` — switch to `alierenbaysal` ONLY when the operator directs

## Banned terms

Single source of truth: [`docs/GLOSSARY.md`](../../docs/GLOSSARY.md) §Banned-terms.

Frequent offenders: `tenant` → `Organization` · `operator` (as a person) → `sovereign-admin` · `client` → `User` · `module`/`template` (in Catalyst sense) → `Blueprint` · `Workspace` → `Environment` · `Instance` (user-facing) → `Application` · `Synapse` (OpenOva product) → `Axon` · `Backstage` → `Catalyst console`.

## Test domains canon (NEVER `openova.io` in tests)

| Surface | Domain pattern | Notes |
|---|---|---|
| Test Sovereign | `t<NN>.omani.works` | swap to `t<NN>.omantel.biz` if LE-rate-limited |
| Tenant Organization | `<orgslug>.omani.homes` (default) | also `omani.rest`, `omani.trade` |
| Voucher redeem URL | `https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>` | BSS menu, NOT `admin.<sov>` |
| Mothership | `console.openova.io` | only for real-mothership work, NEVER in tests |

## CRITICAL safety rules

### NEVER touch `emrah.baysal` email creds

- No `POST` / `PUT` / `DELETE` to `mail.openova.io` admin API
- No write to any `password` / `SMTP_PASS` / `IMAP_PASSWORD` Secret
- Diagnostic READ-ONLY OK. If broken → ask operator; never rotate
- Memory ref: `feedback_never_touch_emrah_baysal_email.md`

### Canonical wipe endpoint for any destructive op

- `POST https://console.openova.io/sovereign/api/v1/deployments/{id}/wipe` — runs `tofu destroy` + cleans Hetzner servers + LBs + S3 bucket + DNS records atomically
- Never raw `hcloud server delete` for shared infra
- Memory ref: `feedback_canonical_wipe_endpoints.md`

## Pre-flight checklist (run BEFORE acting)

| Operation | Mandatory pre-flight |
|---|---|
| **Wipe any Sovereign or namespace** | (1) PVC debug-Pod read of every `tofu.auto.tfvars.json` (2) Hetzner servers list (3) per-target table (4) **ask operator which rows wipeable** (5) only on confirmation use canonical wipe endpoint |
| **Claim a credential is missing** | (1) enumerate `/deps/tofu/*/tofu.auto.tfvars.json` on `catalyst-api-deployments` PVC (2) enumerate `/deps/kubeconfigs/` (3) check Stalwart admin creds (4) only then claim missing |
| **Provision fresh Sovereign** | (1) `gh api /repos/openova-io/openova/packages/container/<bp-*>/versions` for active chart pins (2) pick `parent_domains_yaml` TLD per LE rotation (3) POST `/sovereign/api/v1/deployments` with auth |
| **Dispatch a sub-agent** | (1) pre-dispatch briefing per user-global §5 (2) pillar+step grounding test (3) `isolation: worktree` if parallel + touching same files (4) re-query live state after return |
| **Believe something is "fixed"** | (1) re-query live state directly (`kubectl` / `curl` / `gh`) (2) cite specific evidence (3) operator closes issues — NOT you |
| **File a new TBD** | (1) does this gap block the next pillar walk? if no, note in audit doc only (2) cite canonical doc reference (3) use `Refs #N` not `Closes #N` |

## Resource budgets (hard caps)

- GitHub API: **200 req per dispatch** (5000/h ÷ ~24 dispatches)
- GHCR pushes: serialize via `flock /tmp/ghcr-push.lock` if 2+ agents publish concurrently
- hw86 catalyst-api: max **10 parallel API calls** per agent
- Playwright spec runs: **sequential per agent's spec set** (parallel only across agents)
- Bastion memory: **1.5 GiB max RSS per agent** (6 agents × 1.5GiB = 9GiB ceiling)
- If you blow a budget: STOP, lower concurrency, retry. Don't tight-loop.

## Memory + ADR discipline (mandatory)

1. **Read `MEMORY.md` first** — `cat ~/.claude/projects/-home-openova-repos-openova/memory/MEMORY.md`
2. **Grep for keywords matching your sub-EPIC title** in the memory dir
3. **When you discover a gotcha that's not yet in memory**, ship a new `feedback_<topic>.md` + index entry **in the same PR** (not as a follow-up — same PR)
4. **Every new architectural choice not in the locked-decisions list** ships as a new ADR under `docs/adr/`
5. **Update `docs/ledger/TRACKER.md`** row for your sub-EPIC at PR open + at PR merge

## Escalation & rollback

| Trigger | Action |
|---|---|
| Stuck >30 min on same error | Spawn verifier sub-agent FIRST (fresh eyes) |
| Verifier confirms blocker | `chepherd.alert_human` kind=stuck, urgency=medium, body=verbatim error + 50 lines context |
| Bad PR breaks Wave-1 contract | Revert is allowed and EXPECTED — don't fix-forward when the data shape is wrong |
| Cross-wave contract breakage detected | STOP all downstream agents until restored |
| Anthropic rate-limit | Run `/home/openova/bin/anthropic-rate-limit-trip.sh` (5-min hold + 30-min cron retry) — see `feedback_anthropic_rate_limit_overnight_survival.md` |

---

## Locked architectural decisions (do NOT re-debate)

1. Blueprint topology shape: `spec.topology.{supported[], defaults{multi-region, single-region}, perTopology{<choice>{replication, switchover, placement}}}`
2. bcpTopology enum: `active-active | active-hot-standby | active-passive | singleton`
3. Launch button SSO: silent OIDC `prompt=none&kc_idp_hint=catalyst-pin`, new tab, <500ms target
4. Endpoint mutation: PR + auto-merge against `gitea.<sov>/<org>/iac` with Kyverno + cert-mgr + DNS-conflict pre-check
5. SSO fan-out scope: Tier-1 (4) + Tier-2 (4) + Tier-3 (9+) = ALL SSO-capable apps
6. Tier-3 KC realm: per-Org realm has "Keycloak OIDC" IdP federated to `sovereign` realm broker (2-hop)
7. Default topology detection: `len(Sovereign.spec.regions) > 1` = multi-region; else single-region

---

> **Per-sub-EPIC substitution sheet (fill before dispatch):**
>
> ```yaml
> sub_epic_id: G117.X
> issue_number: NNNN
> wave_id: Wave-N
> slot_id: <letter><digit>
> dispatcher_handle: p0-474-lonely
> isolation: worktree|none
> wave_1_prior_artifacts_or_none: (list)
> pr_title: feat(g117.X): <one-line>
> playwright_spec_name: g117-<topic>.spec.ts
> conventional_subject: feat(g117.X): <one-line>
> commit_body: |
>   <para 1 — why>
>   <para 2 — what>
> acceptance_dod_checklist: |
>   - [ ] criterion 1
>   - [ ] criterion 2
>   - [ ] criterion 3
> ```
