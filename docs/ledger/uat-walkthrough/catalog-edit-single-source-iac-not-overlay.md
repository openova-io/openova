# Catalog edit = single-source IaC, not a card overlay — UAT walkthrough

## Status — last validated: hw158 (2026-06-17) — ❌ FAIL (single-source-IaC loop NOT wired; demo hand-staged)

> **Tally this walk (28 result rows + 8 acceptance): 3 ✅ · 19 ❌ · 6 N/A** (acceptance summary: 0/8 ✅).
> Re-walked live on hw158 with curl/kubectl/gitea-pod-exec ONLY. This **corrects** the prior walk that
> credited #3713/#3710 inline-edit + the `Edit IaC` YamlEditor as built (those UI surfaces are **NOT in
> the live `catalyst-ui:4b0a9c6` build**) and that recorded the CR summary as empty (it is now
> **populated — but by a manual `gitea_admin` commit, not by the console edit**).

- **Verdict: ❌ FAIL — the console-edit→Gitea→Flux→Blueprint-CR single-source loop is NOT wired end-to-end.** The Gitea-auth break is fixed and a Flux source now genuinely reconciles a full-CR `bp-alloy.yaml` into the in-cluster CR (real progress) — **but the path the console edit writes to and the path Flux reads from are two DIFFERENT Gitea locations**, so a console edit never reaches the CR. The CR only carries an edit because a human hand-committed one to the Flux-watched path. This is the #3668 anti-pattern, relocated — not killed.
  - **A1 ✅ (partial) — the Flux `catalog-sovereign` source is now READY=True and reconciles a full CR.** `gitrepository/openova-catalog-sovereign` READY=True (`stored artifact for revision 'catalog-sovereign@sha1:2533f0ab'`) — the Gitea-auth break IS fixed (secret `openova-catalog-sovereign-git-auth` now present: `username=gitea_admin` + a 40-char token). `kustomization/catalog-sovereign` READY=True, `spec.path=./clusters/hw158.omani.works/catalog-sovereign`, sourced from the **`openova/openova` repo branch `catalog-sovereign`**. The reconciled file `clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml` is a **full CR** (`source.version: 1.0.1`, `manifests`, `placementSchema` — NOT a `0.0.0` stub). managedFields prove `kustomize-controller` (op=Apply) now OWNS `f:spec.f:card.f:summary` — the Flux source genuinely wrote the summary into the CR.
  - **A1 ❌ — but the CR is STILL dual-owned by Helm/seed.** `kubectl get blueprint bp-alloy -o jsonpath='{.metadata.labels}'` still carries `app.kubernetes.io/managed-by: Helm` + `catalyst.openova.io/managed-by: catalog-seed` + `helm.toolkit.fluxcd.io/name: bp-catalyst-platform` **alongside** the new `kustomize.toolkit.fluxcd.io/name: catalog-sovereign`; managedFields show `helm-controller (op=Update)` STILL co-owns `f:spec.f:card`. Helm has not relinquished the CR.
  - **A2 ❌ — a console edit writes the COMMERCE-STORE OVERLAY + a Gitea location Flux NEVER reads; the CR does NOT move.** A real console summary edit DID fire on hw158 (catalyst-api access log `06:23:30 POST /api/v1/sme/commerce/apps → 201` in 1.38s — the only mutating call). That POST's after-write `commitCatalogAppEditToGit` (`sme_commerce.go:172-174` → `writeCatalogEditToGit`) committed to the **`catalog-sovereign` Gitea ORG → `bp-alloy/blueprint.yaml` on branch `main`** (commit by `catalyst-api <ops@openova.io>` "catalog: edit bp-alloy card via catalyst-api (#3648)"), summary = **`UAT-3668-RECONCILE-PROOF-hw158-20260617`**. **But Flux reconciles a COMPLETELY DIFFERENT file** — `openova/openova` repo, branch `catalog-sovereign`, path `clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml` — whose summary is `LIVE-VERIFY #3668 single-source IaC summary via catalog-sovereign Flux`, put there by a **manual `gitea_admin <gitea-admin@catalyst.local>` commit** ("catalog: reconcile bp-alloy into Blueprint CR via Flux (#3668 live verify)"). The live CR summary = the **manual** value (`LIVE-VERIFY…`), NOT the **console-edit** value (`UAT-3668-RECONCILE-PROOF…`). So the console edit's bytes sit in a Gitea Org Flux never watches — **the edit did not reach the CR**. The git write is also best-effort/swallowed by design (`sme_commerce.go:167-168`: "a git failure NEVER changes the API response… it is logged"), so the store-200 is still the success criterion.
  - **No editor surface beyond the #3648 single Edit button.** The live build (`catalyst-ui:4b0a9c6`) has only `data-testid=catalog-detail-edit` (one global Edit button → a 5-field inline form: name/summary/topologies/iconLight/iconDark → `saveCatalogEdit` → `PUT/POST /api/v1/sme/commerce/apps`). There is **NO** `catalog-detail-edit-iac` (Edit IaC YamlEditor), **NO** `cif-summary-edit/input/save` per-field inline edit, **NO** icon-picker grid in this build — contradicting the prior walk that claimed all three built. The edit form surfaces NO "Saved to IaC ✓" / git-outcome — it trusts the store 200.
  - **Icon renders from the BUNDLED asset, never IaC.** `CatalogDetail.tsx:174` resolves the hero logo as `findComponent(name)?.logoUrl` (a `public/component-logos/*` asset), and the edit form pre-fills `iconLight: logoUrl ?? ''` (`:336`). The render never reads `card.iconLight` from IaC, so Part B (edited icon visibly changes) cannot pass.
