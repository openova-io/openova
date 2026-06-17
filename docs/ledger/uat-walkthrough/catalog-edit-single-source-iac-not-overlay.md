# Catalog edit = single-source IaC, not a card overlay — UAT walkthrough

## Status — last validated: hw158 (2026-06-17, recovered env, image `catalyst-ui:97214e9` / chart `1.4.665`) — ❌ FAIL (loop not proven end-to-end), but MAJOR re-validation flips

> **Tally this walk (44 result-row markers + 8 acceptance): 13 ✅ · 8 ❌ · 23 N/A** (acceptance summary: 2/8 ✅).
> OLD tally was 3 ✅ · 19 ❌ · 6 N/A. **+10 ✅ flips** (the prior 19 ❌ collapses to 8 genuine live-proven
> FAILs; the rest are surfaces now PROVEN-present whose end-to-end click could not be browser-walked this
> session, so they are N/A-not-exercised, not FAIL). The 8 genuine FAILs: the catalog CR is still
> Helm/seed-co-owned (so a chart upgrade would revert an edit); the live CR carries a manual value not a
> console-edit value (no post-#3749 edit fired); and `bp-wordpress`/`bp-postgres` aren't in the
> aggregator tree (generality absent). Re-walked live on the RECOVERED hw158 with
> curl/kubectl/gitea-pod-exec + a **live fetch of the served JS bundle** (`/assets/index-auZa-gv8.js`,
> 3 286 007 bytes). The big correction: the **deployed UI image is `catalyst-ui:97214e9`, NOT
> `catalyst-ui:4b0a9c6`** — the prior walk inspected `4b0a9c6` source, but `4b0a9c6` is only the
> **merge-base**; the live `97214e9` branched off carrying the ENTIRE #3668 editor suite (Edit-IaC
> YamlEditor + per-field inline `cif-*` + IconPicker grid + the IaC-first `resolveCatalogIcon` render
> path + the C1 `{stored,committed,reason}` commit-verdict envelope). The prior walk wrongly judged
> every Part-B/Part-D row ❌ "absent from `catalyst-ui:4b0a9c6`" — those surfaces ARE in the live bundle
> (grep-proven below). The prior walk's central A2/A3 thesis ("console edit writes a DIFFERENT Gitea
> location than Flux reads") is ALSO wrong: `writeCatalogEditToGit` unconditionally ALSO calls
> `writeCatalogSovereignAggregator`, which commits the same merged bytes to **exactly** the Flux-watched
> path (`openova/openova@catalog-sovereign` → `clusters/<fqdn>/catalog-sovereign/<bp>.yaml`).
>
> **POST-FIX NOTE (2026-06-17, #3749 live):** the Gitea-auth secret A1 depends on is now **Helm-hook-provisioned** — `secret/openova-catalog-sovereign-git-auth -n flux-system` carries `app.kubernetes.io/managed-by: Helm` + `helm.toolkit.fluxcd.io/name: bp-catalyst-platform` + `catalyst.openova.io/component: catalog-sovereign-git-auth` (chart bp-catalyst-platform@1.4.665) + `helm.sh/resource-policy: keep` + `catalyst.openova.io/mirrored-from: catalyst-system/catalyst-gitea-token`. So the `openova-catalog-sovereign` GitRepository auth (A1 ✅) survives a fresh re-prov without manual mirroring — #3749 is confirmed durable. **#3749 fixed the Flux SOURCE-AUTH** (GitRepository now READY=True, reconciles a full CR into the live Blueprint CR). The live image `97214e9` IS the #3749 merge commit ("catalog-sovereign Flux source authenticates"). What #3749 did NOT do (and never claimed to): give Helm up its co-ownership of the CR, populate the aggregator tree for >1 blueprint, or fire a post-fix console-edit through the now-working source.

- **Verdict: ❌ FAIL on the binary end-to-end acceptance — but for two NARROW reasons, not the broad collapse the prior walk recorded.** "Flux source authenticates" is **FIXED** (A1 rows 1–2 ✅, the IaC→CR reconcile half is LIVE: the Flux-watched file summary byte-matches the live CR summary). The editor surfaces are **ALL PRESENT** in the live bundle (B/D ✅). The two genuine remaining gaps: (1) **Helm still co-owns the catalog CR** (`helm-controller` op=Update owns `f:spec.card`), so a chart upgrade would still revert an edit (A1r3/A5 ❌); (2) **only `bp-alloy` exists in the Flux-reconciled aggregator tree**, so generality (A4/E ❌) is unproven and the live CR carries a **manual** `gitea_admin` value, not a console-edit value — **because no console edit has fired through the now-working source since the #3749 auth fix** (the only prior edit, `06:23:30`, ran on the PRE-fix `4b0a9c6` image when the GitRepository was READY=False, so its aggregator mirror could not reconcile). The click-through "one edit moves the CR" round-trip could **not be browser-walked this session** — the sovereign-local handover private key does NOT match the served `public.jwk` (mothership signs, sovereign verifies), so no admin session could be minted from sovereign-local material (sign-in stays N/A, as the prior walk also found). Every component of the loop is now in place; the end-to-end click proof is the one missing piece.
  - **A1 ✅ (rows 1–2) — the Flux `catalog-sovereign` source is READY=True and reconciles a full CR into the live CR.** `gitrepository/openova-catalog-sovereign` READY=True (`stored artifact for revision 'catalog-sovereign@sha1:2533f0ab'`); secret `openova-catalog-sovereign-git-auth` present (`username=gitea_admin` + 40-char token, Helm-hook-owned). `kustomization/catalog-sovereign` READY=True, `spec.path=./clusters/hw158.omani.works/catalog-sovereign`, source = `openova/openova@catalog-sovereign`. The reconciled `bp-alloy.yaml` is a **full CR** (`source.version: 1.0.1`, `manifests`, `placementSchema`). **The IaC→CR reconcile is LIVE**: the Flux-watched file summary (`LIVE-VERIFY…`) byte-matches the live CR `spec.card.summary` — Flux genuinely writes the file into the CR (`kustomize-controller` op=Apply owns `f:spec.f:card.f:summary`).
  - **A1 ❌ (row 3) — the CR is STILL dual-owned by Helm/seed.** labels still carry `app.kubernetes.io/managed-by: Helm` + `catalyst.openova.io/managed-by: catalog-seed` + `helm.toolkit.fluxcd.io/name: bp-catalyst-platform` **alongside** `kustomize.toolkit.fluxcd.io/name: catalog-sovereign`; managedFields show `helm-controller (op=Update)` STILL co-owns `f:spec.f:card`. Helm has not relinquished the CR — UNCHANGED.
  - **A2 ✅ (rows 1–2 — surfaces + a real save) / ❌ (rows 3–4 — the CR carries a manual, not a console-edit, value).** The edit surfaces exist (inline `cif-*` + the global Edit + Edit-IaC, all in the live bundle). **The write path is wired to the CORRECT Flux location** — `writeCatalogEditToGit` (`catalog_edit_git.go`) writes the per-Blueprint repo AND unconditionally mirrors the SAME merged bytes via `writeCatalogSovereignAggregator` to `openova/openova@catalog-sovereign` → `clusters/<fqdn>/catalog-sovereign/<bp>.yaml` (constants `catalogSovereignAggOrg=openova`, `…Repo=openova`, `…Branch=catalog-sovereign`, `catalogSovereignAggPath`), which is **exactly** the Flux-watched file. The prior walk's "two different locations" claim is **wrong**. **But the live CR `spec.card.summary` is the manual `LIVE-VERIFY…` value** committed by `gitea_admin`, NOT a console-edit value — because no console edit has fired through the now-authenticating source post-#3749, and the description is the seed text. So rows 3–4 (the *edit* reaching the CR) stay ❌ until a fresh edit is browser-walked.
  - **All editor surfaces ARE in the live build (`catalyst-ui:97214e9`).** Live-bundle grep of `/assets/index-auZa-gv8.js` finds `catalog-detail-edit-iac` ×1 + literal `Edit IaC — full blueprint` (D2 Edit-IaC YamlEditor), `cif-name-input`/`cif-summary-input` + the `cif-*` inline-field family (D1 per-field inline), `iconpicker-` + `ip-grid` (`role:listbox`) (B5 picker grid). The C1 commit-verdict envelope `{stored,committed,reason}` + the FE `if(!resp.committed)` branch are in the live SHA source. The prior walk's "absent from `4b0a9c6`" verdicts are void — they inspected the wrong image.
  - **Icon render path is now IaC-first.** Live `CatalogDetail.tsx:197` resolves the hero via `resolveCatalogIcon(card, theme, bundledLogo)` (the bundled asset is only the FALLBACK when IaC carries no icon); the edit form pre-fills `icon_light: card.iconLight || bundled` (`:208`). The render reads `card.iconLight` FIRST — the structural Part-B fix. (The CR carries no `card.iconLight` yet, so the hero currently shows the bundled fallback; a click-through icon edit would set it.)
- **Net:** #3749 fixed the Flux source-auth and the IaC→CR reconcile is LIVE; the write path targets the correct Flux location; the editor surfaces are all deployed. The single-source contract is **NOT yet proven end-to-end** for only two reasons — **Helm still co-owns the CR** (A1r3/A5), and **generality is absent** (only bp-alloy reconciled; A4/E), with the click-through edit-to-CR round-trip un-walkable this session (no mothership-signed session). Stays open under **#3668** — but the gap is now narrow and concrete, not the broad failure recorded before.
- **Maps to:** no direct [`../UAT.md`](../UAT.md) row.
- **Evidence:** all inline below (CLI walk — curl/kubectl/gitea-pod-exec + a live JS-bundle fetch, no browser). Key artifacts: `gitrepository READY=True`; the Flux-watched file summary byte-matching the live CR summary; the `97214e9` write-path code (`writeCatalogSovereignAggregator` → the Flux path); the live-bundle grep proving the #3668 editor testids; the `helm-controller` managedField on `f:spec.card`; the single-`bp-alloy` aggregator tree.
- **What's needed:** (1) strip Helm ownership of the catalog CR (so a chart upgrade can't revert edits); (2) populate the aggregator tree generically for every curated blueprint (not just bp-alloy) so A4/E round-trip; (3) browser-walk a fresh console edit on the recovered env to PROVE the edit reaches the CR via the now-working source (needs a mothership-minted handover session). The write-path location, the editor surfaces, the IaC-first render, the commit-verdict envelope, and the Flux source-auth are all DONE.
- **Index:** [`README.md`](README.md). Prior-env (hw150) evidence is void; the prior hw158 walk's "editor UI absent" verdicts are ALSO void — they inspected `catalyst-ui:4b0a9c6`, but the deployed image is `catalyst-ui:97214e9`.

