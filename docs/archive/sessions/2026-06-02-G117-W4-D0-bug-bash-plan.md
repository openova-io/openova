# G117 Wave-4 D0 — bug-bash test plan

> Mandate from EPIC #2737: actively try to break the system. Each row below is a concrete attack vector with reproducible steps, expected behavior, and severity classification if the attack succeeds. **Plan is intentionally adversarial — assume malicious or unlucky user, not happy-path operator.**
>
> Date: 2026-06-02 (drafted by G117 Aux-1)
>
> Target: a fresh prov Sovereign (NOT hw86 fix-forward) brought up after Wave-3 GREEN. The plan runs end-to-end as one Playwright megaspec or as 25 individual test runs; either way every row's `Expected result` must be observed before EPIC #2737 can be marked VERIFIED.
>
> Severity classification (severity if the attack SUCCEEDS — i.e. system misbehaves):
> - **critical** — data loss, cross-Org bleed, auth bypass, irreversible state corruption
> - **high** — user-visible breakage, partial outage, silent failure that doesn't surface to the operator
> - **medium** — degraded UX, missing feedback, recoverable misbehavior

## Pre-flight (run BEFORE row 1)

| # | Step |
|---|---|
| P-0 | Fresh prov via canonical `POST /sovereign/api/v1/deployments` — pick a fresh `t<NN>` per LE rotation (omani.works / omantel.biz). Tag the prov with `g117-bug-bash` in the deployment JSON. |
| P-1 | Wait for `Phase 1 c` to complete — i.e. `bp-self-sovereign-cutover` Job has run and `cutoverComplete=true` reconciled |
| P-2 | Bootstrap 3 test Orgs: `acme`, `beta`, `gamma`. Each Org has 1 Environment (`<org>-prod`). Use the new G117.3 self-serve org-create UI |
| P-3 | For each Org install one Application of each: grafana (Tier-1), guacamole (Tier-2), librechat + matrix (Tier-3) |
| P-4 | Capture baseline `kubectl get applications -A -o yaml > /tmp/baseline.yaml` + `kubectl get hr -A -o yaml > /tmp/baseline-hrs.yaml` for diff after each attack |
| P-5 | Two browser contexts open: one as `sovereign-admin`, one as `org-acme-user`. Both authenticated and pinned to fresh OIDC sessions |

## Attack vectors