- **Net:** the Gitea-auth fix + a working Flux→CR reconcile of a full CR is **genuine progress** (A1 spine partially up), but the **console edit is wired to the wrong Gitea location** (product writes the `catalog-sovereign` ORG `bp-alloy/blueprint.yaml`; Flux reads `openova/openova` branch `catalog-sovereign` `clusters/<env>/catalog-sovereign/bp-alloy.yaml`), Helm still co-owns the CR, the editor surfaces (inline/IaC/icon-picker) are **absent** from the live build, and the success criterion is still the store 200, not the git commit. The single-source contract is **NOT achieved** on hw158. Stays open under **#3668**.
- **Maps to:** no direct [`../UAT.md`](../UAT.md) row.
- **Evidence:** all inline below (CLI-only walk — curl/kubectl/gitea-pod-exec, no browser). Key artifacts: the two divergent Gitea files (console-edit `UAT-3668-RECONCILE-PROOF…` in the `catalog-sovereign` ORG vs the manual `LIVE-VERIFY…` in the `openova/openova` `catalog-sovereign` branch) + the live CR summary matching the **manual** value + the `06:23:30 POST /sme/commerce/apps 201` access log.
- **What's needed:** point `writeCatalogEditToGit` at the SAME location the Flux GitRepository watches (the `openova/openova` `catalog-sovereign` branch `clusters/<env>/catalog-sovereign/<bp>.yaml`, NOT a separate `catalog-sovereign` Gitea Org), or repoint the GitRepository at the Org the product writes; make the git commit (not the store 200) the success criterion + surface it; build the IaC/inline/icon-picker editor surfaces; resolve the hero icon from `card.iconLight`; strip Helm ownership of the catalog CR; then re-walk Parts A–E. The manual hand-staging of the Flux-watched file must be replaced by the product write path.
- **Index:** [`README.md`](README.md). Prior-env (hw150) evidence is void; the prior hw158 walk's editor-UI credits are also void (different image).

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

