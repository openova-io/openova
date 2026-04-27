# Personas and Journeys

**Status:** Authoritative target experience. **Updated:** 2026-04-27.
**Implementation:** The journeys described use Catalyst surfaces (console / Git / API) that are design-stage. See [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md).

How different people use Catalyst. Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology.

---

## 1. Personas

| # | Persona | Where they live | Tools they use |
|---|---|---|---|
| **P1** | **OpenOva Engineer** | github.com/openova-io | Catalyst codebase, Blueprint repos |
| **P2** | **`sovereign-admin`** | Catalyst admin UI + Sovereign Gitea | Browser UI, Git, kubectl (debug) |
| **P3** | **Support Agent** (within a Sovereign Operator team) | Catalyst admin UI in support mode | Browser UI |
| **P4** | **`org-admin`** | Org-scoped Catalyst console | Browser UI, occasional Git |
| **P5** | **SME End User** (e.g. Ahmed, pharmacy owner on Omantel) | Marketplace + the App they installed | Browser only |
| **P6** | **SME Power User** (e.g. Ahmed's tech-savvy nephew) | Console with Developer mode toggled on | Browser, occasionally Git |
| **P7** | **Corporate DevOps / SRE** (e.g. Layla at Bank Dhofar) | Git + console in advanced view | Browser, Git, kubectl-on-own-vcluster, IDE |
| **P8** | **Corporate App Developer** (e.g. Omar at Bank Dhofar) | Console + Git for own service repos | Browser, Git, IDE |
| **P9** | **Security/Compliance Officer** (e.g. Khalid, CISO) | Audit dashboards + EnvironmentPolicy editor | Browser |
| **P10** | **Billing Admin** | Billing console | Browser |

---

## 2. Surfaces

The three first-class surfaces (full list and rationale in [`ARCHITECTURE.md`](ARCHITECTURE.md) §7):

- **UI** — Catalyst console. Form / Advanced / IaC editor depths.
- **Git** — direct push or PR to the Environment Gitea repo (or to private Blueprint repos).
- **API** — REST + GraphQL, for portal integrations.

Plus one debug-only surface:

- **kubectl** — inside one's own vcluster. Read-mostly, never used to mutate Catalyst-managed resources.

**There is no fourth surface.** Terraform, Pulumi, "catalystctl install" are not part of this model.

---

## 3. Personas × Journeys matrix

Cells show which surface(s) the persona uses for that journey. **Bold** = primary. *Italic* = secondary. Empty = not applicable.

|  | P1 | P2 | P3 | P4 | P5 | P6 | P7 | P8 | P9 | P10 |
|---|---|---|---|---|---|---|---|---|---|---|
| **J1** Build & publish Blueprint to public catalog | **Git** + CI | | | | | | | | | |
| **J2** Provision a Sovereign | | **UI**+Git | | | | | | | | |
| **J3** Onboard an Organization | | **UI** | **UI** | | | | | | | |
| **J4** Create an Environment | | **UI** | | **UI** | auto on signup | *UI* | **UI** | | view audit | |
| **J5** Install Application from catalog | | | | **UI** | **UI** form | **UI** | UI + **Git** | UI + **Git** | view audit | view cost |
| **J6** Configure Application | | | | **UI** | **UI** form | **UI** | UI + **Git** | UI + **Git** | view audit | |
| **J7** Author private Blueprint | | *Git*+CI | | | | **UI** + Git | **Git** + CI | **Git** + CI | review + sign | |
| **J8** Author Crossplane Composition (advanced) | **Git** + CI | *Git*+CI | | | | | **Git** + CI | | review | |
| **J9** Promote between Environments | | | | **UI** | | | **UI** + Git PR | **UI** + Git PR | **UI** approve | |
| **J10** Observe runtime / debug | | UI dashboards | UI dashboards | UI dashboards | App's own UI | UI | UI + *kubectl* | UI + *kubectl* | UI audit | |
| **J11** Rotate credentials | | **UI** + auto | | UI + auto | auto | UI | UI + auto | | **UI** + policy | |
| **J12** Audit / compliance review | | UI | UI | UI | | | UI (own changes) | UI (own changes) | **UI** export to SIEM | |
| **J13** Billing & quotas | | UI quotas | UI read | UI invoices | UI plan | | | | | **UI** |
| **J14** Off-board / migrate | | UI export | | UI | UI cancel | | UI export | | audit | UI final invoice |

---

## 4. Two journey narratives, end-to-end

### 4.1 SME journey — Ahmed at Muscat Pharmacy (on Omantel)

**Cast.** Ahmed owns 4 small pharmacies in Muscat. No IT staff. He has a laptop and a credit card.

```
Day 1 — 14:00
─────────────
1. Ahmed visits omantel.openova.io. Sees the marketplace. No login required.
2. Picks "Pharmacy Starter Bundle" — a composite Blueprint
   (bp-bundle-pharmacy: ERPNext + WooCommerce + Stalwart-mail + Postgres + Redis).
3. Clicks "Get Started" → Omantel-branded signup. Phone OTP via Omantel mobile
   bill verification (federated identity). Account created.
4. Catalyst auto-creates: Organization "muscat-pharmacy", Environment
   "muscat-pharmacy-prod", vcluster "muscatpharmacy" on hz-fsn-rtz-prod.
   Workspace-controller spins up the vcluster in ~60 seconds.
5. Bundle install wizard: 3 simple steps —
   Step 1: subdomain (muscatpharmacy.shop.omantel.com)
   Step 2: business details (form generated from Blueprint configSchema)
   Step 3: payment plan (BHD 49/month)
6. Click Install. Provisioning service commits 5 Application directories to
   gitea.omantel.openova.io/muscat-pharmacy/muscat-pharmacy-prod.
   Webhook → projector → Flux reconciles in the muscatpharmacy vcluster.
7. ~3 minutes later: Ahmed sees green checkmarks on his dashboard.
   Each App card has an "Open" button.
   Click ERPNext → SSO via Omantel federated identity → he's in.
─────────────
Day 1 — 14:08 — Ahmed is selling.
```

**What he never saw:** Git, kubectl, vcluster, Flux, Blueprint, YAML, JetStream. **His mental model:** "I have an Omantel account. I bought a bundle. It works."

### 4.2 Corporate journey — Layla at Bank Dhofar (running its own Sovereign)

**Cast.** Layla is an SRE on Bank Dhofar's 12-person Cloud Platform team. They run their own Sovereign on Hetzner. Their internal Organizations are `core-banking`, `digital-channels`, `analytics`, `corporate-it`. Their default tooling is Git + IDE.

```
09:00  Coffee. Opens VS Code. Branch: bp-bd-payment-rail
─────────────────────────────────────────────────────────────────────────
       She's authoring a private Blueprint for a payment-rail microservice
       with Postgres + Redis dependencies.

09:15  Pushes to gitea.bankdhofar.local/digital-channels/shared-blueprints/
       bp-bd-payment-rail. CI in Bank Dhofar's GitHub Actions runner pool
       (running inside the Sovereign) builds the image, signs the Blueprint
       with cosign, publishes to the local OCI registry. blueprint-controller
       picks it up — visible as a private card in the digital-channels Org.

10:00  Switches to her Environment repo:
       gitea.bankdhofar.local/digital-channels/digital-channels-uat
       Edits applications/payment-rail/values.yaml (config tweak).
       Catalyst console (Plan view) shows the diff: what will change,
       dependency impact, drift, cost delta. Like terraform plan, but
       served by the API on the Git diff.

10:15  Happy. Commits to main. Webhook → projector → Flux reconciles in
       30s. Audit log captures her as committer.

11:00  Need to debug the staging deployment.
       Browser: console → digital-channels-staging → payment-rail-staging
       → Logs tab. Then Topology tab to see across regions.
       Or, drops into kubectl scoped to her vcluster:
         $ kubectl --context=hz-fsn-rtz-prod-bankdhofar logs -n payment-rail
       Direct kubectl, scoped strictly to her own vcluster. Bank Dhofar's
       sovereign-admin grants this via a JIT elevation flow.

14:00  Promotion. Opens digital-channels-staging Environment in the console,
       clicks "Copy to digital-channels-uat" on the payment-rail Application.
       Catalyst opens a Gitea PR on the destination Environment's repo.
       EnvironmentPolicy on digital-channels-uat requires team-platform
       approver. Reviewer approves via Gitea web UI (or via the Catalyst
       console's PR view — same backend). Auto-merge. Flux reconciles.

15:00  New Environment needed for a fraud lab. From the console:
       "New Environment in analytics" → fills name "fraud-lab-dev" →
       picks "small" topology (1 region, single bb=rtz). Workspace-controller
       creates the vcluster, bootstraps Flux, creates Gitea repo. Ready in
       60s. Layla now has a new sandbox.

16:00  Business asks for the bank's existing Backstage portal to show
       Catalyst-managed services. Layla integrates: Backstage queries
       Catalyst REST API at https://api.bankdhofar.local/v1/applications,
       authenticated via SPIFFE SVID (workload identity). Backstage's
       service catalog now includes Catalyst Applications alongside other
       systems. No code change in Catalyst — the API was already there.
```

**What Layla DOES use:** UI (for promotion approvals, observability, EnvironmentPolicy editing), Git (for Blueprint authoring and Environment manifests), kubectl (for debugging her own vcluster), and the API (for integrating Backstage). She **never** writes Crossplane code unless she's contributing a new Composition upstream as a Blueprint — and even then it's via a Gitea PR.

**What Layla doesn't use:** Terraform, Pulumi, a "catalystctl" CLI, or any other tool that bypasses Git.

---

## 5. Application card (the user's primary handle)

The card is the user's view of an Application in their Environment. Anatomy below; full UX in the console docs.

```
┌────────────────────────────────────────────────────────────────┐
│  🌐  marketing-site                                       ⋮   │  ← name + menu
│      bp-wordpress @ 1.3.0                                      │  ← Blueprint + version
├────────────────────────────────────────────────────────────────┤
│  ●  Running         🔗 acme.com  ↗                             │  ← status + endpoint
│                                                                 │
│  📍 eu-central                          5 / 5 pods              │  ← placement + health
│  💾 postgres → shared-postgres (own card)                       │  ← key dependency (linked)
│                                                                 │
│  Last deploy: 2h ago by Layla     ⏵ View history                │  ← provenance
│                                                                 │
│  [ Open app ↗ ]    [ Settings ]    [ Logs ]    [ Topology ]    │  ← primary actions
└────────────────────────────────────────────────────────────────┘
```

States via the status badge:

| State | Meaning |
|---|---|
| ● Running (green) | All replicas healthy, traffic flowing |
| ◐ Installing (blue) | Flux reconciling, progress shown inline |
| ◑ Updating (blue) | Config or version change rolling out |
| ◒ Degraded (amber) | Partial — `3/5 pods, 2 unhealthy` |
| ◓ Failed (red) | Install or update failed, "View error" button |
| ○ Paused (grey) | Manually paused, scale-to-zero |
| ◔ Pending approval (purple) | Promotion PR open, awaiting reviewers |

Clicking the card opens the **detail page** with tabs: Overview, Settings, Topology, Secrets, Observability, History, Manifests.

The **Topology** tab is where Placement edits happen — single-region → active-active, region picker, failover policy. The **Manifests** tab is the Monaco IaC editor.

---

## 6. Catalog vs Applications-in-use view

### 6.1 Marketplace (catalog)

```
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│  🌐 WordPress   │  │  💬 Rocket.Chat │  │  🏪 ERPNext     │
│  Self-hosted    │  │  Team chat      │  │  ERP suite      │
│  CMS            │  │                 │  │                 │
│                 │  │                 │  │                 │
│  ⭐ Popular     │  │                 │  │  💼 Business    │
│  [ Install ]    │  │  [ Install ]    │  │  [ Install ]    │
└─────────────────┘  └─────────────────┘  └─────────────────┘
```

Card here = **Blueprint card** (something to install). Visually distinct from Application cards.

### 6.2 Blueprint detail page (cross-Environment view of Applications using a Blueprint)

```
Blueprint: bp-wordpress @ available 1.4.0                         [⋮]
─────────────────────────────────────────────────────────────────────
Self-hosted CMS. Owner: vendor (openova).  Latest: 1.4.0

Applications using this Blueprint in your Org (4)

  Application       Environment        Version    Status
  ─────────────────────────────────────────────────────
  marketing-site    acme-dev           1.4.0      ● Running   [Open]
  marketing-site    acme-staging       1.3.0      ● Running   [Open]
  marketing-site    acme-prod          1.2.0      ● Running   [Open]
  blog              acme-prod          1.2.0      ● Running   [Open]

[ + Install in another Environment ]    [ Compare versions ]
```

This is the "where is this Blueprint running in my Org" view — the simplest cross-Environment surface. No chain object; just a query.

### 6.3 Environment view (where the cards live)

```
Environment: bankdhofar-corp-banking-prod    [+ Install]  [⋮ View modes ▼]

  Group by: ( Status ▼ )    Filter: [_______]    [List | Grid]

  ─── Running (12) ───────────────────────────────────────────
  [ marketing-site card ]  [ blog card ]  [ payment-rail card ]  …

  ─── Updating (1) ───────────────────────────────────────────
  [ analytics-api ◑ updating to 2.3.0 ]

  ─── Degraded (1) ───────────────────────────────────────────
  [ notification-bus ◒ 2/3 pods, restart pending ]

  ─── Backing services (4) ──────────────────────────────────
  [ shared-postgres ]  [ shared-redis ]  [ kafka-cluster ]  [ object-store ]
```

Backing services (Postgres, Redis, etc.) get their own section so users see infrastructure-as-Applications principle (one of the architectural commitments). Clicking a card surfaces dependents (`Used by: marketing-site, blog, analytics-api`).

---

## 7. Differences in default UI mode by Sovereign type

| Setting | SME-style (Omantel) default | Corporate default (Bank Dhofar) |
|---|---|---|
| Console default depth | Form view | Advanced view + IaC editor toggle on |
| Developer mode (Blueprint Studio) | Hidden, off | Visible by role |
| Multi-Environment promotion features | Hidden when only 1 Env | Visible always |
| EnvironmentPolicy editor | Hidden by default | Visible by role |
| `kubectl` access for users | Off | On for `org-developer` and above |
| Git access for users | Off (sovereign-admin can flip per-Org) | On |
| Marketplace features (search, bundles, ratings) | All on | All on but de-emphasized |
| Specter / AIOps Blueprint included by default | Optional | Recommended (Cortex + Specter on top) |

Each Sovereign sets its defaults at provisioning time; users within can override via per-user preferences within the role permissions allowed.

---

*Cross-reference [`GLOSSARY.md`](GLOSSARY.md) and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the underlying model.*
