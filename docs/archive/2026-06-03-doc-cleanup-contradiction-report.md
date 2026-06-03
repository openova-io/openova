# Doc Cleanup — Cross-Document Contradiction Report (2026-06-03)

> **Slice 6 of the founder-directed exemplary-repo cleanup.** Read-only sweep of
> `docs/adr/`, `docs/ledger/`, `docs/archive/`, plus a cross-document contradiction
> grep across all of `docs/`. This file is the deliverable; it is an **actionable
> checklist** for the canonical-docs agent and readme agent to fix in their own
> slices. The ADR-index fix in this same PR is already applied.
>
> Scope note: this agent may only edit `docs/adr/README.md` and `docs/archive/**`.
> Every fix below that lands in a canonical doc (`STATUS.md`, `GLOSSARY.md`,
> `SRE.md`, `RUNBOOKS.md`, `ARCHITECTURE.md`, `PRINCIPLES.md`) or in `docs/sessions/`
> is **out of scope for this slice** — logged here so the owning agent can fix it.
> Items in `docs/ledger/` are cron-owned and must NOT be hand-edited.

---

## 0. Summary by category

| Category | Count | Severity |
|---|---|---|
| Dead links to deleted/consolidated docs (canonical docs) | 11 link-instances across 5 targets | HIGH — exemplary repo must not ship broken internal links |
| GLOSSARY internal duplicate references | 2 | MEDIUM — cosmetic but in the single-source-of-truth doc |
| Banned-term violations (live docs, non-graveyard) | 1 | MEDIUM |
| Banned-term occurrences (cron-owned ledger — note only) | 1 | LOW (cron will not auto-fix; needs upstream template fix) |
| Ambiguous banned-term usage (Backstage in DOD journey) | 1 | LOW — likely legitimate external-product reference |
| ADR-index anomaly (FIXED in this PR) | 1 | — |
| Stale ledger refresh (note only) | 2 files | LOW — cron is touching mtimes, content lag |
| Archive exact-duplicate (byte-identical, distinct filenames) | 1 pair | LOW — left in place deliberately |

---

## 1. ADR integrity (FIXED in this PR)

- **ADR-0009 was missing from `docs/adr/README.md` index.** The file
  `docs/adr/0009-per-org-iac-repo-bootstrap.md` exists on disk and is `Accepted`,
  but the index table stopped at ADR-0004. **Fixed**: added the ADR-0009 row +
  a numbering note.
- **Numbering gap 0005–0008 is BY DESIGN, not an anomaly.** ADR-0009's own header
  states the gap is reserved for in-flight EPIC ADRs (G92/G105/G108/G112
  candidates) and that 0009 picked its slot deliberately to land without ordering
  coupling. No file is missing; no index entry is dangling. The README now
  documents this so future readers don't "fix" the gap.
- No dangling index entries (every index row points at an existing file). ✅
- No duplicate ADR numbers. ✅

---

## 2. Ledger integrity (READ-ONLY — note only, do NOT hand-edit)

Both ledger files exist and are non-empty:

- `docs/ledger/TRUST.md` — 679 lines, file mtime `2026-06-03T05:40`.
- `docs/ledger/TRACKER.md` — 1845 lines, file mtime `2026-06-03T17:03`.

**Staleness note (for the founder, re: cron health):**

- `TRACKER.md` says **`Last refreshed 2026-06-01T14:08:00Z`** in its body, but the
  file mtime is `2026-06-03T17:03`. The cron is *touching* the file but the
  embedded "Last refreshed" timestamp is ~2 days behind today (2026-06-03). Either
  the refresh script is failing to recompute the body, or it only re-stamps on
  content change. **Worth a glance at `/home/openova/bin/refresh-dod-dashboard.sh`.**
- `TRUST.md` body's most-recent "refreshed" marker is **`2026-05-20`** (the
  5-pillar verification block). That is ~2 weeks stale relative to today. The file
  mtime is fresh (`2026-06-03`), so again the cron touches but may not be
  regenerating the verification block. Given the heavy SSO / G117 work landed
  2026-06-02/03, the per-surface verdicts in TRUST.md are almost certainly
  out-of-date and should be re-walked.

No edits made to either ledger file (cron-owned).

---

## 3. Archive organization

- **One intra-archive byte-identical pair** (left in place deliberately):
  - `docs/archive/2026-05-23-pre-wave-5-57-screenshots/pillar1-voucher-revalidated-post-wave-5.51-LIVE-PASS.png`
  - `docs/archive/2026-05-23-pre-wave-5-57-screenshots/pillar1-voucher-validated-LIVE-marketplace-hw01-PILLAR1WALK-10-OMR-credit.png`
  - These are md5-identical but carry **distinct semantic filenames** (a "validated"
    vs a "revalidated" walk artifact). Deleting either loses the named-evidence
    mapping inside a deliberately-frozen screenshot bundle. Per the conservative
    "archive is the graveyard" guidance, **left both in place**; logging here as an
    optional future trim if the founder wants the bundle deduped.