**N/A — CLI-only walk (no browser).** The browser sign-in is out of scope for this curl/kubectl re-walk. The admin-gated catalog API was confirmed reachable + auth-gated (no token → 401):
```
$ curl -sSk -L -w '\n%{http_code}\n' "https://console.hw158.omani.works/api/v1/catalog/bp-alloy"
{"error":"unauthenticated"}
401
```
The live console edit this walk inspects was performed in-browser earlier on hw158 (catalyst-api access log `06:23:30 POST /api/v1/sme/commerce/apps → 201`) — this walk verifies its *effect* on Gitea + the CR via kubectl/gitea-pod-exec, which is the binding acceptance ("the commit + the CR moving IS the acceptance"). The handover-JWT key IS present in-pod (`/var/lib/catalyst/handover-jwt-private.pem`, 1675 bytes) but the pod image is distroless (no python/openssl) so an in-pod mint was not scripted; the CR/Gitea evidence below is independent of a fresh browser session.

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

**Row 3 — ☒ the CR did NOT get the console edit.** The live CR summary is the **manual** value, NOT the console-edit value:
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get blueprint bp-alloy -o jsonpath='{.spec.card.summary}'
LIVE-VERIFY #3668 single-source IaC summary via catalog-sovereign Flux
```
The console edit wrote `UAT-3668-RECONCILE-PROOF-hw158-20260617` to the **`catalog-sovereign` Gitea ORG** (`bp-alloy/blueprint.yaml`, main) — a location the Flux GitRepository (which watches `openova/openova` branch `catalog-sovereign`) **never reads**. Flux instead reconciles the **manually hand-committed** `LIVE-VERIFY…` value. So the CR carries a hand-staged value, NOT the console edit. **☒ FAIL — the console edit never reaches the CR.** (No longer "empty" as the prior walk found — but populated by a human, not by the edit.)

**Row 4 — ☒ description diverged (three sources, not one).**
```
$ kubectl --kubeconfig /tmp/hw158-kc.yaml get blueprint bp-alloy -o jsonpath='{.spec.card.description}'
Grafana Alloy telemetry collector
```
The CR carries the short seed text; the `catalog-sovereign` ORG console-edit file carries the longer `Grafana Alloy — telemetry collector (logs/metrics/traces, OTLP-native)…`. Diverged. **☒ FAIL.**

### A3 — The committed file is a FULL CR, not a `version: 0.0.0` card stub

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea web (`https://gitea.<location>.<fqdn>` or the in-cluster `gitea-http`) → `catalog-sovereign/bp-alloy` | open `blueprint.yaml` history → the latest commit | the commit carries `RECONCILE-PROOF-<ts>` AND a **full CR** — real `spec.version` (e.g. `1.0.1`, not `0.0.0`), `spec.source`, `spec.manifests`, `spec.placementSchema`, `spec.sso` all present (today: a card-only stub with `version: 0.0.0` — FAIL) | ☒ (full CR ✓, but wrong location vs Flux) |

**☒ on the binary intent — full CR ✓, but it's the Gitea ORG Flux never reads.** Read via gitea-pod-exec on the bare repo `/data/git/gitea-repositories/catalog-sovereign/bp-alloy.git`:
```
$ git log --all --format="%h | %an <%ae> | %s | %cr"   # in catalog-sovereign/bp-alloy.git
1abf851 | catalyst-api <ops@openova.io> | catalog: edit bp-alloy card via catalyst-api (#3648) | 38 minutes ago
b174442 | gitea_admin <gitea-admin@catalyst.local> | Initial commit | 38 minutes ago
$ git show main:blueprint.yaml   # excerpt
spec.card.summary: UAT-3668-RECONCILE-PROOF-hw158-20260617
spec.source.version: 1.0.1                                    # NOT 0.0.0
spec.manifests / spec.placementSchema / spec.sso / spec.topology / spec.version: 1.0.1   # all present
```
So `writeCatalogEditToGit`'s read-modify-merge DOES produce a full CR carrying the proof string — the runbook's "0.0.0 stub" FAIL is fixed *for the file the product writes*. **BUT the Flux GitRepository reconciles a DIFFERENT file** — `openova/openova` branch `catalog-sovereign`, `clusters/hw158.omani.works/catalog-sovereign/bp-alloy.yaml`, hand-committed by `gitea_admin` (summary `LIVE-VERIFY…`). The runbook row points at `catalog-sovereign/bp-alloy` (the ORG) and the commit there is correct, but it never reconciles into the CR. Net: full-CR-shape ✓, IaC-spine ☒ — **the file the product writes is NOT the file Flux reads**. **☒ FAIL on the binary intent (the edit reaching the CR via this file).**

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
| `/catalog/bp-alloy` | note the hero logo | the Alloy glyph (today: the **bundled** asset, regardless of IaC) | ☒ (bundled) |
| same → **Edit** → Light-theme icon field | paste a distinct image (1×1 red dot data URI) → **Save** | "Saved to IaC ✓", page refreshes | ☒ |
| same | observe the hero | the hero shows the **red dot** (today: still the Alloy glyph — FAIL, the render reads `findComponent(name).logoUrl`, never `card.iconLight`) | ☒ |

