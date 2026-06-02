# G117 W4.D3 — Endpoint rename walk (grafana → observability; old URL 301s, new 200s, SSO still silent)

> **Goal**: Prove the Endpoints tab (G117.3) supports an end-to-end rename: PR pipeline runs the three pre-checks, auto-merges on green, cert-manager re-issues, PowerDNS rotates, KC client redirect_uri updates, and the OLD hostname 301-redirects to NEW within 2 minutes — silent-SSO budget maintained throughout.
>
> **Pillar**: 1 (marketplace + onboarding — operator self-serve)
>
> **Cited memory**: `feedback_g113_sso_idr_defaultprovider_fix.md`, `feedback_validate_walk_findings_before_dispatch.md`

## Pre-flight env state

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov OR existing prov with D1+D2 already PASSED | `docs/ledger/TRUST.md` |
| P-2 | At least 1 Org (`acme`) with grafana Application + endpoint named `grafana` serving on `grafana.acme.t<NN>.omani.works` | `curl -sf https://grafana.acme.t<NN>.omani.works/api/health` returns 200 |
| P-3 | Per-Org IaC repo (`acme/iac`) exists in Sovereign-local Gitea with branch protection enforcing 3 named pre-checks (W2.C3 + W2.C5 GREEN) | `curl -sf https://gitea.t<NN>.omani.works/api/v1/repos/acme/iac/branch_protections \| jq '.[].required_status_checks'` includes kyverno-admission + cert-manager-precheck + dns-conflict-precheck |
| P-4 | Tier-1 SSO walk passing (D1) | TRUST.md |

## Walk procedure

### Step 1 — Pre-rename baseline

| # | Action | Expected | Probe |
|---|---|---|---|
| 1.1 | Bookmark the OLD URL in a second browser tab | n/a | `firefox-bookmarks` or simply paste later |
| 1.2 | Capture current endpoint metadata via API | `GET /apps/<id>/endpoints` returns endpoint `grafana` with `hostname: grafana.acme.t<NN>.omani.works`, `tls: true`, `visibility: public`, `ssoEnabled: true`, `status: Ready`, `certificateStatus: Issued` | `curl -sf -H "Authorization: Bearer $TOKEN" .../apps/<id>/endpoints \| jq` |
| 1.3 | Confirm KC client redirect_uri | Includes `https://grafana.acme.t<NN>.omani.works/login/generic_oauth` | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/acme/clients?clientId=grafana \| jq '.[0].redirectUris'` |
| 1.4 | Confirm cert-manager Certificate | `kubectl get certificate -n acme grafana-tls -o yaml \| yq '.status.conditions[] \| select(.type=="Ready").status'` returns `True` |
| 1.5 | Curl OLD URL — note returned content fingerprint (e.g. Grafana title HTML) | HTTP 200 + Grafana HTML containing `<title>Grafana</title>` | `curl -sf https://grafana.acme.t<NN>.omani.works/ \| grep -o '<title>[^<]*</title>'` |

### Step 2 — Initiate rename via Endpoints tab

| # | Action | Expected | Probe |
|---|---|---|---|
| 2.1 | Navigate to `/apps/<id>/endpoints` in console | Endpoints tab lists `grafana` row with edit + delete affordances | Screenshot |
| 2.2 | Click Edit on `grafana` row | Inline form (or modal) opens with current values; `name` field editable (W2.C5 ensures rename-trace via `lastRenameFrom` field) | Screenshot |
| 2.3 | Change `name` from `grafana` → `observability` (UI also derives hostname accordingly per template `<name>.<org>.<sov>`) | Hostname auto-updates to `observability.acme.t<NN>.omani.works` | Screenshot |
| 2.4 | Submit | UI shows PR-status badge `pending`; toast: "PR #N opened in `acme/iac`; auto-merge gated on 3 pre-checks" | Screenshot + Network HAR |
| 2.5 | Click the PR URL in the toast | Opens Gitea PR in new tab; PR title `feat(endpoints): rename grafana → observability for acme-grafana app`; diff shows the manifest changes | Screenshot |
| 2.6 | Inspect PR pre-check status | 3 checks: kyverno-admission, cert-manager-precheck, dns-conflict-precheck — all `pending` initially | Gitea PR status panel screenshot |

