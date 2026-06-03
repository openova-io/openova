# UAT Acceptance — Section 4: Structural P0 Fixes + Repository Quality

> Part of the 2026-06-03 OpenOva UAT Acceptance walk. Pre-filled with the work that
> shipped, the live evidence captured at write-time, and a runnable **re-verify** step
> per row so the founder can independently re-confirm before signing the Acceptance column.
>
> Run every `How YOU verify it` command from the repo root on an up-to-date `main`
> (`git fetch origin main && git checkout main && git pull`). Rows marked 🔄 need a
> **live Sovereign** (post-cutover) and cannot be confirmed from the repo alone.

---

## 4a. Structural p0 fixes

| # | What was fixed (structural risk) | How YOU verify it | What we tested + evidence | Acceptance |
|---|---|---|---|---|
| **#2842** (PRs **#2868** + **#2966** + **#2921** + **#2956**) | `harbor.openova.io` mirror was configured for 7 public registries on every node **but had no robot-token credentials** → containerd got `401` on every uncached pull, stalling fresh provs. Fix seeds the harbor auth block (`auth.password = ${harbor_robot_token}`) into cloud-init **registries.yaml** for **both** providers **and both** node roles — #2868 (Hetzner CP + Huawei), #2966 closed the missing **Hetzner worker** template, #2921 ships the operator script to patch the **existing fleet's** nodes, #2956 hardened that script so a failed `systemctl reload` no longer gets masked. | `grep -n 'harbor.openova.io' infra/providers/hetzner/cloudinit-control-plane.tftpl infra/providers/hetzner/cloudinit-worker.tftpl infra/providers/huawei/cloudinit-control-plane.tftpl infra/providers/huawei/cloudinit-worker.tftpl` then `grep -rn 'harbor_robot_token' infra/providers/hetzner/ infra/providers/huawei/` — every one of the 4 templates must reference `harbor.openova.io` AND the auth block must interpolate `${harbor_robot_token}`. Operator fleet script: `ls tools/ \| grep -i 'patch-existing-nodes'` (from #2921/#2956). | All 4 cloudinit templates carry the `harbor.openova.io` mirror; the auth `password: "${harbor_robot_token}"` block is present in both worker templates (`hetzner/cloudinit-worker.tftpl:123–125`, `huawei/cloudinit-worker.tftpl:82–84`) and the token is seeded into both CP templates via `${base64encode(harbor_robot_token)}` (e.g. `hetzner/cloudinit-control-plane.tftpl:158`). All 4 PRs MERGED to `main`. | ✅ |
| **#2861** (PR **#2910**) | `bp-sso-bridge` Pod could not reach **any** Kubernetes Service via SVC-DNS on hw86. RCA: Cilium translates `ipBlock 0.0.0.0/0` into the `reserved:world` identity, which **excludes** the special `reserved:kube-apiserver` identity → every egress to the kube-apiserver (`10.96.0.1:443`) was dropped (`Policy denied identity …->kube-apiserver`). Fix replaces the `0.0.0.0/0` ipBlock with a direct `podSelector` on the kube-apiserver Pod (`component: apiserver`) so the reconciler can list HRs / write ExternalSecret CRs. | `grep -n 'ipBlock\|0.0.0.0/0' platform/sso-bridge/chart/templates/networkpolicy.yaml` — the only hits must be inside **comments** (the RCA note), never a live `to: - ipBlock: cidr: 0.0.0.0/0` rule for the kube-API. Then `grep -n 'component: apiserver' platform/sso-bridge/chart/templates/networkpolicy.yaml` — the apiserver `podSelector` egress rule must be present. | `networkpolicy.yaml` has **zero** live `ipBlock 0.0.0.0/0` egress rules for the kube-API — the only `ipBlock` / `0.0.0.0/0` strings are in the RCA comment block (lines 56–61). The `component: apiserver` podSelector egress rule is present (line 73). PR #2910 MERGED to `main`. | ✅ |
| **#2951** (PR **#2962**) | After cutover, a Sovereign must pull **exclusively** from its own Gitea + Harbor — but `bp-sso-bridge`'s image host still pointed at the mothership `harbor.openova.io`. The cutover Step now rewrites the per-Blueprint Helm `image.registry` default to pivot the `bp-sso-bridge` image host off the mothership Harbor. | **Post-cutover, needs a LIVE Sovereign.** After the `bp-self-sovereign-cutover` Jobs complete, on that Sovereign's kubeconfig run: `kubectl get hr -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.spec.values.image.registry}{"\n"}{end}' \| grep harbor.openova.io` → must return **zero** rows outside intentionally-mothership-pinned paths. Source side (verifiable now): `git log --oneline origin/main \| grep 2962` shows the cutover Step's image-host rewrite landed. | Source: PR #2962 MERGED to `main`; the cutover Step's per-Blueprint image-host rewrite is in place. Live confirmation deferred to the post-cutover egress-block hold of the next fresh-prov cutover walk. | 🔄 |

---

## 4b. Repository quality (deep-clean)