**☒ — the hero icon is resolved from the BUNDLED asset, never IaC.** Source-of-truth on the live build (`catalyst-ui:4b0a9c6`):
```
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx
174:  const logoUrl = findComponent(name)?.logoUrl ?? null          # bundled public/component-logos/* asset
199:  <img src={logoUrl} alt={title} className="hero-logo" ... />   # hero reads logoUrl, NOT card.iconLight
```
The hero `<img src>` is `findComponent(name)?.logoUrl` — the build-time bundled asset keyed by blueprint id, exactly the "today FAIL" the row predicts. There is also no "Saved to IaC ✓" surfacing (the form trusts the store 200). **☒ FAIL.**

### B2 — The same change appears on the grid card

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog` | find the Alloy card in the grid | the card icon is the **red dot** (the grid card resolves from the catalog API `card.iconLight`, not only the commerce-store overlay) | ☒ |

**☒ — same root cause.** The hero never reads `card.iconLight` (B1), so an IaC icon edit cannot propagate to the grid either; the grid renders the bundled/overlay asset. No edited-icon render path exists in this build. **☒ FAIL.**

### B3 — Out-of-band icon edit in Gitea changes the rendered hero

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` | set `spec.card.iconLight` to a different distinct image → commit | — | ☒ |
| `/catalog/bp-alloy` | reload (after reconcile/read) | the hero shows the **git-side** image — render follows IaC, not the console bundle | ☒ |

**☒ — render follows the bundle, not IaC.** Even with `card.iconLight` set in the Flux-watched CR, `CatalogDetail.tsx:174/199` renders `findComponent(name).logoUrl`, so a git-side `iconLight` change is never read by the hero. **☒ FAIL.**

### B4 — The edit form pre-fills the current IaC icon (not the bundled asset)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` (already edited above) → **Edit** | look at the Light-theme icon field | it shows the **current IaC** value (the red dot / git-side URI), not the bundled `/component-logos/alloy.svg` (today: pre-fills the build-time `logoUrl`, dark always blank — FAIL) | ☒ |

**☒ — the form pre-fills the bundled `logoUrl`, not IaC.**
```
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx
336:  iconLight: logoUrl ?? '',     # edit form seeds iconLight from the bundled logoUrl, not card.iconLight
```
The edit form's initial `iconLight` is the build-time `logoUrl`, exactly the "today FAIL". **☒ FAIL.**

### B5 — The visual picker lets you choose a vendored logo

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → next to the icon field | click **Choose** | a thumbnail grid of the 46 vendored `public/component-logos/*` assets opens (today: text field + blind Upload only — no grid) | ☒ |
| same | click `cilium.svg` | the field + a live preview swatch update to `cilium.svg` | ☒ N/A |
| same | **Save** → reload | the hero is the **Cilium** logo | ☒ N/A |

