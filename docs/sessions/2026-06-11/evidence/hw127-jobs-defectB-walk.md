# Defect-B / #3277 — mothership stale-running-jobs walk (2026-06-11)

Walk of the LIVE mothership console (`console.openova.io`) to determine whether
defect-B (#3277 — mothership showing stale `running` bp-* jobs after a Sovereign
hands over) is reproducible/observable now, post the #3279 fix.

## Context

- #3279 landed on the mothership `catalyst-api` at ~2026-06-11T07:43:46Z
  (pod `catalyst-api-77fcfbf597-pkb4n`, image
  `ghcr.io/openova-io/openova/catalyst-api:2365ebc`). Verified live via
  `kubectl get pod -n catalyst -o jsonpath`.
- #3279's sweep fires **only at a Sovereign's handover** and is **forward-only**
  (does NOT retro-fix past deployments).

## What was walked (click-by-click)

1. Logged into `console.openova.io` as `emrah.baysal@openova.io` via the email +
   6-digit PIN flow. PIN read over IMAP from the real Stalwart mailbox
   (`mail.openova.io:993`). Login succeeded — header shows the `E` avatar and a
   banner: **"You have a deployment in flight: hw127.omani.works"**.
   → `hw127-loggedin-inflight-banner.png`
2. The `/sovereign/provision/<id>/dashboard` route is broken with a client-side
   null-deref (`Cannot read properties of null (reading 'length')`). Captured as
   a separate UI defect (not the jobs view).
   → `hw127-dashboard-route-error.png`
3. Apps view (`/sovereign/provision/5a4c1469ea0139a8`) — header status reads
   **"Provisioning"**; bp-* app cards show INSTALLING / INSTALLED / PENDING /
   DEGRADED — all consistent with an in-flight (pre-handover) deployment.
   → `hw127-apps-provisioning.png`
4. Jobs view (`/sovereign/provision/5a4c1469ea0139a8/jobs`) — the actual
   jobs-rendering route. Live state stream "Refreshing from the catalyst-api
   every 5s". Status tally from the full accessibility snapshot:
   **334 succeeded, 46 failed, 19 pending, 13 running.**
   → `hw127-jobs-view.png`, `hw127-jobs-running-filter.png` (status=running)
5. Status=pending filter shows the **Handover job rows are all "Pending"**
   (`Handover`, `Handover (me-east-215-a)`, `Handover (me-east-215-b-1)`).
   Handover has not started.
   → `hw127-jobs-handover-pending.png`

## Backend cross-check

- Only ONE deployment record exists on the mothership PVC
  (`catalyst-api-deployments`): `5a4c1469ea0139a8.json` = **hw127**
  (subdomain `hw127`, pool `omani.works`, org `Omantel`, owner
  `emrah.baysal@openova.io`).
- Its status is `phase1-watching`; `finishedAt` is the zero value
  (`0001-01-01T00:00:00Z`); no handover timestamp. Started 2026-06-11T07:49:19Z
  — AFTER the #3279 image came up (07:43:46Z), but it has not reached handover.

## Verdict

**#3277 is GATED on hw127's eventual handover.**

- There is no deployment on the mothership that handed over after ~07:45Z. The
  only deployment (hw127) is still `phase1-watching` with its Handover jobs
  Pending.
- The 13 `running` job rows on hw127 are **correct, not stale** — the deployment
  is mid-provisioning, so jobs legitimately in flight show `running`. Defect-B
  is specifically about `running` rows that persist *after* handover; that state
  cannot exist here yet because handover has not run.
- #3279's at-handover sweep therefore has had no opportunity to fire. The honest,
  evidenced reason #3277 cannot be walked to PASS/FAIL right now is that no
  post-#3279 handover has occurred. It will be walkable once hw127 (or a later
  fresh prov) completes handover.

Refs #3277 #3263 #3279