### Step 3 — Pre-checks fire + auto-merge

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | Wait <30s for pre-checks to complete | All 3 pass: kyverno (no policy violation), cert-manager (new hostname not in any existing cert), dns-conflict (no existing record under new hostname) | Gitea PR status panel screenshot showing 3 green checks |
| 3.2 | Auto-merge fires | PR merges to `main` of `acme/iac` | Gitea PR shows `Merged` |
| 3.3 | Flux reconciles the new manifest (<60s) | New HTTPRoute / Ingress for `observability.acme.t<NN>.omani.works`; old still serves during overlap window | `kubectl -n acme get httproute observability` exists |
| 3.4 | cert-manager issues new Certificate via LE (or staging in test) | `kubectl -n acme get certificate observability-tls -o yaml \| yq .status` shows Ready=True | `curl -sf` against the new URL with `-k` flag verifies cert subject = new hostname |
| 3.5 | PowerDNS adds A record for new hostname | `dig +short observability.acme.t<NN>.omani.works` returns IP | dig output |
| 3.6 | application-controller updates KC client redirect_uri | `kubectl get application <id> -o yaml \| yq '.status.endpoints[]'` shows both old (`status=Renaming`) and new (`status=Ready`) | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/acme/clients?clientId=grafana \| jq '.[0].redirectUris'` now includes BOTH old and new redirect URIs |
| 3.7 | 301 redirect installed from OLD to NEW | bp-gateway-api HTTPRoute with 301 rule from `grafana.acme.t<NN>.omani.works` to `observability.acme.t<NN>.omani.works` | `curl -sIL https://grafana.acme.t<NN>.omani.works/ -w "%{http_code}\n"` shows 301 then 200 |
| 3.8 | End-to-end wall-clock from Step 2.4 to Step 3.7 | <2 minutes | timestamp diff |

### Step 4 — Verify rename invariants

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | Paste OLD URL from bookmark | HTTP 301 → 200 at NEW URL; Grafana home page renders | Browser; HAR shows 1×301 + 1×200 |
| 4.2 | Sign in via SSO from NEW URL bookmark | Silent SSO works (G113 chain); no interactive prompt | Per D1 probes against NEW URL |
| 4.3 | Sign in via SSO from OLD URL bookmark | 301 → 4-hop SSO chain → 200 at NEW URL with user authenticated | Per D1 probes; 301 hop counted |
| 4.4 | Verify KC client redirect_uri | Has both old + new for the overlap window (TTL configurable; default 24h before old is pruned) | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/acme/clients?clientId=grafana \| jq '.[0].redirectUris'` |
| 4.5 | Wait 24h (or skip-ahead by setting controller env `RENAME_REDIRECT_TTL=10s` for test) | Old hostname removed from KC redirect_uri; OLD HTTPRoute deleted; OLD cert revoked + deleted | Same probes return only new values |
| 4.6 | After cleanup: OLD URL | HTTP 404 (cleanest) or NXDOMAIN | `curl -v` |

### Step 5 — Rename failure-path test (defensive)

| # | Action | Expected | Probe |
|---|---|---|---|
| 5.1 | In a second Org `beta`, attempt to rename a different endpoint to `observability.acme.t<NN>.omani.works` (cross-Org hostname squat) | Pre-checks FAIL — cert-manager-precheck returns `hostname-already-certified`; PR auto-closes | Per EC-3 of bug-bash plan |
| 5.2 | UI surfaces the failure clearly | Endpoint stays at old name; error toast: "Pre-check failed: hostname already certified in Org acme" + link to the closed PR | Screenshot |

## Self-verification curl probes

```bash
# Probe 1 — Capture pre-rename baseline
OLD=https://grafana.acme.t<NN>.omani.works
NEW=https://observability.acme.t<NN>.omani.works
curl -sIL $OLD -w "\nHTTP %{http_code}\n"
# Expect: HTTP 200

# Probe 2 — Trigger rename via API (UI does the same)
APP_ID=<from kubectl get application>
curl -X PATCH -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"observability"}' \
  https://api.t<NN>.omani.works/catalyst/v1/apps/$APP_ID/endpoints/grafana \
  -w "\nHTTP %{http_code}\n"
# Expect: HTTP 202 with body {prURL: "...", status: "open"}

# Probe 3 — Poll PR pre-check completion (<30s)
PR_URL=<from probe 2 response>
PR_SHA=$(curl -sf $PR_URL/api/v1/... | jq -r .head.sha)
while true; do
  STATUS=$(curl -sf https://gitea.t<NN>.omani.works/api/v1/repos/acme/iac/commits/$PR_SHA/statuses | \
    jq -r 'map(.state) | join(",")')
  echo "[$(date +%H:%M:%S)] $STATUS"
  [[ "$STATUS" == "success,success,success" ]] && break
  sleep 2
