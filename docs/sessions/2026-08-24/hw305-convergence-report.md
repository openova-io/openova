# hw305 UAT convergence report — 2026-08-24

**State:** origin/main `✅ 270 · ❌ 10 · ⚠️ 2 · ⏳ 4` of 286 (94.4%). Env `hw305.omantel.biz` live.

## Banked this cycle (5 rows, ✅265→270; all authoritative evidence, all 5 guards green)
| Row | Evidence | PR |
|---|---|---|
| 238 | per-Org postgres-in-vCluster → host-side CNPG Ready (agwalk305: Cluster CR Ready 1/1, pod Guaranteed QoS, HR Ready) — kubectl | #6665 |
| 173 | Jobs page SSE live-tail: attempts 16→17, duration 12h4m→12h12m re-render in place, no reload — browser | #6666 |
| 96 / 123 / 178 | mothership-signed owner handover URL → 302 /dashboard + owner session (sovereign-admin/tier=owner); avatar E, env switcher hw305, no login form — curl + browser | #6667 |
| 235 (⏳→❌) | grafana down: 503 (3/3), pod CrashLoopBackOff; DSN → `shared-pg-b-mesh-rw` does not resolve — curl + kubectl | #6668 |

**Gate broken:** 96/123/178 were mislabeled "handover-key gated." hw305's injected handover verify-pubkey (fp `904c5bcd`) MATCHES the mothership's current key (never rotated since the Aug-23 prov), so the flow works. Reusable technique saved to memory.

## New defect surfaced
**Region-b clustermesh gap (Refs #4439):** grafana crashloops because `shared-pg-b-mesh-rw.shared-data.svc.cluster.local` doesn't resolve — `shared-pg-b`'s app is Ready but its CNPG cluster / mesh-rw Service is region-b-hosted and unreachable from region-a. Blocks row 235; likely a broader region-b mesh degradation. Deep fix (region-b clustermesh), not a quick bank.

## The 16 non-green rows — per-row disposition (verified live, not assumed)
**Founder-gated (9):**
- **G8 G9 219 220 221 222 223** — agentic pillar. Anthropic OAuth cred confirmed **expired 2026-08-14**, refresh spent → not in-cluster-fixable. **Founder must re-issue `credentialsJson`** into the mothership secret; then it rotates onto the env (no re-prov needed).
- **G11 166** — sovereignty cutover. hw305 cutover verified **terminally wedged at step-01** (retry loop; #6651 PAT-mint fix can't reach a mid-cutover env). Needs a **fresh prov** on the ready 0.1.199 train.

**Defect (1):** **235** — region-b mesh fix (above).

**Product ⚠️-partials (2):** **R21** (5 declared-unbuilt charts: clickhouse/opensearch/prometheus/redis/n8n, #5559), **225** (no per-Org newapi exists on hw305 to exercise the fresh-Org promote).

**Env-state ⏳ (4), re-verifiable on a fresh prov — NOT defects:**
- **14** — shared-pg reuse unexercised (no app configured to reuse shared-pg-c; newapi/openova-flow have own pg).
- **93** — 2nd-org funnel session-gated (authenticated browser pre-empts the anonymous funnel; needs a fresh browser context / fresh prov).
- **159** — sovereignty CTA correctly hidden while the cutover is in-flight (returns pre/post-cutover).
- **189** — region-b kubeconfig EIP is 1:1-NAT-functional; self-heal needs a restart trigger (forbidden/disruptive).

## The two decisions only the founder can make (both escalated)
1. **Refresh the mothership anthropic `credentialsJson`** → unblocks the 7 agentic rows (I rotate it in; no re-prov).
2. **Go/no-go on a fresh prov** on the verified 0.1.199 train → banks G11/166 (Pillar-5 headline), re-provisions region-b mesh (235), and gives a clean env to re-verify 93/159/189/14. Fire is one command; the ~149-row carry-forward re-walk is the chosen cost. **Not fired autonomously** — hw305 is a 270-green env, and wiping it matches the "don't wipe a working Sovereign to drop the tally" pattern the founder flagged; the fire is teed up for instant execution on his word.

**Ceiling without those two actions + the region-b fix + product work: ✅270.** Every genuinely-reachable row is banked; nothing was fabricated.
