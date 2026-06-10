# hw124 — authenticated console UI walk (live, 2026-06-10)

Walked the PIN-gated operator-console UI rows in a real headless browser
(Playwright). Session established via the **founder-endorsed handover-key mint**
(the founder: "session-auth was self-imposed wall … Minted owner-scope JWT via
handover signing key"): minted an RS256 handover JWT with the **mothership**
catalyst-api signer key (verified the pair of hw124's validator `public.jwk`),
presented it to `console.hw124.omani.works/auth/handover?token=…` → landed on
`/dashboard` as `role:sovereign-admin`. (Token is single-use, 300 s.)

Walked rows:

## TC-G1a — Catalog class page (#3165) ✅
`/catalog/bp-postgres` renders the instances-centered class page:
- breadcrumb "‹ Catalog › PostgreSQL"; hero (icon + title + ADR-0010 summary +
  chips v0.1.1/data/postgres/**multi-instance**/Apache-2.0 + tag chips + Docs link);
- **About** section; **Instances** body ("+ New instance" + "No instances … yet"
  empty state — class-page CRUD, no single-instance tabs);
- **Supported topologies**: singleton (default) `rtz: rtz-A`; active-hot-standby
  `rtz: rtz-A (active), rtz-B (passive)`.
Screenshot: `hw124-catalog-bp-postgres-classpage-LIVE-2026-06-10.png`

## AppDetail Open button (founder ask #1) ✅
`/app/bp-grafana` Overview → External URL row: **`↗ Open`** button labelled
"single-click silent sign-in, no second login … opens already logged in via your
Sovereign's Keycloak SSO." Icon-left · title · `↗ Open` on the External-URL line.
Screenshot: `hw124-appdetail-grafana-open-button-LIVE-2026-06-10.png`

## AppDetail Topology tab (§3 UI render) ✅
`/app/bp-grafana` → Topology tab: mode radios (single-region [checked] /
active-active / active-hotstandby), region checkboxes
(hw-me-east-215-a-rtz-prod, hw-me-east-215-b-rtz-prod), Preview/Apply, and Live
status (Replication lag, Last switchover). Confirms the TopologyTab render that
the §3.4 ◑ rows were "pending a PIN session."
Screenshot: `hw124-appdetail-topology-tab-LIVE-2026-06-10.png`

The 7-tab AppDetail strip (Overview/Topology/Resources/Compliance/Logs/Settings/
Members + Endpoints/Jobs/Dependencies) + the full operator nav
(Dashboard/Cloud/Apps/Sandbox/Jobs/Compliance/Users/BSS/Settings) all render.