**☒ — no icon picker grid in this build.** The edit form (`CatalogEditForm.tsx`) offers only a text field + file-upload-to-data-URI (`fileToDataURI`/`onPickIcon`) for light/dark icons — no `Choose` button, no thumbnail grid of the vendored assets. `grep -rn "IconPicker\|Choose\|component-logos.*grid"` over the UI src finds no picker component wired into the catalog edit form. **☒ FAIL (text field + blind upload only — the "today" state).**

---

## PART C — The IaC commit is the success criterion (write-budget; was #3676)

### C1 — With Gitea reachable, the commit succeeds under its own budget

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` → **Edit** → Summary `BUDGET-PROOF-<ts>` → **Save** | save | UI shows **"Saved to IaC ✓"** (the git outcome is surfaced, not the store's 200) | ☒ |
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` log | view the latest commit | carries `BUDGET-PROOF-<ts>` | ☒ (commit lands in the wrong location) |

**☒ — the git outcome is NOT surfaced; the store 200 is the success criterion.** The UI save path returns only the store result:
```
products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogEditForm.tsx
98:   const slug = await saveCatalogEdit(blueprintId, edit)   # PUT/POST /api/v1/sme/commerce/apps
99:   onSaved({ ...edit, slug })                              # no git/committed flag in the response
```
`grep "Saved to IaC|committed|Saved (cache only)|IaC commit failed"` over `CatalogEditForm.tsx` + `commerce.api.ts` → **no matches**. The server's `commitCatalogAppEditToGit` is explicitly best-effort + swallowed (`sme_commerce.go:167-168`: "a git failure NEVER changes the API response… it is logged"), so the UI shows green on the store 200 regardless of the git commit. The commit also lands in the `catalog-sovereign` Gitea ORG (not the Flux-watched path). **☒ FAIL — the commit is not the success criterion.**

### C2 — With Gitea DOWN, the UI does NOT report a green save (no silent divergence)

| Go to (URL) / action | Then do | You should see | Result |
|---|---|---|---|
| terminal | `kubectl --kubeconfig /tmp/<env>.kubeconfig scale deploy/gitea -n gitea --replicas=0` | gitea Pod terminates | ☒ N/A |
| `/catalog/bp-alloy` → **Edit** → Summary `OFFLINE-<ts>` → **Save** | save | **amber** "Saved (cache only) — IaC commit failed: …", NOT a green "Saved" (today: green "Saved" while Gitea got nothing — FAIL) | ☒ |
| terminal | `kubectl ... scale deploy/gitea -n gitea --replicas=1`; wait Ready | gitea up | ☒ N/A |
| `/catalog/bp-alloy` | follow the UI's retry instruction (or observe the durable retry) | `OFFLINE-<ts>` is now committed to Gitea — the source + cache reconverge | ☒ N/A |

**☒ — by code inspection the UI would still report a GREEN save with Gitea down (the exact "today FAIL").** Because the git write is best-effort + swallowed server-side (`sme_commerce.go:167-168/214-217` logs + continues) and the UI surfaces only the store result (C1), a Gitea-down edit still returns the store 200 → the UI shows "Saved" with no amber state and no retry. There is no amber/"Saved (cache only)" state in `CatalogEditForm.tsx`. The destructive `scale deploy/gitea --replicas=0` was NOT run on the live env (would break every other catalog/SSO/tenant gitops surface mid-walk); the code path makes the outcome unambiguous. **☒ FAIL — silent green-on-Gitea-down divergence.**

> The slow-Gitea path (a `PutFile` that takes 3s) is exercised by the unit test in the appendix —
> under the old shared 1500ms probe budget it silently no-ops; under the dedicated ~15s
> `catalogEditGitBudget` it commits. (Appendix = automated checks, NOT acceptance.)

---

## PART D — The whole CR is editable, one editor (editor surface; was #3682)

### D1 — Per-field inline edit on the detail page (cards)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | hover the summary line | a pencil/edit affordance appears **on the field** | ☒ |
| same | click the summary → type `INLINE-<ts>` → Save | **only the summary** updates in place — no full-form modal (today: clicking the text does nothing; you must use the global **Edit** button — FAIL) | ☒ |
| same | repeat for **category**, **docs**, and the **topology list** | each edits in place and saves just that field | ☒ N/A |