| Area | What was cleaned | How YOU verify it | Evidence | Acceptance |
|---|---|---|---|---|
| **Rubbish purge** | 485 files removed — 58 committed root-level screenshots + 426 `playwright-mcp` debug dumps + stray `.lock` files — and `.gitignore` hardened so screenshots/dumps can't be re-committed. | `git ls-files '*.png' \| grep -vE '^docs/\|brand/\|component-logos/\|\.pr-evidence/'` → empty (no stray root/session screenshots; the only committed PNGs are doc assets, brand assets, the component-logo set, and curated `.pr-evidence/`). Also confirm guards: `grep -nE 'playwright-mcp\|test-results\|\.png' .gitignore`. | PR **#2968**. Re-verified on `origin/main`: zero stray screenshots committed at root or under `docs/sessions/`; the 17 remaining `*.png` are all legitimate UI/component-logo/`.pr-evidence` assets under `products/catalyst/bootstrap/ui/`. | ✅ |
| **Canonical docs accuracy** | 11 dead intra-doc links repaired, stale "what's built" claims corrected, and cross-document contradictions resolved across the 7 canonical docs (`GLOSSARY` / `STATUS` / `ARCHITECTURE` / `DOD` / `PRINCIPLES` / `RUNBOOKS` / `SECURITY`). | `for f in GLOSSARY STATUS ARCHITECTURE DOD PRINCIPLES RUNBOOKS SECURITY; do echo "== $f =="; grep -oE '\]\(([^)]+\.md[^)]*)\)' docs/$f.md \| sed -E 's/.*\(([^)#]+).*/\1/' \| while read p; do [ -e "docs/$p" ] \|\| [ -e "$p" ] \|\| echo "DEAD: $p"; done; done` → no `DEAD:` lines for intra-repo `.md` targets. | PR **#2973**. Re-verified on `origin/main`: intra-repo links in the 7 canonical docs resolve; the contradiction pass folded findings into `docs/archive/2026-06-03-doc-cleanup-contradiction-report.md`. | ✅ |
| **Sessions prune** | 70 transient session reports archived, duplicate reports deleted, and a `README` index added to `docs/sessions/`. | `ls docs/sessions/README.md` (index present) and `git log --oneline origin/main \| grep 2971` (PR landed). Spot-check no duplicate session basenames remain: `ls docs/sessions/ \| sort \| uniq -d` → empty. | PR **#2971**. Re-verified on `origin/main`: `docs/sessions/README.md` index present; archived reports moved under `docs/archive/`. | ✅ |
| **Legacy subdirs consolidated** | `docs/proposals/`, `docs/runbooks/`, and `docs/lessons-learned/` were folded into the canonical **`RUNBOOKS.md §12` debugging cookbook**; the three legacy directories are deleted. | `ls docs/lessons-learned docs/proposals docs/runbooks 2>&1` → all three report **`No such file or directory`**. Confirm the content landed: `grep -n '§12 — Debugging cookbook' docs/RUNBOOKS.md` → matches. | PRs **#2969** + **#2974**. Re-verified on `origin/main`: all three legacy dirs gone; `RUNBOOKS.md` carries `## §12 — Debugging cookbook — hard-won lessons` (line 1819) with subsections 12.1–12.8+. | ✅ |
| **Component READMEs** | Banned-terms violations and dead links corrected across `platform/*/README.md` and `products/*/README.md` to match the canonical glossary/architecture docs. | `grep -rliE '\bBackstage\b\|\bSynapse\b\|\bWorkspace\b\|\bNova Cloud\b' platform/*/README.md products/*/README.md` → empty (no audited banned terms in component READMEs). | PR **#2970**. Re-verified on `origin/main`: component READMEs free of the audited banned terms; dead links repaired. | ✅ |
| **ADR / ledger integrity** | ADR-0009 (per-Org IaC repo bootstrap) re-indexed in `docs/adr/README.md`; a cross-doc contradiction report filed under `docs/archive/`. | `grep -n '0009' docs/adr/README.md` → ADR-0009 row present with `Accepted` status. `ls docs/archive/2026-06-03-doc-cleanup-contradiction-report.md` → file present. | PR **#2972**. Re-verified on `origin/main`: `docs/adr/README.md:15` lists ADR-0009 `Accepted` (with the deliberate 0005–0008 reserved-gap note); contradiction report present in `docs/archive/`. | ✅ |

---

## Summary

| Row | Acceptance |
|---|---|
| 4a · #2842 harbor robot-token (both providers, CP + worker + fleet script) | ✅ |
| 4a · #2861 sso-bridge Cilium kube-apiserver identity | ✅ |
| 4a · #2951 cutover pivots bp-sso-bridge image host off mothership Harbor | 🔄 (post-cutover, live Sovereign) |
| 4b · Rubbish purge (#2968) | ✅ |
| 4b · Canonical docs accuracy (#2973) | ✅ |
| 4b · Sessions prune (#2971) | ✅ |
| 4b · Legacy subdirs consolidated (#2969 + #2974) | ✅ |
| 4b · Component READMEs (#2970) | ✅ |
| 4b · ADR / ledger integrity (#2972) | ✅ |

**8 of 9 rows ✅ verified on `main`. 1 row 🔄** — #2951 confirms only after the next fresh-prov cutover walk reaches the post-cutover egress-block hold.