| # | Attack vector | Concrete steps | Expected result | Severity if found |
|---|---|---|---|---|
| 1 | Invalid YAML in endpoint form | Open Endpoints tab on `acme/grafana`. Submit hostname = `<script>alert(1)</script>`. Then submit unicode RTL override `‮`. Then submit 270-char hostname `aaaa…<253 a's>…aaaa.acme.t<NN>.omani.works`. Then submit IDN homograph `аpps.acme.t<NN>.omani.works` (Cyrillic а). | UI rejects each with field-level error referencing the specific Kyverno ClusterPolicy violated; no PR opened in `acme/iac`; no Application CR mutated. catalyst-api logs surface the rejection with `code: validation-failed` + the offending input redacted. | high |
| 2 | Concurrent multi-instance create + delete | Two browser tabs in same Org. Tab A clicks "+ New instance" on grafana, submits with name `obs-race`. Tab B simultaneously clicks Delete on an existing `obs-prod` instance. Submit within 100ms of each other. | Optimistic-concurrency 409 on whichever request lost the race. No orphan Application CR. No orphan HR. No orphan ExternalSecret. `kubectl get applications` returns exactly the surviving instances. | high |
| 3 | Network partition mid-PR-merge | Open Endpoints tab. Submit a new endpoint. As soon as the PR opens in `acme/iac`, `kubectl delete pod -n gitea -l app=gitea` to kill Gitea mid-PR. Wait 30s; bring Gitea back. | UI shows pending state in the Endpoints tab with `Last attempt: <timestamp>, retrying`. Once Gitea recovers, the PR resumes its lifecycle: pre-checks re-run, auto-merge fires, Endpoint reaches `Ready`. No half-applied state in cluster (no orphan Certificate, no orphan HTTPRoute). | critical |
| 4 | Malicious topology override | Open `/catalog/grafana/new`. Open browser devtools, intercept the `POST /apps/instances` request, modify body to `{"topology": "active-passive-everywhere-DROP-TABLES"}` (a value not in `Blueprint.spec.topology.supported`). Submit. | API returns 400 with `Error{code: "invalid-topology", message: "topology 'active-passive-everywhere-DROP-TABLES' not in supported set [active-active, active-hot-standby, singleton]"}`. No Application CR mutation. catalyst-api logs the rejection. | medium |
| 5 | Expired catalyst_session mid-launch | In console, set session cookie expiry to 1s in the past via devtools: `document.cookie="catalyst_session=...;expires=Thu, 01 Jan 1970 00:00:00 GMT"`. Click Launch on Grafana. | Launch URL fetch returns 401. UI catches it and either (a) silently refreshes the session via OIDC `prompt=none` then re-tries the Launch, OR (b) falls back to interactive `prompt=login` for re-auth then re-tries. End user sees Grafana within 3s total — they don't get a raw 401 page. | medium |
| 6 | Renamed endpoint stale bookmark | In Endpoints tab, rename `grafana` endpoint to `observability`. Wait for PR merge + Flux reconcile + cert-manager re-issue + DNS update (3min budget). Then in a second browser context, paste the OLD bookmarked URL `https://grafana.acme.t<NN>.omani.works/`. | OLD URL returns HTTP 301 → NEW URL `https://observability.acme.t<NN>.omani.works/`. SSO redirect_uri preserved (KC client config updated by bp-sso-bridge reconciler). User lands at /home of Grafana, authenticated. | high |
| 7 | Two users editing same Application in parallel | Two browser tabs, both authenticated as `org-acme-user`. Both open `/apps/<id>` for the same Application. Tab A edits endpoint `grafana`'s `visibility` from `public` → `internal`. Tab B edits same endpoint's `port` from `443` → `8443`. Both submit within 200ms. | Either: (a) last-write-wins with explicit warning "Another change was applied while you were editing — review and re-submit" surfaced to the loser, OR (b) optimistic-concurrency 409 returned to whichever lost the race, with the loser's diff preserved in the UI for re-apply. No silent merge of both changes. | medium |
| 8 | Load test — 50 apps in parallel | Use a script: for i in $(seq 1 50); curl -X POST -d '{"blueprint":"grafana","org":"acme","name":"obs-load-$i","topology":"singleton"}' .../apps/instances. Run all 50 in parallel via xargs -P50. | All 50 Application CRs created within 60s. All 50 HRs reconcile to Ready within 15min. No 429 rate-limit failures from catalyst-api. No GHCR rate-limit failures (chart pulls). No PowerDNS rate-limit failures (DNS records). No cert-manager rate-limit failures (LE staging issuer used for bug-bash, NOT production). | medium |
| 9 | Cross-Org realm SSO bleed | Authenticate as `org-acme-user` against Org `acme`'s LibreChat. Extract id_token from `/proxy/oauth/userinfo` request. In a new browser context, navigate to `org-beta`'s Matrix and inject the id_token as `Authorization: Bearer <token>`. | Matrix server rejects with 403 `{error: "invalid_token", error_description: "issuer mismatch: token issued for realm acme, target realm beta"}`. No per-Org identity leak. No user-row collision in Matrix DB. Sovereign realm tokens (catalyst-admin scope) also rejected (privilege-escalation prevention). | critical |
| 10 | Blueprint with malformed `spec.topology` | As Sovereign-admin, kubectl apply a Blueprint YAML with `spec.topology: {bcpTopology: "invalid-value", supported: ["nonsense"]}`. | Admission webhook rejects with field-path error: `spec.topology.bcpTopology: must be one of [active-active, active-hot-standby, active-passive, singleton]; spec.topology.supported[0]: same constraint`. Blueprint CR not created. `kubectl get blueprints` does not show it. | high |
| 11 | SSO session fixation | Capture a `KEYCLOAK_SESSION` cookie value as user-A. Force user-B's browser to use that exact cookie value via devtools. Visit `/apps/<id>` and click Launch. | KC rejects the fixated session: either re-issues a fresh session ID on user-B's first request (proper session-fixation defense), or 401s requiring re-auth. Under no circumstances does user-B inherit user-A's session. | critical |
| 12 | SSRF via endpoint hostname | Submit endpoint hostname `169.254.169.254` (AWS IMDS) or `metadata.google.internal`. | Kyverno ClusterPolicy `disallow-link-local-hostnames` (must be added in W2.C5; if missing this attack succeeds → file gap as P0) rejects with 400. catalyst-api also defense-in-depth rejects pre-Kyverno. No DNS A record created. | critical |
| 13 | XSS in Org display name | Create Org with `name: "<img src=x onerror=alert(1)>"`. Browse to `/orgs` then to the org detail page. | UI renders the name as escaped text. No script execution. No HTML injection into the breadcrumb, page title, or any list view. Verifier opens browser console — zero errors AND zero alerts fired. | critical |
| 14 | Path traversal via app name | Submit Application name `../../etc/passwd` to `POST /apps/instances`. | Regex validation `^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$` (per OpenAPI `CreateInstanceRequest.name`) rejects with 400. No filesystem touch on catalyst-api pod. No Application CR created. | critical |
| 15 | Org delete with in-flight Apps | While 3 Applications are still in `Reconciling` state in Org `gamma`, click Delete on the Org via the admin UI. | Confirmation dialog warns: "3 Applications still reconciling; deleting the Org will abort them and clean up. Continue?". On Continue: organization-controller finalizer first uninstalls all HRs (cascading down to per-cluster), then per-Org Gitea repo deletion, then OpenBao path purge, then Org CR tombstone. No orphan resources. | high |
| 16 | Endpoint create with reserved hostname | Submit endpoint with hostname matching a Sovereign-CP service: `auth.t<NN>.omani.works` (Keycloak) or `gitea.t<NN>.omani.works`. | dns-conflict-precheck FAILs with `code: hostname-reserved`. cert-manager-precheck also FAILs. PR auto-closes. UI shows clear error to user. | high |
| 17 | Recursive Helm value injection | In the New Instance dialog, set a values override `values.image.repository: "{{.Values.image.repository}}"` (a Go-template that, if naively rendered, would loop). | catalyst-api refuses the values; either via schema validation (preferred — only documented keys in `Blueprint.spec.configSchema` allowed) or via Helm itself failing the template render. No Application reaches Reconciling. | high |
| 18 | Launch URL replay (one-shot violation) | Click Launch on Grafana, get the URL with one-shot token. Open URL in tab 1 (success). Within 60s, open the SAME URL in tab 2 (incognito). | Tab 2's request is rejected: token is one-shot per OpenAPI contract (`expiresAt: 60s` + single-use). User redirected to interactive login. | high |
| 19 | Service-Account token theft | Compromise `org-acme-user`'s console session via XSS (assume the attack from #13 succeeded for testing). Extract bearer token. Use the token against `POST /sovereign/api/v1/deployments`. | Sovereign-admin-scoped endpoints reject with 403. Per-Org Org-admin endpoints work (expected — that's the user's actual scope). The token does NOT escalate to Sovereign-wide privileges. | critical |
| 20 | Slow-loris on PR pipeline | Open 100 endpoint-create PRs simultaneously via curl. None of them have valid YAML. | Each is rejected at PR-open time by `validateRequest()`. No PR ever reaches Gitea. catalyst-api's request rate-limit (1 PR per Org per second baseline) kicks in. No service degradation observable to other Orgs. | medium |
| 21 | Region-kill during multi-instance create | While creating a 3-instance grafana with `active-hot-standby` topology, kill region B (`hcloud server stop` on all region-B nodes). | application-controller detects region-B unreachable. Active HR in region A reaches Ready. Passive HRs in region B stay Pending with explicit `status.conditions[type=ClusterReachable, status=False, reason=RegionDown]`. UI shows degraded badge but the App is usable. Once region B restored: passive HRs reconcile to Ready automatically. | high |
| 22 | Catalyst-api OOM during create | Set `catalyst-api` resource limit to 128Mi via `kubectl set resources`. Trigger 20 parallel app creates. | catalyst-api either gracefully 429s the queue OR restarts cleanly (no half-created Application CRs left behind). On restart, in-flight requests get retried by the client. No data corruption. | high |
| 23 | Stale Application reference | Delete an Application via API. Immediately (within 500ms) GET `/apps/<id>`. | API returns 404 `Error{code: "not-found"}`. Console UI handles 404 gracefully (redirect to `/apps` list with banner "This Application was deleted"). | medium |
| 24 | DNS-conflict-precheck timeout | Block PowerDNS API egress with a NetworkPolicy for 30s. Submit endpoint create. | Pre-check returns `precheck-unavailable` status to Gitea, NOT a silent PASS. PR is held without auto-merge. Once PowerDNS API restored: pre-check re-runs and either PASS/FAIL on its own merits. | critical |
| 25 | Application Spec mutation via PATCH | Use `kubectl patch application acme/obs-prod --type=merge -p '{"spec":{"blueprint":"matrix"}}'`. | Admission webhook rejects with `spec.blueprint is immutable`. Application stays as grafana. | high |
| 26 | Endpoints tab race with reconciler | Click Delete on an endpoint. Within 100ms, click Create on a new endpoint with the same hostname the deleted one had. | Either: (a) delete completes first and create succeeds, OR (b) create is held until delete's reconcile completes, OR (c) create returns 409 with `hostname-in-deletion` and a hint to retry in 30s. No half-state where both old + new endpoints exist briefly. | high |
| 27 | Wildcard certificate exhaustion | Create 5 Applications each with 2 endpoints under the same parent domain in rapid succession (10 hostnames total). | cert-manager either issues a wildcard cert for the parent OR 10 individual certs — both acceptable; what's NOT acceptable is hitting LE rate-limit (5 per week per registered domain). Test uses LE staging issuer to verify the path without consuming production quota. | medium |
| 28 | Gitea action runner exhaustion | Trigger 20 endpoint PRs simultaneously. Gitea has 2 action runners. | PRs queue. Each runs through pre-checks in <30s. No deadlock. No PR is silently stuck >60s with no log line. | medium |
| 29 | Multi-instance namespace collision | Create Application `obs` in Org `acme`. Manually `kubectl create ns obs-prod-1` via Sovereign-admin (squat the slot). Then try to create a 2nd Application named `obs-prod` in Org `acme` (which would want namespace `obs-prod-1`). | application-controller detects namespace exists + lacks the `catalyst.openova.io/instance=...` label. Either: (a) rejects with `namespace-collision`, OR (b) bumps the slot to `obs-prod-2` and proceeds. No silent overwrite of the squatted namespace's contents. | high |
| 30 | TLS downgrade via endpoint mutation | Edit a `tls: true` endpoint to `tls: false` while keeping `visibility: public`. | `kyverno-admission` precheck FAILs with `require-tls-on-public-endpoints`. PR auto-closes. No HTTPRoute change applied. Existing HTTPS endpoint continues serving. | critical |

