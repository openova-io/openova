# OpenOva UAT Matrix — Wave 2 (DEEP) — 2026-06-03

> **What this is**: Pre-go-live User Acceptance Test, **Wave 2 — deeper attack surface**. 10 independent verifier agents attacked attack-surfaces the Wave-1 sweep didn't stress-test (cross-Org isolation, DR wall-clock, Cilium NP depth, helm hollow-render, CRD admission boundary, secrets rotation, cutover idempotency, CI hygiene, anti-tether sweep, voucher fraud). 9 new gap tickets filed. Read this together with `2026-06-03-uat-matrix.md` for the full pre-go-live picture.

## Overall verdict change

Wave-1 said 🟢 GO. **Wave-2 downgrades to 🟡 GO with 9 prerequisites** — none block code-side fresh-prov on hw87, but 1 (Pillar 5 anti-tether sweep #2939) means a franchised Sovereign that completes `bp-self-sovereign-cutover` SUCCESSFULLY would still fail PIN-based silent SSO. Founder should review #2939 + #2937 before declaring a Sovereign "independent."

## Wave-2 attack matrix

| # | Attack vector | Verdict | New ticket |
|---|---|---|---|
| 1 | **Can Org-A read Org-B's secrets?** | 🟢 ISOLATION-STRONG — Sandbox SA is namespace-scoped (`Role` not `ClusterRole`), catalyst-api `callerInOrg()` gate enforces `claims.Org` membership on every endpoint mutation (PR #2757), OpenBao token paths scope to `org/<slug>/*`, Keycloak per-Org realm isolation enforced at JWT level, CiliumNetworkPolicy default-deny | none |
| 2 | **Does ≤60s RTO actually hold under realistic latency?** | 🟡 PASS-AT-RISK — baseline ~16s, typical ~25s, worst ~45s, BUT **step-4 (PDM commit) has NO timeout** → if PDM hangs, switchover hangs forever. Step-7 NATS audit is synchronous-blocking | [**#2933**](https://github.com/openova-io/openova/issues/2933) |
| 3 | **Other charts with same Cilium identity trap as #2861?** | 🟡 1 FOUND — `platform/vpa/chart/templates/networkpolicy.yaml` uses bare `ipBlock 0.0.0.0/0` for kube-apiserver watch. Identical RCA. 13 other charts use ipBlock safely (external-only egress, not kube-API) | [**#2934**](https://github.com/openova-io/openova/issues/2934) |
| 4 | **Do all charts render cleanly with default values?** | 🟡 24 of 34 charts fail render (missing `charts/` dependency bundle). 16 of those 24 don't carry `smoke-render-mode: default-off` annotation → Blueprint-Release hollow-chart guard FALSELY passes them → customers install + see render errors | [**#2935**](https://github.com/openova-io/openova/issues/2935) |
| 5 | **Do CRDs reject malformed CRs at admission?** | 🟡 Only 2 of 13 CRDs have CEL validations. Blueprint CRD topology missing cross-field rules (`defaults.multi-region not in supported[]` slips through). Application CRD has loose `preserve-unknown-fields` on `spec.parameters` | [**#2936**](https://github.com/openova-io/openova/issues/2936) |
| 6 | **Are auto-generated Secrets persistence-safe?** | 🟡 1 HIGH-RISK — `catalyst-kc-master-admin-credentials` (shipped this session in PR #2918) missing `helm.sh/resource-policy: keep`. 22 other Secrets safe. 7 medium-risk passthrough Secrets are operator-discipline, not chart-discipline | [**#2937**](https://github.com/openova-io/openova/issues/2937) |
| 7 | **Is bp-self-sovereign-cutover idempotent on re-run?** | 🟡 10 of 11 Jobs safe. **Step 09 (gitea-token-mint) is destructive — DELETE old token + mint NEW** → workloads holding old token in cached creds break permanently. cutoverComplete gate doesn't apply when Helm upgrades the chart | [**#2938**](https://github.com/openova-io/openova/issues/2938) |
| 8 | **Any CI workflows quietly red on main?** | 🔴 4 RED right now — Build & Deploy Catalyst (transient Docker Hub timeout, flapping), Test — Bootstrap Kit (flapping), **infra/hetzner — OpenTofu validate (PERSISTENT 6h+ on tofu fmt)**, **Playwright UI smoke Group L (PERSISTENT 3h+)** | [**#2939**](https://github.com/openova-io/openova/issues/2939) |
| 9 | **Pillar 5 anti-tether — beyond blueprint.yaml** | 🔴 **18 VIOLATIONS** across 6 categories: 3 Go const hardcodes (`pinIssuer`, `DefaultIssuer`), 6 Helm chart values defaults, 7 Ingress host specs, 4 catalyst-api env defaults, 1 cloud-init PDNS env. **A successfully-cutover Sovereign still tethered.** | [**#2940**](https://github.com/openova-io/openova/issues/2940) |
| 10 | **Voucher fraud + replay** | 🟢 SECURE — single-use enforced via DB PK + transactional checkout. 🟡 2 hardening gaps — no minimum code entropy (operator can issue `"ACME"`), no per-endpoint rate-limit on /checkout | [**#2941**](https://github.com/openova-io/openova/issues/2941) |

## Severity ranking of new tickets

| Severity | Tickets | Why |
|---|---|---|
| 🔴 **CRITICAL** | #2940 (anti-tether sweep) · #2938 (CI hygiene) | #2940 breaks Pillar 5 independence claim despite cutoverComplete=true. #2938 hides regressions on main. |
| 🟠 **HIGH** | #2933 (switchover timeouts) · #2934 (vpa Cilium trap) · #2937 (cutover Step 09 token rotation) | Each breaks a customer-visible business capability in specific failure modes. |
| 🟡 **MEDIUM** | #2935 (helm hollow-render hygiene) · #2936 (CRD admission boundary) · #2939 (KC master Secret keep annotation) | Defense-in-depth; don't break the happy path but degrade safety. |
| 🟢 **LOW** | #2941 (voucher hardening) | Operator-discipline workaround exists (issue strong codes manually). |

## Cumulative ticket count this session

| Session output | Count |
|---|---|
| PRs merged | 24 (PR #2932 = Wave-1 matrix) |
| Wave-1 follow-up tickets filed | 3 (#2929 RBAC, #2930 test gap, #2931 reload mask) |
| Wave-2 follow-up tickets filed | 9 (#2933 → #2941) |
| **Total new in-progress tickets** | **12** |

## Business-capability rollup (Wave-1 + Wave-2 combined)

| Customer-visible capability | Wave-1 | Wave-2 | Net |
|---|---|---|---|
| Sign up + redeem voucher + see console | 🟢 PASS | 🟡 entropy + rate-limit gaps (#2941) | 🟢 SHIPPABLE |
| Pick multi-region at signup, get it | 🟢 PASS | 🟡 ≤60s RTO at PDM-latency risk (#2933) | 🟢 SHIPPABLE |
| Survive region-kill with zero data loss | 🟢 PASS | 🟡 switchover step timeouts (#2933) | 🟢 SHIPPABLE under typical latency |
| Sandbox + AI assistant w/ org knowledge | 🟡 RBAC gap (#2929 #2930) | (not re-tested) | 🟡 #2929 BLOCKS |
| Fully sovereign post-cutover | 🟢 PASS | 🔴 18 hardcodes (#2940) + Step 09 (#2937) | 🔴 #2940 BLOCKS |
| Silent SSO Launch from any app | 🟢 PASS | (not re-tested) | 🟢 SHIPPABLE |
| Fresh hw87 prov walkable state | 🟢 PASS | 🟡 CI red on main (#2938) + #2934 vpa trap | 🟢 SHIPPABLE (#2934 only affects VPA-enabled provs) |

## Bottom line for founder

**Fresh hw87 prov is SHIPPABLE today.** None of the 9 Wave-2 tickets block bringing up a new Sovereign. The defense work shipped this session flows live on hw87 once you wipe hw86 + prov hw87.

**BUT** — before declaring any Sovereign "independent" via `bp-self-sovereign-cutover`, fix **#2940** (the 18 hardcoded tethers) and **#2937** (Step 09 token rotation). A cutover that completes successfully but still tethers to mothership for OAuth/JWT-issuer/Ingress is worse than a cutover that fails — it presents a false sense of independence.

The 12 new tickets are a healthy outcome of brutal verification — they are gaps the dispatcher (this session's autonomous run) missed. Per memory L7, the verifier agents earned their keep.

---

_Generated by 10-agent parallel UAT Wave-2 verifier attack, 2026-06-03._

---

## Remediation scoreboard — fix-party complete (2026-06-03 ~08:05Z)

After the Wave-2 attack filed 9 gap tickets (#2933–#2941), a parallel
fleet of 7 worktree-isolated fix-agents + inline work remediated them.
A dedicated **audit agent** then brutally re-verified every claimed fix
against the actual code on `main` — **8/8 prior fixes confirmed physically
present, ZERO theater/discrepancies**, all repo-wide gates green.

| UAT gap | Fix PR(s) | Verified on main | State |
|---|---|---|---|
| #2929 Sandbox SA RBAC | #2952 | ✅ grep-confirmed (manifests.go L261/L267) | fixed |
| #2930 Playwright MCP CRD test | #2959 | ✅ test added, gated | fixed |
| #2931 patch-script reload mask | #2956 | ✅ `\|\|true` gone, `exit 4` present | fixed |
| #2933 switchover step timeouts | #2957 | ✅ PDMTimeout+AuditPublishTimeout, tests pass | fixed |
| #2934 bp-vpa Cilium trap | #2946 | ✅ no ipBlock, component:apiserver | fixed |
| #2935 helm hollow-render | #2955 + #2961 | ✅ 15-chart annot + CI dep-build guard | fixed |
| #2936 Blueprint CRD CEL | #2958 + #2960 | ✅ 3 CEL rules + Application bounds | fixed |
| #2937 KC master Secret keep | #2945 | ✅ helm.sh/resource-policy: keep | fixed |
| #2939 CI hygiene | #2943 + #2944 + #2963 | ✅ all 4 lanes green incl Playwright Group L | fixed |
| #2940 Pillar-5 tethers | #2947/#2948/#2949/#2950 + #2962 | ✅ Cat 1/4/5 fixed; Cat 3 = false-positive (.helmignore); Cat 2 cutover-rewrite via #2962 | fixed (Cat-2 cutover walk pending) |
| #2941 voucher hardening | #2953 + #2954 | ✅ rate-limiter + min-entropy, tests pass | fixed |
| #2938 cutover Step-09 idempotency | — | n/a | **deferred — founder rejected the refactor** |

### Honest residuals (NOT closed — founder-gated)

- **#2938** — cutover Step-09 token-rotation idempotency: the operator
  rejected the probe-then-skip refactor branch. Design is documented in
  the ticket body. Awaits founder decision (re-author / kill).
- **All tickets remain OPEN** — per project `CLAUDE.md`, only the founder
  closes after a fresh-prov operator-walk-with-screenshot. PR merge is
  code-verified, NOT pillar-shipped.
- **Live UAT** — the real go-live verification (fresh hw87 prov + visual
  pillar walks) has not run; it's founder-gated via
  `scripts/sovereign-wipe-preflight.sh` → wipe → reprov.

### Bottom line

Code-side remediation of the UAT campaign is **complete and
independently audited** (zero theater). The campaign did its job:
17 verifier agents found 12 gaps, 11 are fixed-and-verified, 1 is
deferred by founder call. The outstanding work is operational
(founder wipe+reprov) — not code.
