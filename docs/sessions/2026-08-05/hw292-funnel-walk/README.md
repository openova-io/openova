# hw292 marketplace funnel — live Playwright walk (2026-08-05)

**Env:** hw292 (dep `1c56518035a83e03`, fired 2026-08-03T04:05Z, cc=true), the live
marketplace storefront `https://marketplace.hw292.omani.works`. Walk done with
Playwright against the deployed build — NOT a re-render of source.

This env carries its 2026-08-03 fire train, so it is the correct surface to walk
Pillar-1 (marketplace onboarding) behavior that shipped before that date. Fixes
merged AFTER 2026-08-03 are NOT on this build and are not walkable here (they ride
the next fresh prov).

## What loads

Storefront serves: title "Build Your Organization — OpenOva", the 6-step funnel
nav (1 Plans · 2 Apps · 3 Add-ons · 4 BCP · 5 Review · 6 Checkout), hero
"Build your cloud Organization in under 5 minutes — Every app is FREE, you only
pay for the infrastructure." Pillar-1 storefront is live and reachable.

## Live finding — customer Org "uatco" provisioning FAILED at mysql

Navigating the funnel resumed an existing customer-Org session (owner
`emrah.baysal@openova.io`, tenant `339cba9f-051a-4492-b447-b57301ae9e23`, host
`console.uatco.omani.homes`). The `/launching` page shows:

> **Provisioning didn't finish** — Something went wrong at the **Installing mysql
> (dependency)** stage. Our team has been notified — you can try again or contact
> support.

Progress list (as rendered live):
1. **Creating tenant** ← banned term (see below)
2. Committing manifests to Git
3. Provisioning vCluster
4. (blank step)
5. Deploying WordPress
6. Configuring TLS certificates
7. Running health checks

Screenshot: `hw292-funnel-uatco-provisioning-failed-mysql.png`.

### Evidence value (honest — this is a FAILURE, not a pass)

| UAT line | What this walk shows | Verdict |
|---|---|---|
| rows 20/23 — customer Org through checkout | the funnel DID drive a second Org (uatco) through checkout **into provisioning** — the checkout mechanism is exercised end-to-end | **PARTIAL** — Org created + provisioning attempted, but not active |
| rows 86/90/233/234 — purchased app serves | provisioning **failed at the mysql dependency**; WordPress never came up, so the purchased app does **not** serve | **FAIL (live)** — concrete lead for the next cycle |
| #5646 / row-121 banned term | the timeline literally prints **"Creating tenant"** | **CONFIRMED live** on the pre-#5673 build; deploy-gated (fix merged after this env's fire) |

## Next lead (claimed)

The **"Installing mysql (dependency)" provisioning failure** is the most concrete
open gap toward Pillar-1 100%: a paying customer's app must serve. Root-causing it
needs the hw292 uatco-vCluster kubeconfig (behind the gated catalyst-api-deployments
PVC) or the uatco console `/jobs` job detail — the failed job's logs will say why
the mysql dependency install failed (image pull, PVC bind, CNPG/mysql operator, or
dependency ordering). Tracked against the funnel-purchased-app line (rows
86/90/233/234); "Try again" is offered, so a transient vs. structural check is the
first step.