## Per-row evidence to capture

For EVERY row above, the W4.D0 executor records:

1. Browser screenshot of the UI state at the moment of attack (or `curl -v` output if API-only attack)
2. Server-side log lines from catalyst-api / Gitea / Kyverno / KC (whatever surfaces the rejection)
3. Post-attack `kubectl get applications -A -o yaml > /tmp/post-row-<N>.yaml` and `diff /tmp/baseline.yaml /tmp/post-row-<N>.yaml` (must be empty for rows that should leave no trace; minimal diff for rows that legitimately mutate state)
4. Per-row decision: `PASS` (system rejected/handled correctly per Expected result) or `FAIL` (attack succeeded)

## Reporting

After all 30 rows: produce `docs/sessions/2026-06-0X-G117-W4-D0-bug-bash-report.md` with:

- Top-level summary: `30/30 PASS` or `<N>/30 PASS — <M> FAIL`
- Per-row evidence link
- For every FAIL: severity classification + recommended fix-author dispatch
- Cross-reference to TRUST.md surface rows (rows touched by W4.D0 flip back to UNVERIFIED until re-walked after fixes ship)

## Definition of Done for W4.D0

- [ ] All 30 rows executed end-to-end on a single fresh prov (NOT hw86)
- [ ] Per-row evidence captured (screenshots + logs + kubectl diffs)
- [ ] Report posted with PASS/FAIL summary
- [ ] Every FAIL has a follow-up issue filed (NOT closed in this PR; tracked separately)
- [ ] EPIC #2737 comment posted with the report URL + summary table
- [ ] If any FAIL classified `critical`: STOP the EPIC merge. Dispatch fix-authors. Re-run the affected rows after fixes land.
- [ ] No row glossed over as "n/a" or "not testable" without a written justification surfaced to the operator

## Anti-theater discipline (binding for every executor)

- Don't reduce a test to "ran the happy path and it worked" — the WHOLE POINT is the unhappy path
- Don't catch + suppress an error in the test harness to make CI pass — surface every divergence
- Don't fake the cookie / token / network-partition steps via mocks alone; at least one row per attack-class must use the live wire (per `feedback_validate_agent_outcomes_no_theater.md`)
- Don't run the bash on fix-forward hw86 — the WHOLE POINT of W4.D0 is fresh-prov verification (per `feedback_zero_touch_fresh_prov_mandate.md`)
- Don't merge the EPIC with critical-severity FAILs unfixed
