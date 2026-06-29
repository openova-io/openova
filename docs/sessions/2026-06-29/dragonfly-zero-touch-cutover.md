# Session 2026-06-29 — Dragonfly P2P registry → deterministic zero-touch cutover

**Goal (founder):** drive omantel.biz (kom4dc Sovereign) to a **deterministic zero-touch
state** — provision → converge → handover → **cutover** → `cutoverComplete` with **zero
manual touch, including the cutover**. Root blocker: the cutover registry-pivot wedged on
kom4dc (nodes cannot hairpin to their own public EIP). Solution: replace the
bastion-proxy + per-cloud hairpin hacks with **Dragonfly P2P**.

## Shipped

| PR | What | Status |
|---|---|---|
| #4640 | `bp-dragonfly` — per-node dfdaemon P2P registry layer (slot 06) | ✅ merged |
| #4641 | cutover registry-pivot → dfdaemon mirror + hostAlias (kills #4637 kom4dc hairpin) | ✅ merged |
| #4642 | qaFixtures provs auto-fire cutover on handover (zero-touch incl. cutover, #4061-safe) | ✅ merged |
| #4643 | ADR-0012 — Dragonfly registry architecture decision record | ✅ open |
| — | `bp-catalyst-platform:1.4.955` published (auto-cutover wiring) | ✅ |

Design: `docs/ideation/dragonfly-p2p-registry-cutover-redesign.md` · Decision: `docs/adr/0012-…`
· Findings: issue #4639.

## WBS / Gantt

```
            ◀──────────────── COMPLETE ────────────────▶│◀ now ▶│
0   Design (stress-tested: cloud-agnostic/DR/isolation)   ████████          ✅
1   bp-dragonfly (chart, slot 06)                             ████████      ✅ #4640
2   Validate on live kom4dc (deploys+runs, dfdaemon :4001)        ████████  ✅ #4639
2b  Defuse 5/5 VPC quota landmine                                  █████     ✅ 1/5
3   Cutover-rewrite (dfdaemon mirror + upstream flip + hostAlias)    ██████  ✅ #4641
4.2 Auto-cutover-on-handover wiring (qaFixtures, #4061-safe)          █████  ✅ #4642
4.x ADR-0012 (decision record)                                        ███   ✅ #4643
─────────────────────────────────────────────────────────────────────────────────
4.3 fresh kom4dc qaTestEnabled e2e prov (fbf043d2)                       🔄  IN FLIGHT
4.4 converge → handover → AUTO-cutover → cutoverComplete                 ⏭️  ◀ last gate
```

## Key decisions / findings

- **dfdaemon = node-local containerd mirror** (`127.0.0.1:4001`, hostNetwork) — generic,
  no per-cloud branch. Each plane/cluster **self-contained**; **no cross-plane mesh**
  (preserves plane-isolation; blast radius = 1 cluster).
- **The #4637 root is the token realm:** dfdaemon is hostNetwork → inherits the node
  hairpin, so Harbor's `Www-Authenticate` realm (`https://registry.<fqdn>/service/token`)
  hairpins. Fix = `hostAlias` `registry.<fqdn>` → harbor ClusterIP. (Captured in #4639.)
- **Zero-touch scope:** qa/test provs auto-fire the cutover; **customer Sovereigns stay
  operator-gated** (BSS CTA) per #4061.
- **Operational traps captured to memory:** (1) catalyst-api in-memory store does not
  reload on recycle → wipe via workdir `tofu destroy`; (2) merging a bp-catalyst-platform
  change then firing a prov → the mothership catalyst-api rolls and abandons the prov →
  pre-flight `rollout status` + no in-flight Build-&-Deploy CI before firing.

## Follow-ups (non-blocking)

- Pre-bake dfdaemon+Cilium into the node image (k3s airgap bundle) so bootstrap needs no
  upstream (the dfdaemon "egg").
- Per-plane registry footprint sizing (full Harbor vs lightweight per-plane registry).
- Sweep leftover OBS buckets from wiped throwaway provs.

## Acceptance (pending)

The e2e prov `fbf043d226aca6f1` (single-region kom4dc, `qaTestEnabled`) must walk
**converge → handover → auto-cutover → `cutoverComplete`** through the 600s deny-egress
hold with no manual touch. That is the one outstanding gate; everything upstream is done.
