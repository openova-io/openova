# Session Retrospective + Next-Session Handoff — 2026-05-19/20 Trust Recovery

> **Audience:** the next Claude session (or the founder, cold). This is the **whole-day** artifact. CLAUDE.md §4 catalogue and `docs/ledger/TRUST.md` cover specific surfaces — this doc covers the cycle.

---

## Section 1 — Headline

**Date range:** 2026-05-19 07:14Z (founder accountability collapse) → 2026-05-20 ~03:00Z (natural session end / mothership wedged).

**One-sentence summary:** Trust collapsed at 07:14Z when founder spot-checked three surfaces (treemap, App Resources tab, Compliance tab) and found **all three broken** despite weeks of "shipped" claims; ~44 PRs landed across the next ~20 hours, ~14 TBD-V issues were filed, 7 audit + 2 design docs were produced, `docs/ledger/TRUST.md` was refreshed twice, and the CLAUDE.md §4 anti-pattern catalogue was extended — every blocker is now either fixed at source-level or filed with a concrete next-step + ownership.

**Final state:** 5/5 pillars remain 🔴 **UNVERIFIED**, but each is **substantively de-risked**. The verification denominator is **frozen** on TBD-V15 (#2020) — mothership substrate is CPU-requests-saturated, blocking all fresh-prov walks until founder authorizes a remediation path. The retrospective discipline holds: PR merge ≠ pillar shipped; only an operator walk + screenshot on a fresh prov flips a pillar 🟢. None today.

---

## Section 2 — Timeline of merged PRs

60 PRs merged into `openova-io/openova` since `2026-05-19T00:00Z` (per `gh pr list --search "merged:>=2026-05-19"`). The pre-collapse PRs (#1933, #1932, the #1935/#1937 revert chain) are the *trigger* of the collapse; the rest is the recovery cycle. Pillar attribution per CLAUDE.md §0.

| Merged (UTC) | PR # | Title | Pillar | Issue ref |
|---|---|---|---|---|
| 05-19 08:20 | #1933 | fix(bp-kyverno): install 19 compliance ClusterPolicies on fresh Sovereign | (op-tool) | TBD-V3, #1929 |
| 05-19 08:24 | #1932 | fix(catalyst-ui): plumb App targetNamespace into Resources tab URL | (op-tool) | TBD-V2, #1928 |
| 05-19 11:08 | #1935 | revert(bootstrap-kit): bp-kyverno pin 1.2.0 → 1.1.0 | (op-tool) | regression of #1933 |
| 05-19 11:53 | #1937 | fix(catalyst-chart): restore Chart.yaml apiVersion+name corrupted by #1932 | (op-tool) | regression of #1932 |
| 05-19 13:07 | #1938 | fix: Resources tab labelSelector → `helm.toolkit.fluxcd.io/name` | (op-tool) | Refs #1928 |
| 05-19 13:52 | #1941 | TBD-A50 closure (ClusterMesh inter-region data plane t34) | 3 | #1941 |
| 05-19 14:16 | #1951 | fix(bp-newapi): point Valkey/Redis host to valkey-primary | 4 | Refs #1944 |
| 05-19 14:22 | #1954 | fix(sme-secrets): reflect into catalyst-system on fresh prov | 1 | Refs #1943 |
| 05-19 14:34 | #1957 | fix(ci): unblock cosmetic gate — disable workflow pending #1956 | (CI) | Refs #1956 |
| 05-19 14:36 | #1955 | fix: openova-flow-server DNS — `.catalyst-system` not `.catalyst` | (op-tool) | Refs #1948 |
| 05-19 14:42 | #1960 | fix(catalyst-chart): catalyst projector valkey.addr → valkey-primary | 4 | Refs #1953 |
| 05-19 14:45 | #1958 | fix(infra): fail-fast on missing Hetzner public IP + ExternalIP assertion (A2 invariant) | 3 | Refs #1941 |
| 05-19 14:53 | #1962 | fix(chart): wrap Helm-templated `value:` fields in quotes | (CI) | Closes #1930 |
| 05-19 14:55 | #1940 | fix(ci): sme-demo spec — visit /sme/users not /console/sme/users | (CI) | #1940 |
| 05-19 14:58 | #1961 | feat(api): /api/v1/sme/bss/overview handler | 1 | Refs #1949 (D-BSS) |
| 05-19 15:03 | #1942 | feat(ui): /jobs page region filter dropdown | (op-tool) | Refs #1821 (D20) |
| 05-19 15:08 | #1939 | fix: treemap leaf-click fires at layer-0 + resolves bare id to AppDetail | (op-tool) | Refs #1927 |
| 05-19 15:23 | #1963 | fix(bp-crossplane): install hcloud provider package + CRDs on fresh prov | 3 | Refs #1947 |
| 05-19 15:25 | #1964 | fix(bp-vcluster): install vcluster.com CRDs on fresh prov | 3 | Refs #1945 |
| 05-19 15:36 | #1967 | fix(bp-catalyst-platform): default MARKETPLACE_ENABLED=true | 1 | Closes #1966 |
| 05-19 15:57 | #1969 | fix: align Application CR watcher to `apps.openova.io/v1` | (op-tool) | Refs #1946 |
| 05-19 16:14 | #1970 | fix(e2e): cosmetic-guards spec re-alignment (α of 2) | (CI) | Refs #1956 |
| 05-19 16:21 | #1971 | fix: default MARKETPLACE_ENABLED=true at source (TBD-V4) | 1 | Closes #1968 |
| 05-19 16:52 | #1973 | fix(e2e): cosmetic-guards spec — mock provision routes (β of 2) | (CI) | Refs #1956 |
| 05-19 16:53 | #1974 | fix: surface secondary-region jobs in deployment jobs endpoint | (op-tool) | Refs #1942, TBD-A63 |
| 05-19 17:57 | #1978 | fix(infra): move Layer 1+2 bash to `write_files` (cloud-init <30720) | 3 | Closes #1977 |
| 05-19 18:00 | #1979 | fix(infra): idempotent ExternalIP reconciler (TBD-A50 layer 3) | 3 | Refs #1941 |
| 05-19 18:04 | #1980 | fix(catalyst-ui): cosmetic regressions (γ of 3) | (CI) | Refs #1976 |
| 05-19 18:58 | #1985 | fix(infra): refactor L3 ExternalIP reconciler to write_files | 3 | Closes #1981 |
| 05-19 20:05 | #1987 | fix(sandbox-controller): emit canonical `SANDBOX_*` env vars | 4 | Refs #1986 |
| 05-19 20:15 | #1988 | feat(sandbox-pty-server): bundle qwen-code+claude-code+aider+opencode | 4 | Refs #1986 B1 |
| 05-19 20:35 | #1992 | feat(sandbox-pty-server): agent catalogue + lazy-spawn on attach | 4 | Refs #1986 B3 |
| 05-19 20:38 | #1991 | fix(bp-flux-stuck-hr-recovery): detect+correct deployed-but-unknown-Ready HRs | 3 | Refs #1989 |
| 05-19 20:57 | #1993 | fix(tenant-route): restore `console.<slug>.<parent>` prefix | 1 | Closes #1990 |
| 05-19 21:38 | #1996 | fix: purge 5 `.openova.io` leaks — tenants reach Sovereign not mothership | 5 | Closes #1994 |
| 05-19 21:55 | #1998 | fix(bp-flux-stuck-hr-recovery): grant `helmreleases/status` patch RBAC | 3 | Closes #1995 |
| 05-19 22:21 | #2004 | fix(chart): bump organization-controller pin `72e3f08`→`c9b58ea` | 3 | Closes #1997 |
| 05-19 22:26 | #2007 | fix(bp-valkey): allow empty password — unblocks bp-newapi (TBD-V12) | 4 | Closes #2003 |
| 05-19 22:29 | #2005 | fix(build-organization-controller): add missing auto-bump pipeline (TBD-A69) | (CI) | Refs #1997 |
| 05-19 22:43 | #2008 | fix(bp-sme): wait for gitea user-bootstrap before provisioning (TBD-V11) | 1 | Closes #2002 |
| 05-19 23:02 | #2009 | fix(sme-notification): align JWT signing secret with catalyst-api (TBD-V8) | 1 | Closes #1999 |
| 05-19 23:11 | #2010 | fix(marketplace): post-checkout redirects to `console.<slug>.<pool-tld>` | 1 | Closes #2001 |
| 05-19 23:21 | #2011 | fix(billing): transactional voucher redemption | 1 | Closes #2000 |
| 05-19 23:42 | #2013 | fix(blueprint-controller): align mode enum with bp-*-vcluster | (CI) | (pre-existing red) |
| 05-19 23:52 | #2017 | fix(sandbox-controller): correct NEWAPI_BASE_URL to actual Service name | 4 | TBD-V14 |
| 05-20 00:04 | #2014 | fix(continuum-witness/cfkv): stabilise `RenewExtendsTTLAndBumpsGeneration` | (CI) | (pre-existing red) |
| 05-20 00:08 | #2018 | fix(self-sovereign-cutover): make state-resume idempotent | 5 | TBD-V13 |
| 05-20 00:11 | #2012 | chore(ci): add auto-bump-images + pkg/** path filter to all `build-*-controller` (TBD-A69) | (CI) | Closes #2006 |
| 05-20 00:42 | #2022 | feat(security/kyverno): split policies into bp-kyverno-policies@1.0.0 Blueprint | (op-tool) | Refs #2019 |
| 05-20 00:48 | #2023 | fix(security/kyverno-policies): annotate `catalyst.openova.io/no-upstream=true` | (op-tool) | Refs #2019 |
| 05-20 01:19 | #2029 | fix(catalyst-ui): wizard StepSuccess CTA → BSS menu (TBD-V20) | 1 | Refs #2028 |
| 05-20 01:29 | #2031 | feat(ci): add `build-projector` workflow + publish to GHCR (TBD-V22) | (CI) | Refs #2030 |
| 05-20 01:44 | #2036 | fix(self-sovereign-cutover): correct totalSteps "8" → "9" (TBD-V25) | 5 | Refs #2035 |
| 05-20 01:54 | #2037 | fix(sandbox-controller): add 4 missing `SANDBOX_*` env vars + LLM_GATEWAY_TOKEN case fix (TBD-V21) | 4 | Refs #2032 |
| 05-20 02:00 | #2038 | feat(marketplace-ui): render configSchema fields on AppDetail (TBD-V18) | 1 | Refs #2026 |
| 05-20 02:13 | #2041 | fix(self-sovereign-cutover): strip mothership-side auths from ghcr-pull Secret (TBD-V24 MISS-2) | 5 | Refs #2034 |
| 05-20 02:22 | #2039 | feat(self-sovereign-cutover): add step 10 — pivot vCluster HRs to Sovereign Harbor (TBD-V24 MISS-1) | 5 | Refs #2034 |
| 05-20 02:30 | #2043 | feat(marketplace-ui): thread configSchema form values into install POST (TBD-V27) | 1 | Refs #2026 |
| 05-20 02:48 | #2044 | fix(catalyst-api/kubeconfig): GET fallback to bare path when region == primary | (op-tool) | Refs #1882 |
| 05-20 02:51 | #2045 | feat(self-sovereign-cutover): step 11 — pivot Crossplane Provider CRs to Sovereign xpkg mirror | 5 | Refs #2034 |
| 05-20 02:52 | #2046 | chore(canon): purge openova.io test-data string leaks | (canon) | Refs #2025 |

**Pillar-tagged subset:** ~26 of these 60 PRs move one of the 5 pillars (the rest are op-tooling, CI repairs, or canon-hygiene). Pillar 1 leads (10 PRs), then Pillar 5 (7), Pillar 4 (5), Pillar 3 (4), Pillar 2 (0).

---

## Section 3 — TBDs filed (status snapshot at session end)

All `TBD-V*` issues from `gh issue list --repo openova-io/openova --search "TBD-V[0-9]"`. Per anti-theater rule 1 ("`Refs #N` is the default; close only after walk + screenshot"), **none of these closed today** with the single exception of TBD-V25 (#2035), which was a literal cosmetic typo (`totalSteps: "8"` → `"9"`).

| Issue # | Title (abridged) | State | Status / Next-step owner |
|---|---|---|---|
| #2015 | TBD-V14: sandbox-controller NEWAPI_BASE_URL service-name mismatch | OPEN | SHIPPED via #2017 (chart pin in flight). Walk-verification owner = next session post-V15. |
| #2016 | TBD-V13: cutover orchestrator state-resume not idempotent | OPEN | SHIPPED via #2018. Walk-verification owner = next session post-V15. |
| #2020 | TBD-V15: mothership catalyst-api Pending — CPU exhaustion | OPEN | **FOUNDER-DECISION** (Path A vs B vs C vs D). Blocks every other pillar walk. See Section 4. |
| #2021 | TBD-V15: catalyst-bootstrap-api `CATALYST_NEWAPI_ADDR` default points at non-existent Service (latent, mirrors V14) | OPEN | IN-PROGRESS — class-bug variant of V14/V17. Fix-author next-session; cheap (~3 LOC). |
| #2025 | TBD-V17: dormant/off-path in-cluster Service-name mismatches | OPEN | IN-PROGRESS — audit (`/tmp/svc-name-mismatch-audit-2026-05-20.md`) enumerates ~6 dormant call-sites. Discrete fix PRs per call-site. |
| #2026 | TBD-V18: marketplace AppDetail does not render `configSchema` | OPEN | SHIPPED via #2038 + #2043. Walk-verification owner = next session post-V15. |
| #2027 | TBD-V19: founder decision — Pillar 1 step 4 phone-OTP vs email-magic-link | OPEN | **FOUNDER-DECISION** (recommendation: Option B = email-magic-link as canon). See Section 4. |
| #2028 | TBD-V20: wizard CTA points at anti-canon `admin.<fqdn>` | OPEN | SHIPPED via #2029. Walk-verification owner = next session post-V15. |
| #2030 | TBD-V22: build-projector CI workflow missing | OPEN | SHIPPED via #2031. Walk-verification = next pin bump after first GHCR publish. |
| #2032 | TBD-V21: sandbox-controller env-var prefix drift residuals | OPEN | SHIPPED via #2037. Walk-verification owner = next session post-V15. |
| #2033 | TBD-V23: cutover step 08 egress-block-test has no deny policy (Pillar 5 theater) | OPEN | **FOUNDER-DECISION** (Approach A recommended; Approach B definitively dead in Cilium 1.16). Design doc ready. See Section 4. |
| #2034 | TBD-V24: Pillar 5 "8-tether pivot" claim — chart pivots 5-7 tethers, no authoritative list | OPEN | IN-PROGRESS — MISS-1 (#2039), MISS-2 (#2041), step-11 (#2045) shipped; MISS-3 still under investigation (`/tmp/investigate-tbd-v24-miss3-2026-05-20.md`). |
| #2035 | TBD-V25: cutover status ConfigMap totalSteps "8" vs chart's 9 steps | **CLOSED** | SHIPPED via #2036 (truly cosmetic; closed via `Closes #N` exception in §5). |
| #2040 | TBD-V26: marketplace.app.install MCP tool blocked by AddApp simulator + auth-bootstrap | OPEN | **FOUNDER-DECISION** (Path A vs Path B; Path B recommended). See Section 4. |
| #2042 | TBD-V27: thread configSchema form values into install POST | OPEN | SHIPPED via #2043 (frontend half). Backend `HelmRelease.values` binding awaits TBD-V26 resolution. |

**Numeric tally:** 14 TBD-V issues OPEN, 1 CLOSED (V25). Of the 14 open: **9 have shipping PRs landed but are WALK-BLOCKED on V15**, **4 require founder decision before fix-author can dispatch**, **1 (V17) is in active investigation phase.**

---

## Section 4 — Founder decisions queued

Each item has an analysis doc ready in `/tmp` (bastion `vmi3305700`). Recommendations are precise so founder can ack with a one-word reply.

### 4.1 — TBD-V15 #2020 mothership substrate (highest priority — blocks everything else)

**Symptom:** mothership single-node k3s is CPU-requests-saturated at 99% (5945m / 6000m). `catalyst-api` (100m request) cannot schedule → `console.openova.io/sovereign/api/v1/deployments` returns 502 → every fresh-prov walk blocked. Live CPU usage is only 44%, so this is a **scheduling-quota problem, not a hardware-load problem**.

**Four ranked paths** (analysis: `/tmp/tbd-v15-mothership-audit.md`, `/tmp/tbd-v15-proposal.md`):

| Path | Action | Time-to-effect | Founder $ | Risk | Headroom freed |
|---|---|---|---|---|---|
| **A — unwedge iogrid stuck rollouts** | `kubectl scale deployment/providers-svc -n iogrid --replicas=0` then back to 1; ditto `workloads-svc` (broken `:scaffold` image tags pin a doubled-booked RS) | <5 min | none | 30–60s iogrid-only downtime | ~300m |
| B — `priorityClassName: system-cluster-critical` on catalyst-api | chart PR drafted, not pushed | ~10 min (chart+Flux) | none | medium — silent tenant eviction | 100m guaranteed |
| C — cpx52 → cpx62 resize | OpenTofu apply, 5–10 min mothership outage | ~30 min | YES | medium (clean resize) | 10000m + 53 GiB |
| D — migrate tenant workloads off mothership | architecturally-correct, multi-week effort | weeks | YES (new VPS) | high (tenant downtime) | ~3500m |

**Recommendation:** **Path A first** (60s to free 300m CPU; zero $ cost; reversible; bounded to `iogrid` namespace). Pair with **Path C this week** (durable headroom) and **Path D in 2-4 weeks** (architecturally correct). I have executed **ZERO** destructive actions — no PRs pushed, no deployments scaled, no pods deleted.

### 4.2 — TBD-V19 #2027 phone-OTP vs email-magic-link

**Symptom:** CLAUDE.md §0 Pillar 1 step 4 says *"Phone-OTP signup (federated mobile-bill identity)"*. Shipped code uses **email + 6-digit PIN magic-link** end-to-end. Zero SMS/phone-OTP plumbing exists anywhere in the repo. The repo even contradicts itself: `docs/FRANCHISE-MODEL.md:79` canonicalises email-magic-link; only `docs/DOD.md:78-79` (aspirational personas doc) mentions phone-OTP. Analysis: `/tmp/analyze-tbd-v19-auth-2026-05-20.md`.

**Two options:**

- **Option A — implement phone-OTP** to match CLAUDE.md §0 verbatim. Requires Twilio/Nexmo/Vonage integration (Sovereign tenancy + cost), Keycloak federation for mobile-bill IdP (none today), SMS budget. Weeks of work.
- **Option B (RECOMMENDED) — adopt email-PIN magic-link as canonical Pillar 1 step 4; update CLAUDE.md §0 wording.** Memory + `FRANCHISE-MODEL.md` + implementation all already match. The PERSONAS-AND-JOURNEYS.md doc is the aspirational leak to be corrected. Zero engineering work — only a docs PR.

### 4.3 — TBD-V23 #2033 Pillar 5 real deny-egress impl

**Symptom:** `bp-self-sovereign-cutover` step 08 ships a placeholder ConfigMap that is **never applied** — the "10-min deny-egress hold proof" required by Principle #11 has been theater. Design doc: `/tmp/design-tbd-v23-deny-egress-proposal.md` (~360 LOC PR scoped).

**Two approaches:**

- **Approach A (RECOMMENDED) — scoped CCNP + probe Pod via label selector.** Two `CiliumClusterwideNetworkPolicy` CRs in the step-08 ConfigMap: (a) scoped default-deny on `catalyst.openova.io/cutover-egress-test=probe` label, (b) `toFQDNs` allowlist for Sovereign-local hosts. Script applies, polls Flux readiness for 10 min, removes, validates restoration.
- **Approach B — `toFQDNs` in `egressDeny`.** **Definitively dead in Cilium 1.16** per upstream policy doc: *"Deny policies do not support … `toFQDNs`."* The current placeholder's author caught this in 2026-05-04 — they were right.

**Recommendation:** Approach A. Blast radius is bounded to the probe Pod + a deliberately-chosen witness (Flux source-controller), not the cluster.

### 4.4 — TBD-V26 #2040 marketplace.app.install Path A vs Path B

**Symptom:** Pillar 4 step 2d ("qwen-code provisions an additional application via MCP") requires a `marketplace.app.install` MCP tool that does not exist (`registry.go:196` reserves the namespace, ships zero handlers). Investigation `/tmp/audit-pillar4-deep-wiring-2026-05-20.md` finding A1 + issue body of #2040 establish **two real blockers**:

- **Blocker 1:** `marketplace-api AddApp` handler is a simulator (`handlers.go:409-433` returns 202 with `{status:"deploying"}`, commits NO HelmRelease, mutates NO state).
- **Blocker 2:** No auth bootstrap path from Sandbox PAT → marketplace-api tenant JWT (independent signing keys).

**Two paths:**

- **Path A — fix marketplace-api end-to-end.** Wire `AddApp` to commit HelmRelease YAML to tenant Gitea repo OR Patch `Organization.spec.apps[]`. Bootstrap a marketplace-api-signed JWT into Sandbox plane via Secret reflector. Doubles the surface area.
- **Path B (RECOMMENDED) — direct Organization CR write via Sandbox SA RBAC.** MCP tool wraps `kubectl patch organization/<slug> --type=merge -p '{"spec":{"apps":[…]}}'` against the chroot cluster. No marketplace-api hop, no auth-bootstrap gap. Mirrors how `sandbox.db.provision` already works.

---

## Section 5 — Audit corpus (7 audit docs + 2 design docs)

All under `/tmp/` on bastion `vmi3305700`. Read-only verification agents only — none of these emit PRs or merge actions.

| Filename | Scope | Key finding | Follow-up issue(s) |
|---|---|---|---|
| `/tmp/audit-pillar1-bss-2026-05-20.md` | Pillar 1 (marketplace + signup) | 5/8 wired-correct; AppDetail configSchema gap, wizard CTA anti-canon, billing transactional gap | #2026, #2028, #2000 |
| `/tmp/audit-pillar4-deep-wiring-2026-05-20.md` | Pillar 4 (Sandbox + MCP) | **0/6 wired-correct end-to-end**; A1 (marketplace.app.install missing), A4 (cosmetic dispatch), B2 (stdio binary deployed as Pod → EOFs), B3 (no mcp.json injection), C1 (newapi default Bank Dhofar not in-cluster Qwen), D2 (no SPIFFE/SVID) | #2040 (V26), #1986 |
| `/tmp/audit-pillar5-cutover-2026-05-20.md` | Pillar 5 (sovereignty cutover) | step 08 ships a placeholder ConfigMap that is never applied — passive Flux-Ready ≠ deny-egress proof | #2033 (V23), #2034 (V24) |
| `/tmp/investigate-tbd-v24-tethers-2026-05-20.md` | Pillar 5 tether audit | 3 concrete MISS findings vs the "8-tether pivot" claim | MISS-1/2 shipped (#2039, #2041); MISS-3 still open |
| `/tmp/investigate-tbd-v24-miss3-2026-05-20.md` | TBD-V24 MISS-3 (the third tether) | Crossplane Provider CRs pull from `xpkg.upbound.io` post-cutover | Folded into #2045 (step 11) |
| `/tmp/svc-name-mismatch-audit-2026-05-20.md` | Service-name pattern hunt (cluster-wide) | ~6 dormant call-sites carry incorrect default `addr` env vars (V14, V17 class) | #2021, #2025 |
| `/tmp/audit-enabled-false-2026-05-20.md` | `enabled: false` defaults audit | 312 findings cluster-wide; **0 safe to flip blindly**; projector image build gap was the actionable surface | Addressed via #2031 (V22) |
| `/tmp/design-tbd-v23-deny-egress-proposal.md` (design) | TBD-V23 implementation design | Approach A (scoped CCNP + probe Pod) recommended; Approach B (`toFQDNs` in egressDeny) dead in Cilium 1.16 | Awaits founder ack on #2033 |
| `/tmp/analyze-tbd-v19-auth-2026-05-20.md` (design) | TBD-V19 founder-decision analysis | Email-magic-link is the shipped reality; phone-OTP is aspirational only | Awaits founder ack on #2027 |

Additional tactical fix summaries in `/tmp/fix-tbd-v*-summary.md` (V13/V14/V18/V18-D/V21-residuals/V24-miss{1,2,3}/V25) — these are per-PR write-ups, not standalone audits.

---

## Section 6 — Substrate state (the verification denominator)

**TBD-V15 #2020 mothership wedged → 0/5 pillar walks possible.** Per anti-theater rule 2 ("validate against fresh state, not stable state"), the existing wedged mothership cannot host a fresh-prov walk; existing T1-T4 walks against t34/t37/t38 are stale (those provs no longer exist). Verification denominator = 0 until founder authorizes a V15 path.

**Walk-blocked PR queue (awaiting fresh-prov verification once V15 unblocks):**

`#1882`, `#2017` (V14), `#2018` (V13), `#2024` (placeholder ID — superseded), `#2029` (V20), `#2031` (V22), `#2036` (V25), `#2037` (V21), `#2038` (V18), `#2039` (V24 MISS-1), `#2041` (V24 MISS-2), `#2043` (V27), `#2044` (kubeconfig-region fallback), `#2045` (cutover step 11), `#2046` (canon-purge), `#2022` (kyverno-policies split), `#2023` (chart no-upstream annotation). **~16 PRs sit in `WALK-BLOCKED` state.**

**Auto-bump pipeline (TBD-A69 #2006) — VERIFIED via downstream evidence.** First true pillar-adjacent verification of any infra fix today: commit `0252dc61` (auto-bump fire) appeared on `openova-io/openova:main` post-#2043 merge, proving the `pkg/**` path filter restoration ships the controller image bump end-to-end. This is the only PR with a non-placeholder verification trail today.

---

## Section 7 — Anti-patterns surfaced today (catalogue extension)

CLAUDE.md §4 catalogue extended via commit `7b6964f2` (added TBD-V14/V15/V16 + 3 new patterns). Surfaced patterns this cycle:

| Pattern | Smell | Detection | Recommended treatment |
|---|---|---|---|
| **Service-name-mismatch in env-var defaults** (V14, V17, V21) | `addr: http://newapi.newapi.svc:3000` default vs actual Service `newapi-bp-newapi.newapi.svc` | grep chart `templates/*/deployment.yaml` env defaults vs `kubectl get svc -A -o name` | Use Helm template ref to `.Values.<chart>.fullname` so defaults and Service name drift in lockstep. |
| **Dormant-bug pattern** (V16) | Two independent code paths exist; only one is hot-tested; the other is wrong but never exercised | grep for parallel implementations of the same surface (e.g. magic-link in both `services/auth` and `bootstrap/api`) | Mark dormant copies with `//go:build off_path` or delete; never let them silently drift. |
| **Substrate vs code** (V15) | Code is correct, fresh prov never reaches the surface because mothership-side substrate is wedged | `kubectl describe pod catalyst-api -n catalyst | grep Insufficient` reveals quota saturation | Treat substrate exhaustion as a P0 blocker; never paper over with retries. Surface a `TRUST.md` "verification denominator frozen" row. |
| **Tenant-on-control-plane** (V15 finding) | Tenant workloads share substrate with control plane | grep mothership namespace list for non-control-plane namespaces | Architectural fix: per-pillar DoD says tenants live in per-Org vClusters on rtz/dmz, not `mgt` mothership (Path D in V15). |
| **IaC-vs-live drift** (V15 finding) | OpenTofu says `product_id: V45 (VPS S)`, live VPS is allegedly cpx52 — manually upsized at some point | `tofu refresh` vs `hcloud server describe` diff | Re-import + re-plan; never trust live state matches the IaC plan after manual ops. |
| **Notification-gap stall** (continuum-witness cfkv) | Test passes intermittently; an "extends TTL" code path silently no-ops in some scheduling order | RenewExtendsTTLAndBumpsGeneration test flake repro | Use deterministic clock injection + invariant assertions (not duration sleeps). Fixed via #2014. |
| **Bulk-template closure overriding live evidence** (trust-audit) | Walker reports verdict on surface but ledger entry says "auto-closed via bulk fixup" | grep TRUST.md for issues marked VERIFIED-PASS with no screenshot link | Reopen + re-walk. The trust-audit `cc0b2271` re-opened #1741, #1819, #1882 on this evidence. |
| **Step orchestrator state-resume non-idempotent** (V13) | Cutover orchestrator stuck at step 5/9; Job complete; orchestrator never advanced after catalyst-api restart | Logs show `step complete` but `currentStepIndex` unchanged across restart | Make state-machine writes durable + checkpoint-on-read. Fixed via #2018. |
| **Walker-skips-default-layer** (already in CLAUDE.md catalogue ref) | Verification agent claims PASS on surface never navigated (ad94ebe2 caught by af4537d2) | Cross-check `.playwright-mcp/*.png` exists AND the XHR log shows the surface was loaded | Verification agents must write the screenshot path to the issue comment AND attach the XHR trace; no exceptions. |

---

## Section 8 — Next session ordering (recommended)

Pick up in this priority order:

1. **Authorize TBD-V15 #2020 Path A** (mothership unblock) — 60-second `kubectl scale` against iogrid, frees ~300m CPU, unwedges fresh-prov walks. Reversible. Zero $. **This is the single highest-leverage action of the entire day.**
2. **Confirm TBD-V19 #2027 Option B** (email-magic-link as canon) — docs-only PR to update CLAUDE.md §0 + delete the phone-OTP wording from PERSONAS-AND-JOURNEYS.md:78-79. Zero engineering, only the brand-narrative cleanup.
3. **Confirm TBD-V23 #2033 Approach A** → ship the real deny-egress impl. Fix-author dispatch is ready (`/tmp/design-tbd-v23-deny-egress-proposal.md`). ~360 LOC PR. **This is the actual proof for Pillar 5.**
4. **Pick TBD-V26 #2040 Path A vs B** (Path B recommended) → ship `marketplace.app.install` MCP tool. This is the missing piece of Pillar 4 step 2d.
5. **Fresh-prov walk verification cycle** → POST a `tNN.omani.works` (avoid `tNN.openova.io` per canon), let bootstrap-kit settle ≤14 min, walk the 10 deterministic steps. **Flip ~16 walk-blocked PRs from UNVERIFIED to VERIFIED-PASS (or surface fresh bugs).**

Items 1-4 are all founder-decision-gated; item 5 is the verification cadence that pays off the whole day's work.

---

## Section 9 — Numeric summary

| Metric | Value |
|---|---|
| **PRs merged** (since 2026-05-19T00:00Z, openova-io/openova) | **60** total / **~44 in trust-recovery cycle** / **~26 pillar-tagged** |
| **TBDs filed** (TBD-V series this cycle) | **15** (V13-V27) |
| **TBDs resolved** (shipped + closed per anti-theater rule) | **1** (V25, cosmetic typo exception per §5) |
| **Audit docs produced** | **7** (pillar 1/4/5, V24 tethers + miss3, svc-name-mismatch, enabled-false) |
| **Design docs produced** | **2** (V23 deny-egress, V19 auth analysis) |
| **TRUST.md refreshes** | **2** (`886a2b36` mid-night add-on; `5cf9d4ba` 02:00Z → 03:00Z late-night add-on) |
| **CLAUDE.md catalogue extensions** | **1** (`7b6964f2`: TBD-V14/V15/V16 + 3 new patterns) |
| **Founder decisions queued** | **4** (V15, V19, V23, V26) |
| **Walk-blocked PRs awaiting fresh-prov verification** | **~16** (enumerated in §6) |
| **Pillars VERIFIED-PASS (🟢)** | **0/5** |
| **Pillars 🔴 UNVERIFIED** | **5/5** (every pillar substantively de-risked at code level; verification denominator frozen on V15) |
| **Hetzner / mothership destructive actions taken** | **0** (anti-theater rule 7: verification is READ-ONLY) |
| **Operator-walk-with-screenshot artifacts attached to issues today** | **0** (verification denominator zero per V15 substrate wedge) |

---

## Cross-references

- CLAUDE.md §0 — 5-pillar DoD definition
- CLAUDE.md §4 — anti-pattern catalogue (extended this session via `7b6964f2`)
- `docs/ledger/TRUST.md` — per-surface 4-state ledger (this session: `886a2b36`, `5cf9d4ba`)
- `docs/ledger/TRACKER.md` — 15-min auto-refresh operator dashboard
- Per-PR fix summaries: `/tmp/fix-tbd-v*-summary.md`
- Per-audit deep-dives: `/tmp/audit-pillar{1,4,5}-2026-05-20.md`
- Founder-decision design docs: `/tmp/design-tbd-v23-deny-egress-proposal.md`, `/tmp/analyze-tbd-v19-auth-2026-05-20.md`, `/tmp/tbd-v15-proposal.md`
- Memory feedback corpus: `~/.claude/projects/-home-openova-repos-openova-private/memory/`

---

**Definition-of-Done reminder (still in force):** PR merge does not flip any pillar from 🔴 to 🟢. The walk + screenshot is the contract. Until the operator (or a verification agent) walks the surface on a fresh prov and attaches evidence to the issue, every "shipped" item above is in `UNVERIFIED` state. The verification denominator is **0** until TBD-V15 (#2020) is unblocked.