done

# Probe 4 — Wait for new hostname to resolve + serve
while true; do
  CODE=$(curl -sf -o /dev/null -w "%{http_code}" $NEW/api/health 2>/dev/null || echo "fail")
  echo "[$(date +%H:%M:%S)] new=$CODE"
  [ "$CODE" = "200" ] && break
  sleep 5
done

# Probe 5 — Verify 301 from old to new
curl -sIL $OLD -w "%{redirect_url}\n" -o /dev/null
# Expect: $NEW

# Probe 6 — SSO still silent via 301 path
curl -sIL -b /tmp/cookies "$OLD/?prompt=none&kc_idp_hint=catalyst-pin" 2>&1 | grep -E "HTTP/|location:" | head -10
# Expect: 301 to NEW, then 4-hop SSO chain, then 200

# Probe 7 — KC client redirect_uri includes BOTH during overlap
curl -sf -H "Authorization: Bearer $KC_ADMIN" \
  https://auth.t<NN>.omani.works/admin/realms/acme/clients?clientId=grafana | \
  jq '.[0].redirectUris'
# Expect: array containing both old and new URIs

# Probe 8 — Cross-Org hostname squat (defensive)
curl -X POST -H "Authorization: Bearer $BETA_TOKEN" \
  -d '{"name":"observability","hostname":"observability.acme.t<NN>.omani.works"}' \
  https://api.t<NN>.omani.works/catalyst/v1/apps/<beta-app-id>/endpoints \
  -w "\nHTTP %{http_code}\n"
# Expect: 202 (PR opens) but PR fails pre-checks within 30s and auto-closes
```

## Evidence on TRUST.md format

```markdown
| 2026-06-0X | t<NN>.omani.works | Endpoint rename (grafana→observability) end-to-end <2min | VERIFIED-PASS | g117-w4-d3 | PR auto-merged in 24s; cert reissued 18s; KC redirect_uri updated 12s; 301 honored; SSO still silent | screenshots/g117-w4-d3-{pre,pr-open,3-checks-green,post-rename,301-hop,sso-via-old}.png | <walker> |
```

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Pre-rename | GET | OLD URL | 200 |
| Trigger rename | PATCH | `/api/apps/<id>/endpoints/grafana` | 202 |
| PR pre-checks | GET | `/api/v1/repos/acme/iac/commits/<sha>/statuses` | 200 with 3× state=success |
| New URL serves | GET | NEW URL | 200 |
| Old URL post-rename | GET | OLD URL | 301 → 200 at NEW |
| SSO via 301 path | GET | `OLD/?prompt=none&kc_idp_hint=...` | 301 → 302×3 → 200 |
| Cross-Org squat | POST | Org-B PR for Org-A hostname | 202 then PR closed |

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d3-pre-rename-endpoints-tab.png` | Step 2.1 |
| 2 | `<date>-g117-w4-d3-pr-opened-toast.png` | Step 2.4 |
| 3 | `<date>-g117-w4-d3-gitea-pr-3-checks-pending.png` | Step 2.6 |
| 4 | `<date>-g117-w4-d3-gitea-pr-3-checks-green.png` | Step 3.1 |
| 5 | `<date>-g117-w4-d3-pr-merged.png` | Step 3.2 |
| 6 | `<date>-g117-w4-d3-new-url-200.png` | Step 3.3 (browser at NEW) |
| 7 | `<date>-g117-w4-d3-old-url-301-redirect-trace.png` | Step 4.1 (HAR + browser) |
| 8 | `<date>-g117-w4-d3-sso-via-old-url-silent.png` | Step 4.3 |
| 9 | `<date>-g117-w4-d3-cross-org-squat-rejected.png` | Step 5.1 (Gitea PR closed) |

## Failure-mode triage

- **PR opens but pre-checks never run**: Gitea Actions runners not configured per ADR-0009; check W2.C3 + W2.C5
- **Pre-check times out at 60s**: PowerDNS API slow; W2.C5 EC-11 fail-closed must surface; fix-author dispatch
- **KC client redirect_uri NOT updated**: bp-sso-bridge reconciler not honoring rename trace; check W2.C4 controller code
- **301 NOT installed**: bp-gateway-api HTTPRoute rename logic missing; check application-controller
- **301 installed but SSO breaks**: KC client redirect_uri filter doesn't accept the OLD URL; verify the overlap window includes BOTH URIs
- **Old URL never cleaned up after TTL**: pruning Job not scheduled; check organization-controller's CronJob