**☒ — no per-field inline edit in this build.** `grep -rn "cif-summary-edit|cif-summary|InlineField|data-testid=.cif"` over the UI src → **no matches**. The only catalog-edit affordance is the single global `data-testid=catalog-detail-edit` button → the 5-field `CatalogEditForm`. Clicking the summary text does nothing; you must use the global Edit button — exactly the "today FAIL". **☒ FAIL.** (Contradicts the prior walk's "#3713 ✅ present" claim — that surface is not in `catalyst-ui:4b0a9c6`.)

### D2 — The full-CR IaC editor (the reused `YamlEditor`) edits non-card fields

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `/catalog/bp-alloy` | click **Edit IaC** (admin only) | the full `blueprint.yaml` opens in the YAML editor with a **side-by-side diff** + schema validation (today: no such action — FAIL) | ☒ |
| same | change `spec.source.version` → **Commit** | the diff shows the change; commit succeeds | ☒ N/A |
| local Gitea → `catalog-sovereign/bp-alloy/blueprint.yaml` log | view the latest commit | carries the new `spec.source.version` | ☒ N/A |
| `/catalog/bp-alloy` | reload | the version chip reflects the edited version | ☒ N/A |
| terminal | `git grep -n "widgets/cloud-list/YamlEditor" products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx` | the catalog page imports the **reused** `YamlEditor` | ☒ |

**☒ — there is no "Edit IaC" action / full-CR YamlEditor in this build.** `grep -rn "Edit IaC|catalog-detail-edit-iac|YamlEditor"` over `CatalogDetail.tsx` → only `catalog-detail-edit` (the single global Edit button); **no `catalog-detail-edit-iac`, no `YamlEditor` import** on the catalog page. The `git grep "widgets/cloud-list/YamlEditor" … CatalogDetail.tsx` returns nothing. So the entire D2 surface (full-CR editor + diff + validate + commit) is absent — "today: no such action — FAIL". **☒ FAIL.** (Directly contradicts the prior walk's "the `Edit IaC` full-CR YamlEditor IS built" claim — not in `catalyst-ui:4b0a9c6`.)

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
| 1 | A READY Flux source reconciles `catalog-sovereign` → Blueprint CRs (A1) | ☒ (source READY ✓, but CR still Helm/seed-co-owned + only bp-alloy, hand-staged) |
| 2 | A console edit reaches the in-cluster CR; the committed file is a full CR not a `0.0.0` stub (A2, A3) | ☒ (edit writes the wrong Gitea location; CR carries a manual value, not the edit) |
| 3 | A non-card field (`spec.source.version`) round-trips git ↔ CR ↔ UI (A4) | ☒ (no bp-wordpress in the Flux source; round-trip not achievable) |
| 4 | A chart upgrade does NOT revert a console edit — Helm no longer owns the CR (A5) | ☒ (Helm still co-owns `f:spec.card`) |
| 5 | The edited icon visibly renders (hero + grid + out-of-band), form pre-fills the IaC icon, picker works (B1–B5) | ☒ (hero reads bundled `logoUrl`, not `card.iconLight`; no picker) |
| 6 | "Saved to IaC ✓" on success; amber/no-green-save when Gitea down; retry reconverges (C1, C2) | ☒ (git outcome not surfaced; store 200 is the success criterion; best-effort swallowed write) |
| 7 | Per-field inline for cards (widened set) + full-CR `YamlEditor` for the rest, both committing the same Gitea file (D1, D2) | ☒ (no inline edit, no Edit IaC/YamlEditor in `catalyst-ui:4b0a9c6`) |
| 8 | Identical mechanism on `bp-wordpress` + `bp-postgres`, `git diff` shows zero per-blueprint branches (E1, E2) | ☒ (only bp-alloy reconciled; generality absent) |

**Acceptance: 0 / 8 ✅. Headline verdict: ❌ FAIL.**

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