- **Many cross-directory duplicates exist** between `docs/sessions/**` and
  `docs/archive/2026-05-23-pre-wave-5-57-screenshots/**` (≈30 png pairs). These are
  NOT removed: `docs/sessions/` is owned by another slice, and the archive bundle
  is a deliberate historical snapshot. Cross-dir dedup is out of scope for this
  conservative archive slice.

---

## 4. Dead links — links to docs/ files that were deleted/consolidated (HIGH)

The lean-doc consolidation (2026-05-20) folded several top-level docs into the 7
canonical files but left behind `[...](OLD-DOC.md)` links pointing at the deleted
files. `RUNBOOKS.md:1830` even self-documents the fold ("folded from former
`MULTI-REGION-DNS.md` on 2026-05-20") while other docs still link the dead file.

### 4a. `MULTI-REGION-DNS.md` (DELETED → folded into `ARCHITECTURE.md` §8.8)

- [ ] `STATUS.md:93` — `See [...](PLATFORM-POWERDNS.md) and [...](MULTI-REGION-DNS.md).`
      → retarget to `ARCHITECTURE.md` (lua-record / GSLB section, ~§8.8 / lines 830–853).
- [ ] `GLOSSARY.md:146` — GSLB acronym row `See [...](MULTI-REGION-DNS.md).`
      → retarget to `ARCHITECTURE.md` (GSLB / lua-record section).
- [ ] `SRE.md:22` — `See [...](MULTI-REGION-DNS.md) for the lua-record patterns.`
      → retarget to `ARCHITECTURE.md`.
- [ ] `SRE.md:55` — `... remove unhealthy endpoints ... See [...](MULTI-REGION-DNS.md).`
      → retarget to `ARCHITECTURE.md`.

### 4b. `PLATFORM-POWERDNS.md` (DELETED → folded into `RUNBOOKS.md` + `ARCHITECTURE.md`)

- [ ] `STATUS.md:93` — `See [...](PLATFORM-POWERDNS.md) ...`
      → retarget to `RUNBOOKS.md` §"Pool zone bootstrap" / PowerDNS zone model.
- [ ] `RUNBOOKS.md:100` — `If any line is missing, see [...](PLATFORM-POWERDNS.md) §"Pool zone bootstrap".`
      → self-link: retarget to the in-file PowerDNS section (RUNBOOKS already
      contains the pool-zone-bootstrap content at ~line 71–100). Convert to a
      same-doc anchor or drop the link.
- [ ] `RUNBOOKS.md:1268` — `see [...](PLATFORM-POWERDNS.md) §"Per-Sovereign zone model"`
      → same: retarget to in-file section (RUNBOOKS:1268–1279 IS the per-Sovereign
      zone model).
- [ ] `RUNBOOKS.md:1828` — see-also bullet `[...](PLATFORM-POWERDNS.md)` → drop or
      retarget to `../platform/powerdns/` (the chart) which DOES exist.

### 4c. `SOVEREIGN-PROVISIONING.md` (DELETED → folded into `RUNBOOKS.md`)

- [ ] `ARCHITECTURE.md:1678` — see-also bullet `[...](SOVEREIGN-PROVISIONING.md) — bringing a Sovereign online.`
      → retarget to `RUNBOOKS.md` (provisioning runbook section).

### 4d. `FRANCHISE-MODEL.md` (DELETED → content lives in `RUNBOOKS.md` voucher section / `DOD.md` Pillar 5)

- [ ] `RUNBOOKS.md:1630` — `... document in [...](FRANCHISE-MODEL.md).`
      → retarget to `DOD.md` (Pillar 5 / franchise) or drop the stale "to-do" link.
- [ ] `RUNBOOKS.md:1764` — `Write [...](FRANCHISE-MODEL.md) documenting it as canonical.`
      → this is a stale work-instruction referencing a doc that was consolidated;
      rewrite to point at the canonical home or remove the instruction.

> **Immutable-ADR note (no fix — record only):** `docs/adr/0002-post-handover-sovereignty-cutover.md:117`
> references `INVIOLABLE-PRINCIPLES.md` (renamed to `PRINCIPLES.md`). ADR bodies are
> immutable-additive, so this link is **intentionally frozen** and must NOT be
> edited. Readers should mentally map `INVIOLABLE-PRINCIPLES.md` → `PRINCIPLES.md`.

> **Non-doc note (low priority):** `docs/cloudinit-hetzner-notes.md` contains several
> `# Per INVIOLABLE-PRINCIPLES.md #N` and `# docs/PLATFORM-TECH-STACK.md §8.1`
> references inside **embedded cloud-init code comments** (lines 230, 466, 756, 934,
> 1221, 1280). These are illustrative code comments, not live doc links, but they
> name deleted docs. If that file is kept as canonical, swap to `PRINCIPLES.md` /
> `ARCHITECTURE.md`. (Owner: whoever owns `cloudinit-hetzner-notes.md` — likely the
> canonical/runbooks agent.)

---

## 5. GLOSSARY internal duplicate references (MEDIUM)

`GLOSSARY.md` is the single source of truth, so duplicate references in it read as
sloppiness in the most-important doc.

- [ ] `GLOSSARY.md:105` — the cross-reference sentence lists **`[\`DOD.md\`](DOD.md)`
      twice in a row**: `... Cross-referenced by [...](../CLAUDE.md), [...](RUNBOOKS.md),
      [...](DOD.md), [...](DOD.md), and the user-global ...`. Remove the duplicate
      `DOD.md` token.
- [ ] `GLOSSARY.md:153–157` — the **"See also"** list repeats `DOD.md` twice
      (lines 155 and 157) and `ARCHITECTURE.md` twice (lines 154 and 156).
      De-dup to one bullet each.

---

## 6. Banned-term sweep (per `GLOSSARY.md` §Banned terms)

Most occurrences of banned strings across `docs/` are **legitimate** — they appear
inside the banned-terms documentation itself (`GLOSSARY.md`, `DOD.md` forbidden-
domains tables, `PRINCIPLES.md`, `ARCHITECTURE.md:304`, `STATUS.md:177`) or inside
the `RUNBOOKS.md:1125–1126` grep-guard script that lists the banned words on
purpose. Those are NOT violations.

Genuine / notable hits:

- [ ] **`docs/sessions/2026-05-20-walk-runbook.md:118`** — heading
      `### Pillar 1 ships (Marketplace + voucher onboarding on Nova Cloud)`.
      `Nova Cloud` is a banned term (predecessor brand). → replace with `openova`
      (the Sovereign run by OpenOva). **Owner: sessions-slice / canonical agent.**
- [ ] **`docs/ledger/TRUST.md:587`** (cron-owned — note only) — row
      `| 1 — Marketplace + phone-OTP on Nova Cloud | ...`. Contains banned `Nova Cloud`
      **and** the obsolete `phone-OTP` Pillar-1 step (canon is email-PIN magic-link
      per `docs/sessions/2026-05-19-20-trust-recovery.md` Option B). Cannot hand-edit
      (cron clobbers). **Fix belongs in the cron template / generator source**, not
      the file. Flagging for founder so the generator gets corrected.
- [ ] *(ambiguous — likely OK)* `docs/DOD.md:623–636` — Layla's day-in-the-life
      journey integrates "the customer's existing **Backstage** portal" with the
      Catalyst REST API. `Backstage` is banned only as a name for *the platform's own*
      component (GLOSSARY:116); here it is an **external third-party product the
      customer already runs**, which is the legitimate exemption. **Recommend: leave
      as-is**, but optionally add a parenthetical "(the customer's own third-party
      Backstage, not a Catalyst component)" to make the exemption explicit and
      pre-empt a false-positive on the next banned-term grep.