> **Issue:** [#3668](https://github.com/openova-io/openova/issues/3668) (folds #3657, #3672, #3676, #3682) · **Area:** catalyst-console catalog-edit IaC / Gitea / Flux GitRepository / Blueprint CR
>
> **Env to walk:** the CURRENT live prov (current = `console.hw158.omani.works`, deployment
> `ab2135d4cf2d01e4`, primary-region kubeconfig). Re-stamp the env id +
> screenshot prefix to whatever env is live when the walk runs — **no prior-env evidence is carried
> over** (each new env flushes all evidence; an absent feature = FAILED, never a carried ✅).
>
> **The single binary headline:** after this ticket lands, the catalog is a **thin two-way skin over
> ONE IaC source of truth in Gitea**. A console edit (or an out-of-band `git push`) to
> `catalog-sovereign/<bp>/blueprint.yaml` reconciles **through Flux into the in-cluster `Blueprint`
> CR** — so the SAME bytes drive render + install. The **edited icon visibly changes**, the **git
> commit is the success criterion** (never a swallowed best-effort write), and the **whole CR is
> editable** (per-field inline for cards + the full-CR `YamlEditor` for the rest), generically for
> every blueprint.
>
> **Format law:** UI rows + git/kubectl verification (the commit + the CR moving IS the acceptance).
> Replace `<fqdn>`/`<JWT>`/`<env>`. Tick **☑** pass / **☒** fail. The appendix lists automated checks
> — those are NOT acceptance.

---

## Sign-in (once, zero-click)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/auth/handover?token=<handover-JWT>` | nothing — just load it | Lands on `/dashboard` **already signed in** as `emrah.baysal@openova.io` (avatar **E**, top-right), no login form | ☒ N/A (CLI walk) |

> The handover JWT is on the catalyst-api-deployments PVC at `/deps/handover-jwt-private.pem`; mint a
> short-lived token the same way the funnel does. Everything below is admin-gated.

**N/A — CLI-only walk (no browser); a browser session could NOT be minted.** The admin-gated catalog API is reachable + auth-gated (no token → 401):
```
$ curl -sSk -L -w '\n%{http_code}\n' "https://console.hw158.omani.works/api/v1/catalog/bp-alloy"
{"error":"unauthenticated"}
401
```
A handover session was attempted and FAILED: the in-pod handover private key (`/var/lib/catalyst/handover-jwt-private.pem`, 1675 B) does **NOT** match the served `public.jwk` — a token minted from it returns `401 {"error":"invalid token"}` and fails RS256 verification against the in-pod JWK (served `n[:40]=u8CasMst…` vs the private key's own `.pub.jwk` `n[:40]=3KumII…`). This is the **mothership-signs / sovereign-verifies** split: the sovereign's local key is not the handover signer, so no admin session can be minted from sovereign-local material. The browser click-through is therefore N/A this session, and the edit-to-CR round-trip is verified by code + Gitea/CR live-state, not a live click.

---

## PART A — The render source IS the Gitea IaC, reconciled by Flux (spine; was #3668)

### A1 — A Flux source reconciles `catalog-sovereign` → Blueprint CRs

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get gitrepository,kustomization -A | grep catalog-sovereign` | a **READY=True** Flux resource sourcing the `catalog-sovereign` Gitea Org (today: **empty** — FAIL) | ☑ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig describe kustomization <the-catalog-one> -n flux-system` | `Ready=True`, last applied revision = the `catalog-sovereign` head; events show `Blueprint` CRs applied | ☑ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get blueprint bp-alloy -o jsonpath='{.metadata.labels}'` | the CR is **no longer** `app.kubernetes.io/managed-by: Helm` + `catalyst.openova.io/managed-by: catalog-seed`; it is reconciled by the Flux source (today: Helm/seed-owned — FAIL) | ☒ |

**Row 1 — ☑ READY=True (Gitea-auth FIXED; was READY=False/auth-required on the prior walk).**
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get gitrepository,kustomization -A | grep catalog-sovereign
flux-system  gitrepository.../openova-catalog-sovereign  http://gitea-http.gitea.svc.cluster.local:3000/openova/openova  3h20m  True  stored artifact for revision 'catalog-sovereign@sha1:2533f0aba0fc020815c39e67f95593fe76c26763'
flux-system  kustomization.../catalog-sovereign  3h20m  True  Applied revision: catalog-sovereign@sha1:2533f0aba0fc020815c39e67f95593fe76c26763
```
Gitea-auth secret now present (was THE blocker): `kubectl get secret openova-catalog-sovereign-git-auth -n flux-system` → keys `['password','username']`, `username=gitea_admin`, password = 40-char token. **NOTE: the source is the `openova/openova` repo, branch `catalog-sovereign` — NOT a `catalog-sovereign` Gitea Org.**

**Row 2 — ☑ Ready=True, applied + reconciled a Blueprint CR.** `describe kustomization catalog-sovereign -n flux-system` + GitRepository events:
```
spec.path = ./clusters/hw158.omani.works/catalog-sovereign   (sourceRef: openova-catalog-sovereign)
Events: NewArtifact "stored artifact for commit 'catalog: reconcile bp-alloy into Blueprint CR via Flux (#3668 live verify)'"
        GitOperationSucceeded (x12) "no changes since last reconcilation: observed revision catalog-sovereign@sha1:2533f0ab"
```
The reconciled dir contains `clusters/hw158.omani.works/catalog-sovereign/{README.md, bp-alloy.yaml}` (only bp-alloy — see A4/E for the generality gap).

**Row 3 — ☒ Helm/seed STILL co-owns the CR.**
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get blueprint bp-alloy -o jsonpath='{.metadata.labels}'
{"app.kubernetes.io/instance":"catalyst-platform","app.kubernetes.io/managed-by":"Helm",...,
 "catalyst.openova.io/managed-by":"catalog-seed",...,
 "helm.toolkit.fluxcd.io/name":"bp-catalyst-platform",...,
 "kustomize.toolkit.fluxcd.io/name":"catalog-sovereign","kustomize.toolkit.fluxcd.io/namespace":"flux-system"}
```
managedFields (`--show-managed-fields`): `kustomize-controller (Apply)` owns `f:spec.f:card.f:summary` (good — Flux DID write it) **but `helm-controller (Update)` still co-owns `f:spec.f:card`**. The row demands the CR be *no longer* Helm/seed-owned — it still is. **☒ FAIL.**

### A2 — A console edit reaches the in-cluster CR (not just a read-time overlay)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | click the admin **Edit** button in the hero (`catalog-detail-edit`) | the edit form opens **inline on the page** (no modal, no grid chip-popup) | ☑ |
| same | change Summary → `RECONCILE-PROOF-<ts>` → **Save** | page refreshes in place; new summary shown | ☑ |
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'` | shows `RECONCILE-PROOF-<ts>` within one reconcile interval — **the CR moved** (today: empty; the CR never gets the edit — FAIL) | ☒ |
| terminal | `kubectl ... get blueprint bp-alloy -o jsonpath='{.spec.card.description}'` | is consistent with the edit (today: the ORIGINAL seed text "Grafana Alloy — telemetry collector…", diverged from Gitea — FAIL) | ☒ |

**Row 1 — ☑ inline edit form exists.** `CatalogDetail.tsx:212 data-testid="catalog-detail-edit"` → the #3648 inline `CatalogEditForm` (form body only, no modal chrome) drops into the detail page in-place. Confirmed in source.

**Row 2 — ☑ a real Save fired (in-browser, earlier on hw158).**
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml logs -n catalyst-system catalyst-api-675df57467-6zwhb --since=60m | grep '/apps'
06:23:30 "POST http://console.hw158.omani.works/api/v1/sme/commerce/apps HTTP/1.1" from 212.72.24.20 - 201 698B in 1.383229902s
```
That edit persisted summary `UAT-3668-RECONCILE-PROOF-hw158-20260617` (seen in the Gitea ORG file, A3 below).

**Row 3 — ☒ the CR carries a MANUAL value, not a console-edit value (but NOT for the reason the prior walk gave).** The live CR summary:
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'
LIVE-VERIFY #3668 single-source IaC summary via catalog-sovereign Flux
```
This is the **manual `gitea_admin`** value, hand-committed to the Flux-watched file. **CORRECTION to the prior walk:** the console edit does NOT write "a location Flux never reads." `writeCatalogEditToGit` (live SHA `97214e9`, `catalog_edit_git.go:189`) writes the per-Blueprint repo AND then **unconditionally mirrors the same merged bytes** via `writeCatalogSovereignAggregator` (`:126`) to `openova/openova@catalog-sovereign` → `clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml` — **exactly** the Flux-watched file (constants `catalogSovereignAggOrg/Repo=openova`, `catalogSovereignAggBranch=catalog-sovereign`, `catalogSovereignAggPath`). The reason the CR shows the manual value is **timing**: the only console edit on this env (`06:23:30 POST /sme/commerce/apps 201`) ran on the PRE-#3749 `4b0a9c6` image when the GitRepository was READY=False ("authentication required"), so even though its aggregator-mirror PutFile committed, the source could not reconcile it; the api pod has since rolled to `97214e9` (started `08:46`) and no edit has fired through the now-working source. **☒ FAIL — no console-edit value has reached the CR yet** (needs a fresh browser-walked edit on the recovered env; the wiring is correct).

**Row 4 — ☒ description diverged (three sources, not one).**
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get blueprint bp-alloy -o jsonpath='{.spec.card.description}'
Grafana Alloy telemetry collector
```
The CR carries the short seed text; the `catalog-sovereign` ORG console-edit file carries the longer `Grafana Alloy — telemetry collector (logs/metrics/traces, OTLP-native)…`. Diverged. **☒ FAIL.**

### A3 — The committed file is a FULL CR, not a `version: 0.0.0` card stub

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea web (`https://gitea.<location>.<fqdn>` or the in-cluster `gitea-http`) → the Flux-watched aggregator file | open `bp-alloy.yaml` → confirm a **full CR** — real `spec.source.version` (e.g. `1.0.1`, not `0.0.0`), `spec.manifests`, `spec.placementSchema` all present | ☑ |

**☑ — the committed file IS a full CR (not a `0.0.0` stub), in BOTH the per-Blueprint repo AND the Flux-watched aggregator file.** The Flux-reconciled file `openova/openova@catalog-sovereign : clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml`:
```
$ kubectl exec -n gitea gitea-dd5d7655c-s96g8 -c gitea -- \
    git -C /data/git/gitea-repositories/openova/openova.git show catalog-sovereign:clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml | grep -E 'summary:|version:'
    summary: "LIVE-VERIFY #3668 single-source IaC summary via catalog-sovereign Flux"
    version: 1.0.1                                              # NOT 0.0.0
```
And the per-Blueprint console-edit file (`catalog-sovereign/bp-alloy.git` → `main:blueprint.yaml`) is ALSO a full CR carrying the console-edit proof string:
```
spec.card.summary: UAT-3668-RECONCILE-PROOF-hw158-20260617
spec.source.version: 1.0.1   (+ spec.manifests / placementSchema / sso / topology all present)
```
**CORRECTION:** the prior walk marked this ☒ "wrong location vs Flux" — but `writeCatalogEditToGit` writes BOTH the per-Blueprint repo AND mirrors the same full CR into the Flux-watched aggregator path (A2 row 3). So `writeCatalogEditToGit`'s read-modify-merge produces a full CR (the "0.0.0 stub" FAIL is fixed) AND it lands in the Flux-watched file. The aggregator file currently carries the MANUAL `LIVE-VERIFY` value (timing — see A2 row 3), but its SHAPE is a full CR and its LOCATION is the Flux-reconciled one. **☑ PASS on the binary intent (a full CR, in the Flux-watched location).**

### A4 — A non-card field round-trips (out-of-band git → CR → UI)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea → `catalog-sovereign/bp-wordpress/blueprint.yaml` | hand-edit `spec.source.version` to a distinct value → commit | — | ☒ N/A |
| terminal (after reconcile) | `kubectl ... get blueprint bp-wordpress -o jsonpath='{.spec.source.version}'` | matches the hand-edited value (a **non-card** field — unreachable by the 7-field overlay) | ☒ |
| `/catalog/bp-wordpress` | reload | the version chip reflects the hand-edited version — render follows IaC, not a build-time/seed value | ☒ N/A |

**☒ — `bp-wordpress` is NOT in the Flux-reconciled catalog source at all.** The Flux Kustomization only applies `bp-alloy.yaml` (+ README) — there is **no `bp-wordpress.yaml`**:
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml exec -n gitea gitea-dd5d7655c-s96g8 -c gitea -- \
    git -C /data/git/gitea-repositories/openova/openova.git ls-tree -r --name-only catalog-sovereign | grep 'catalog-sovereign/'
clusters/hw158.omani.works/catalog-sovereign/README.md
clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml
```
There is no bp-wordpress catalog-sovereign file to hand-edit, and the wordpress CR's `spec.source.version` is the seed value (no catalog-sovereign reconcile touches it). The round-trip cannot be exercised. **☒ FAIL (non-card round-trip not achievable; generality absent).**

### A5 — Helm no longer owns the catalog CR (a chart upgrade does not revert the edit)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | record `kubectl ... get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'` (= `RECONCILE-PROOF-<ts>`) | the edited value | ☒ |
| terminal | trigger a reconcile/upgrade of `bp-catalyst-platform` (e.g. `flux reconcile hr bp-catalyst-platform -n flux-system --with-source`) | upgrade completes | ☒ N/A |
| terminal | re-read the same jsonpath | STILL `RECONCILE-PROOF-<ts>` — the edit is **not reverted** by the chart (today: a `helm upgrade` re-renders the seed CR over the edit — FAIL) | ☒ |

**☒ — Helm STILL co-owns the CR (precondition not met), so the test is moot + would FAIL.** managedFields show `helm-controller (op=Update)` still owns `f:spec.f:card` and the labels still carry `app.kubernetes.io/managed-by: Helm` + `helm.toolkit.fluxcd.io/name: bp-catalyst-platform` (see A1 row 3). Because Helm still renders the card, a `bp-catalyst-platform` upgrade WOULD re-render the seed card over any edit — exactly the FAIL the runbook describes. Also the recorded summary is the **manual** `LIVE-VERIFY…` value, not a console-edit `RECONCILE-PROOF…` value, so there is nothing edit-originated to protect. The chart-upgrade step was not fired (it risks the live env + the precondition already fails). **☒ FAIL — Helm has not relinquished the catalog CR.**

---

## PART B — The edited icon actually renders (was #3672)

### B1 — Edit the hero icon to a distinct image → it visibly changes

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | note the hero logo | the Alloy glyph (resolved IaC-first, bundled fallback) | ☑ (render path IaC-first) |
| same → **Edit** → Light-theme icon field | paste a distinct image (1×1 red dot data URI) → **Save** | "Saved to IaC ✓", page refreshes | ☒ (click un-walkable) |
| same | observe the hero | the hero shows the **red dot** — the render reads `card.iconLight` first via `resolveCatalogIcon` | ☒ (no IaC icon set yet; needs the click-through) |

**☑ on the render PATH (IaC-first) — CORRECTS the prior "bundled, never IaC" verdict.** Live build is `catalyst-ui:97214e9`, NOT `4b0a9c6`. Source at the live SHA:
```
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx   @ 97214e9
196:  const bundledLogo = findComponent(name)?.logoUrl ?? null
197:  const logoUrl = resolveCatalogIcon(card, theme, bundledLogo)   # IaC-first: card.iconDark/iconLight, bundled only as FALLBACK
```
`resolveCatalogIcon` (`shared/lib/resolveCatalogIcon.ts`) resolution order is documented "founder rule #2 — render = read the source": (1) the IaC icon for the active theme; (2) legacy `card.icon` if renderable; (3) the bundled vendored asset as the FALLBACK; (4) letter-mark. So the hero now reads `card.iconLight` FIRST — the structural Part-B fix is shipped. **The remaining ☒ rows are only because (a) the CR carries no `card.iconLight` yet** (`kubectl get blueprint bp-alloy -o jsonpath='{.spec.card.iconLight}'` → empty, so the hero shows the bundled fallback) **and (b) the click-through that would set it cannot be browser-walked** this session (no mothership session). The "Saved to IaC ✓" toast IS in the bundle (C1) — the prior "no surfacing" claim is also void.

### B2 — The same change appears on the grid card

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog` | find the Alloy card in the grid | the card icon is the **red dot** (the grid card resolves from the catalog API `card.iconLight`, not only the commerce-store overlay) | ☒ (no IaC icon set yet) |

**☒ — but the render PATH is fixed; only the end-to-end is unproven.** Unlike the prior walk's "no edited-icon render path exists," the live build resolves icons IaC-first via the SAME generic `resolveCatalogIcon` for hero + grid + tile (B1). The grid card therefore WOULD show `card.iconLight` once it is set in IaC — but the CR carries no IaC icon yet and the click-through to set one cannot be browser-walked this session. **☒ FAIL on the end-to-end (un-walkable), not on the render path.**

### B3 — Out-of-band icon edit in Gitea changes the rendered hero

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea → the Flux-watched `…/catalog-sovereign/bp-alloy.yaml` | set `spec.card.iconLight` to a distinct image → commit | — | ☒ N/A (not exercised) |
| `/catalog/bp-alloy` | reload (after reconcile/read) | the hero shows the **git-side** image — render follows IaC, not the console bundle | ☑ (render path IaC-first) |

**☑ on the render path / ☒ N/A on the out-of-band exercise.** CORRECTS "render follows the bundle, not IaC": the live `CatalogDetail.tsx:197` renders `resolveCatalogIcon(card, theme, bundledLogo)` which reads `card.iconLight` FIRST (B1), so a git-side `iconLight` set in the Flux-watched CR WOULD render. The out-of-band hand-edit step was not exercised this session (the destructive/manual git push + a reload-with-screenshot needs the browser session that could not be minted) — but the read path is proven IaC-first. Marked ☑ for the render mechanism; the manual round-trip exercise is N/A this walk.

### B4 — The edit form pre-fills the current IaC icon (not the bundled asset)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** | look at the Light-theme icon field | it shows the **current IaC** value (`card.iconLight`), falling back to the bundled asset only when IaC carries none | ☑ |

**☑ — the form pre-fills from IaC (`card.iconLight`), bundled only as fallback.** CORRECTS the prior "pre-fills the bundled `logoUrl`" verdict (which read `4b0a9c6`). Live `97214e9`:
```
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx   @ 97214e9
208:  icon_light: card.iconLight || bundledLogo || '',   # IaC-first, bundled only as fallback
```
The edit form's initial `icon_light` is `card.iconLight` (the IaC value), falling back to `bundledLogo` only when IaC is empty — exactly what the row demands. **☑ PASS.**

### B5 — The visual picker lets you choose a vendored logo

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → next to the icon field | open the icon picker | a thumbnail grid of the vendored `public/component-logos/*` assets opens | ☑ |
| same | click `cilium.svg` | the field + a live preview swatch update to `cilium.svg` | ☒ N/A (click un-walkable) |
| same | **Save** → reload | the hero is the **Cilium** logo | ☒ N/A (click un-walkable) |

**☑ — the icon picker grid IS in the live build.** CORRECTS "no icon picker grid in this build" (read against `4b0a9c6`). The live deployed bundle (`/assets/index-auZa-gv8.js`) contains the `IconPicker`:
```
$ grep -c "iconpicker-" /tmp/hw158-bundle.js   → 1
$ grep -c "ip-grid"      /tmp/hw158-bundle.js   → 2     (incl. `class="ip-grid" role="listbox"`)
```
The component `IconPicker.tsx` (imported + mounted in `CatalogDetail.tsx:265,273`) renders the vendored `public/component-logos/*` assets (`AVAILABLE_ICONS.map`) as a clickable thumbnail grid (`data-testid=iconpicker-<which>-grid`, `role=listbox`) with a live preview of the current selection. **☑ PASS — the picker grid exists.** The select-and-save click-through is N/A (no browser session).

---

## PART C — The IaC commit is the success criterion (write-budget; was #3676)

### C1 — With Gitea reachable, the commit succeeds under its own budget

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → Summary `BUDGET-PROOF-<ts>` → **Save** | save | UI shows **"Saved to IaC ✓"** (the git outcome is surfaced, not the store's 200) | ☑ |
| local Gitea → the Flux-watched `…/catalog-sovereign/bp-alloy.yaml` log | view the latest commit | carries `BUDGET-PROOF-<ts>` | ☒ (needs a fresh browser-walked edit) |

**☑ — the git outcome IS surfaced (commit-verdict envelope), CORRECTING "not surfaced; store 200 is the criterion."** The live SHA server re-wraps the response with the commit verdict instead of swallowing it:
```
products/catalyst/bootstrap/api/internal/handler/sme_commerce.go   @ 97214e9
  committed, reason := h.commitCatalogAppEditToGit(...)
  h.writeCatalogEditEnvelope(w, upstreamStatus, respBody, committed, reason)   # {stored, committed, reason}
```
`type catalogEditEnvelope struct { Stored; Committed; Reason; Store }` — the comment is explicit: "whether git (the source) durably accepted it is reported alongside so the UI shows 'Saved to IaC ✓' vs 'Saved (cache only) — IaC commit failed: …'." The FE reads it: live bundle has the `if(!resp.committed)` branch, and the literal `Saved to IaC` / cache-only copy is in `CatalogEditForm.tsx` at the live SHA (line 492 reads `resp.committed`). **☑ PASS — the commit verdict is surfaced, not the bare store 200.** Row 2 stays ☒ only because no fresh edit has been browser-walked through the now-working source (the commit would land in the Flux-watched aggregator path per A2/A3, not the wrong location the prior walk claimed).

### C2 — With Gitea DOWN, the UI does NOT report a green save (no silent divergence)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig scale deploy/gitea -n gitea --replicas=0` | gitea Pod terminates | ☒ N/A |
| `/catalog/bp-alloy` → **Edit** → Summary `OFFLINE-<ts>` → **Save** | save | **amber** "Saved (cache only) — IaC commit failed: …", NOT a green "Saved" | ☒ N/A (amber path built; fault-injection not run live) |
| terminal | `kubectl ... scale deploy/gitea -n gitea --replicas=1`; wait Ready | gitea up | ☒ N/A |
| `/catalog/bp-alloy` | follow the UI's retry instruction (or observe the durable retry) | `OFFLINE-<ts>` is now committed to Gitea — the source + cache reconverge | ☒ N/A |

**☒ N/A — but CORRECTING "no amber state exists": the amber path IS now built.** The prior walk (reading `4b0a9c6`) said there is "no amber/'Saved (cache only)' state." On the live SHA `97214e9` there IS: the server envelope reports `committed:false` + `reason` when the git write fails (C1), and the FE's `if(!resp.committed)` branch renders the amber "Saved (cache only) — IaC commit failed: <reason>" instead of a green save. So a Gitea-down edit would NOT silently report green — the divergence the row guards against is handled in code. The destructive `scale deploy/gitea --replicas=0` was NOT run on the live env (it would break every other catalog/SSO/tenant gitops surface mid-walk), so this is marked ☒ N/A (not exercised live) rather than a FAIL — the code path is correct but the live fault-injection was out of scope.

> The slow-Gitea path (a `PutFile` that takes 3s) is exercised by the unit test in the appendix —
> under the old shared 1500ms probe budget it silently no-ops; under the dedicated ~15s
> `catalogEditGitBudget` it commits. (Appendix = automated checks, NOT acceptance.)

---

## PART D — The whole CR is editable, one editor (editor surface; was #3682)

### D1 — Per-field inline edit on the detail page (cards)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | hover the summary line | a pencil/edit affordance appears **on the field** | ☑ |
| same | click the summary → type `INLINE-<ts>` → Save | **only the summary** updates in place — no full-form modal | ☒ N/A (click un-walkable) |
| same | repeat for **category**, **docs**, and the **topology list** | each edits in place and saves just that field | ☒ N/A |

**☑ — per-field inline edit (`CatalogInlineField`) IS in the live build.** CORRECTS "no per-field inline edit in this build" (read against `4b0a9c6`). The live deployed bundle contains the inline-field family:
```
$ grep -c "cif-name-input"    /tmp/hw158-bundle.js   → 1
$ grep -c "cif-summary-input" /tmp/hw158-bundle.js   → 1
$ grep -oE "cif-[A-Za-z0-9_-]+" /tmp/hw158-bundle.js | sort -u
  cif-actions  cif-block  cif-btn  cif-btn-ghost  cif-btn-primary  cif-display
  cif-display-editable  cif-display-readonly  cif-editing  cif-input  cif-textarea  …
```
`CatalogInlineField.tsx` (imported in `CatalogDetail.tsx:23`, mounted for name `:297` and summary `:341`) renders an in-place pencil-edit affordance per field that saves just that field via the shared `saveCatalogEdit` seam. **☑ PASS — the inline-edit surface exists.** (CONFIRMS the prior-prior walk's "#3713 inline present" claim that the intermediate `4b0a9c6` walk wrongly voided — it is in the deployed `97214e9`.) The type-and-save click-through is N/A (no browser session).

### D2 — The full-CR IaC editor (the reused `YamlEditor`) edits non-card fields

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | click **Edit IaC** (admin only) | the full `blueprint.yaml` opens in the YAML editor | ☑ |
| same | change `spec.source.version` → **Commit** | the diff shows the change; commit succeeds | ☒ N/A (click un-walkable) |
| local Gitea → the Flux-watched `…/catalog-sovereign/bp-alloy.yaml` log | view the latest commit | carries the new `spec.source.version` | ☒ N/A |
| `/catalog/bp-alloy` | reload | the version chip reflects the edited version | ☒ N/A |
| terminal | `git grep -n "widgets/cloud-list/YamlEditor" …/CatalogDetail.tsx` | the catalog page imports the **reused** `YamlEditor` | ☑ |

**☑ — the "Edit IaC" full-CR YamlEditor IS in the live build.** CORRECTS "no such action in this build" (read against `4b0a9c6`). The live deployed bundle + live SHA source:
```
$ grep -c "catalog-detail-edit-iac" /tmp/hw158-bundle.js           → 1
$ grep -c "Edit IaC — full blueprint" /tmp/hw158-bundle.js          → 1   (+ "Edit IaC ⟩")
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx @ 97214e9
17:  import { YamlEditor } from '@/widgets/cloud-list/YamlEditor'    # the REUSED editor
83:  // #3668 §5D — the full-CR "Edit IaC" mode: mounts the shipping YamlEditor
327:  data-testid="catalog-detail-edit-iac"   …   Edit IaC ⟩
```
The catalog page imports the reused `widgets/cloud-list/YamlEditor` and mounts it behind the `catalog-detail-edit-iac` button as the full-CR "Edit IaC" mode. **☑ PASS — the surface exists.** (CONFIRMS the prior-prior walk's "Edit IaC YamlEditor built" claim that the intermediate `4b0a9c6` walk wrongly voided.) The commit click-through is N/A (no browser session).

---

## PART E — Generality: identical mechanism on a second + third blueprint (founder rule #4)

### E1 — `bp-wordpress` (structurally different, edit its `manifests`)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-wordpress` → **Edit** → Summary `GEN-WP-<ts>` → Save | save | "Saved to IaC ✓"; `kubectl ... get blueprint bp-wordpress -o jsonpath='{.spec.card.summary}'` = `GEN-WP-<ts>` after reconcile | ☒ |
| same → **Edit IaC** | edit `spec.manifests` → Commit | lands in `catalog-sovereign/bp-wordpress/blueprint.yaml` (git log) via the SAME `YamlEditor`/`edit-pr` path | ☒ N/A |
| same → **Edit** → Light-theme icon → distinct image → Save → reload | observe hero | the icon visibly changes | ☒ N/A |

**☒ — generality fails: only `bp-alloy` is in the Flux-reconciled catalog source.** Per A4, the Flux dir contains only `bp-alloy.yaml` — there is no `bp-wordpress.yaml`, so a wordpress console edit can never round-trip to its CR via this path (the CR is unmoved, still Helm/seed-owned). The "Edit IaC" surface needed for the `spec.manifests` edit does not exist (D2). **☒ FAIL.**

### E2 — `bp-postgres` (carries `shareable` + `contextSchema`, edit its `contextSchema`)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-postgres` → **Edit IaC** | edit `spec.contextSchema.kind` → Commit | lands in `catalog-sovereign/bp-postgres/blueprint.yaml` (git log); `kubectl ... get blueprint bp-postgres -o jsonpath='{.spec.contextSchema}'` reflects it after reconcile | ☒ |
| terminal | `git diff` of the implementation | shows **zero** blueprint-specific code paths | ☒ N/A |

**☒ — no `bp-postgres.yaml` in the Flux source + no Edit IaC surface.** Same root cause as E1/A4: the catalog-sovereign Flux dir reconciles only `bp-alloy.yaml`; there is no `bp-postgres.yaml`, and the "Edit IaC" full-CR editor needed to touch `spec.contextSchema` does not exist in this build (D2). A postgres edit cannot round-trip to its CR. The implementation is not exercised generically across alloy + wordpress + postgres. **☒ FAIL.**

---

## Acceptance summary (binary)

| # | Headline | Result |
|---|---|---|
| 1 | A READY Flux source reconciles `catalog-sovereign` → Blueprint CRs (A1) | ☒ (source READY=True ✓ + IaC→CR reconcile LIVE, but the CR is still Helm/seed-co-owned + only bp-alloy) |
| 2 | A console edit reaches the in-cluster CR; the committed file is a full CR not a `0.0.0` stub (A2, A3) | ☒ (write-path targets the CORRECT Flux location ✓ + full CR ✓; CR still carries a manual value — no post-#3749 edit fired yet) |
| 3 | A non-card field (`spec.source.version`) round-trips git ↔ CR ↔ UI (A4) | ☒ (only bp-alloy in the aggregator tree; round-trip not achievable) |
| 4 | A chart upgrade does NOT revert a console edit — Helm no longer owns the CR (A5) | ☒ (Helm still co-owns `f:spec.card`) |
| 5 | The edited icon visibly renders (hero + grid + out-of-band), form pre-fills the IaC icon, picker works (B1–B5) | ☒ (render path now IaC-first ✓ + pre-fill ✓ + picker grid ✓; end-to-end render un-walkable — no IaC icon set + no browser session) |
| 6 | "Saved to IaC ✓" on success; amber/no-green-save when Gitea down; retry reconverges (C1, C2) | ☑ (commit-verdict envelope `{stored,committed,reason}` surfaced + FE amber branch present in the live `97214e9` build) |
| 7 | Per-field inline for cards (widened set) + full-CR `YamlEditor` for the rest, both committing the same Gitea file (D1, D2) | ☑ (inline `cif-*` + Edit-IaC `YamlEditor` both grep-proven in the live deployed bundle) |
| 8 | Identical mechanism on `bp-wordpress` + `bp-postgres`, `git diff` shows zero per-blueprint branches (E1, E2) | ☒ (only bp-alloy reconciled; generality absent) |

**Acceptance: 2 / 8 ✅ (was 0/8). Headline verdict: ❌ FAIL — but only on Helm-CR-co-ownership (A1r3/A5), generality (A4/E), and the un-walkable click-through (no mothership session). The Flux source-auth, the write-path location, the full-CR shape, all editor surfaces, the IaC-first render, and the commit-verdict envelope are DONE.**

---

## Appendix — automated checks (NOT acceptance)

- `go test -race ./products/catalyst/bootstrap/api/internal/handler/...` — incl.:
  - the slow-Gitea injection (`PutFile` sleeps 3s) asserting `committed:true` under `catalogEditGitBudget` (today: deadline at 1500ms → swallowed);
  - the erroring-Gitea injection (`PutFile` errors) asserting the edit response reports `committed:false` + reason (today: 200 OK, store-only);
  - the catalog-edit merge preserving `source`/`manifests`/`placementSchema`/`sso`/`contextSchema` (today: dropped to a `0.0.0` stub);
  - `parseBlueprintCRToCatalog` reading `card.iconLight`/`iconDark`.
- `npm test` (`products/catalyst/bootstrap/ui`) — `CatalogDetail.test.tsx` asserting the hero `<img src>` follows `card.iconLight`; the `IconPicker` test; per-field inline summary save; `YamlEditor` mounting on a blueprint CR + commit.
- `scripts/expected-bootstrap-deps.yaml` lockstep for any new `dependsOn`; the new Flux-source slot present in the bootstrap-kit `kustomization.yaml` (drift guard); kustomize build green; pin-sync-audit green for the chart/blueprint.yaml/slot-pin bump.

> Token-passing / `must_contain` style checks are forbidden as acceptance (PR #1362 shape). Acceptance
> is the founder walking the clickable rows above and seeing the CR move, the icon change, the save
> states, and the full-CR editor commit — on the CURRENT env.
