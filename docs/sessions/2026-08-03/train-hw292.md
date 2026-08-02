# RT-1 train manifest — hw292 (cut 2026-08-03, tip `296da3b4c`)

Every rider named with its merge SHA and delivery class. Ancestry table for the
pre-pin set: [`docs/ledger/PATH-TO-100.md`](../../ledger/PATH-TO-100.md) (not
duplicated here, per the plan). Guards re-run on THIS tip, this date — not
carried from an earlier state.

## Gate status at cut (all verified this hour)

- bootstrap-kit go suite: **ok** (full package, `-count=1`)
- `check-bootstrap-kit-pin-sync.sh`: **PASS** (all pins in sync)
- Publish-gate (no hollow pins): bp-postgres **0.2.16**, bp-guacamole **0.2.33**,
  bp-newapi **1.4.146** — all present on ghcr, semver-tag-verified
- Known hazard **#5583**: deploy-bot bumps cover 2 of 5 lockstep sites — the
  guards MUST be re-run at fire time; a green manifest does not survive a
  deploy-bot bump between cut and keystroke. Two occurrences on 2026-08-02
  (repairs `c6e79239f`, `90c69ae46`).

## Class A — aboard image pin `fb41faf30` (ancestry-proven, boots with the image)

The 8-SHA verify-aboard sweep (2026-08-02, all `git merge-base --is-ancestor` proven):
#5528 `3a860b801` (prewarm scarf fallback + fatal gate, chart 0.1.158) · #5529
`3d6b321c1` (org-tenant HelmRepository cutover-aware) · #5506 `f27302982`
(Kyverno APICall RBAC) · #5446 `68ee149c5` (pgdata subPath) · #5424 `fe6f1a288`
(cart shape-validation) · #5438 `32ee47a8d` · #5441 `d202fcc1a` · #5411
`c7c00d771` (launch-URL leg). Plus the prior train set per PATH-TO-100
(#5519/#5526/#5490/#5524/#5478/`a854cbae`+`6844b38f6`/#5533 bp-gitea 1.2.49/
#5522/#5507, cnpg-pair 0.2.23) and the issue-close fixes #5479 `849da91ba`
(#5475) and #5454 `87bbca849` (#5453).

## Class B — merged 2026-08-02/03 post-pin (delivered via the fresh prov's chart/image pulls)

Substantive: #5553 `eb20fc0a3` (vitest gate fail-closed + crash backstop) ·
#5536 `3af4497b6` + #5538 `d2dac4d19` (console placement honesty; closes #3969
children #5422/#5420 on the hw292 render walk) · #5539 `fad88bdb9`
(deterministic catalog gen) · #5540 `d197d747c` + #5541 `3c391e762` (org-create
/ app-install true status) · #5552 `d11685457` + #5556 `dd2a8b931` (A16 CI
guards) · #5579 `4abd41237` (wizard ORG_DEFAULTS #5401 + #5515 Go-side
topology_loader + #5482 divergence test) · #5543 `282253f8f` (true transport
status) · #5537 `c18191116` (SIGPIPE sweep, test scripts only) · #5551
`2d650eb6f` (janitor honest counts, #5545) · #5338 `e5d632a21` (dr-shared-pg
Continuum cnpgPair — #5311's standby-probe engage, runtime proof on the walk) ·
docs #5549 `32214128c` / #5550 `b187b474b` · lockstep repairs `c6e79239f` +
`90c69ae46` (catalog regeneration hash-proven idempotent) · 24 dependabot
security/maintenance merges (x/net, x/crypto, golang-jwt, glog, grpc, js-yaml,
undici, brace-expansion, fast-uri, et al. — vulnerability banner 411→321 over
the day).

## Excluded from this train — every exclusion named

- **Held for post-cc=true thaw**: #5534 (#5485 defects 4-6), #5535 (#5488
  cutover-abort hardening — its absence defines the cutover-abort branch)
- **Founder-gated**: #5386 (§854 tamper-evidence, conflicts with merged
  successors), #5329-vs-#5356 pick (break-glass wipe)
- **Red, needs code**: #4023 (protobuf 1.31→1.33 breaks `platform/huawei` build)
- **Dependabot skipped-red**: refresh on their own rebases; none security-critical
  beyond the merged set

## Fire-day checklist (Phase C step 1, restated against this manifest)

1. Re-run BOTH guards + pin-sync on the then-tip (hazard #5583) — a stale PASS is void
2. Registry enumeration proves no other env exists (curl is corroboration, not instrument)
3. Fresh `scripts/prov-preflight.sh` PASS line committed same-day
4. reset-uat already banked at `429a39f76` — do NOT re-run
5. MERGE FREEZE declared at keystroke; nothing merges until cc=true
6. Founder sequence: scale-up #5558 → keystroke (POST /sovereign/api/v1/deployments)

Refs #960 #5265 #5583 #5558