No `Synapse` (as product), `Workspace` (Catalyst sense), `omantel.openova.io`,
or `eventforge.io` violations found outside the banned-terms documentation and
the graveyard (`docs/archive/**`, which is frozen history and exempt).

---

## 7. Built-state contradiction sweep (STATUS vs ADR vs session reports)

Checked the highest-risk claim — Pillar 3 CNPG synchronous replication — for
STATUS ↔ ADR drift:

- `STATUS.md:77/130/209` claim bp-cnpg-pair ships `remote_apply` synchronous
  replication, **CODE-COMPLETE, awaits fresh-prov region-kill walk**.
- `docs/adr/0004-cnpg-sync-replication.md` (`Status: Accepted`) specifies exactly
  `synchronous_commit: remote_apply` + `synchronous_standby_names: FIRST 1 (...)`.
- **Consistent.** No contradiction. Both correctly state "code shipped, walk
  pending" — neither over-claims a verified region-kill. ✅

No STATUS-vs-ADR built-state contradictions surfaced. (A deeper per-surface
STATUS-vs-live-cluster reconciliation is the TRUST.md ledger's job, not a static
doc sweep — see §2 staleness note.)

---

## 8. Suggested fix ownership

| Items | Owner slice |
|---|---|
| §4a–4d dead links in `STATUS.md` / `GLOSSARY.md` / `SRE.md` / `RUNBOOKS.md` / `ARCHITECTURE.md` | canonical-docs agent |
| §5 GLOSSARY internal dups | canonical-docs agent (GLOSSARY owner) |
| §6 `Nova Cloud` in `2026-05-20-walk-runbook.md` | sessions-slice / canonical agent |
| §6 `Nova Cloud` + `phone-OTP` in `TRUST.md` | founder — fix cron generator template |
| §6 Backstage parenthetical in `DOD.md` (optional) | canonical-docs agent |
| §2 ledger staleness | founder — check `refresh-dod-dashboard.sh` |
| ADR-0009 index + numbering note | **DONE in this PR** |

---

*Generated by the SLICE 6 integrity-check agent, 2026-06-03. All findings are
file:line-cited and were produced by read-only grep across `docs/`. The only
mutating change in this PR is the `docs/adr/README.md` ADR-0009 index fix.*
