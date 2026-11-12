# Sovereign acceptance suite (ADR-0015 D9)

> **Function is the acceptance criterion.** This suite is the definition of
> "ready end to end" for a Sovereign. It drives the live Sovereign API the
> way the console does, and checks that things work: the app serves, takes
> writes, and moves between regions. That a resource exists is not enough.
> Refs #7067, [ADR-0015](../../../docs/adr/) D9. Pillars 1 and 3.

## Run it

```bash
# Read-only. Works out the Org, Environment and Regions, and checks every endpoint
# the run uses. The API client refuses any non-GET call in this mode.
ACCEPT_MINT=1 scripts/acceptance-sovereign.sh hw307 --discover

# The real run. Installs, writes, moves, moves back, deletes.
# --record writes docs/ledger/acceptance/hw307-<run_id>.json
ACCEPT_MINT=1 scripts/acceptance-sovereign.sh hw307 --record
```

| Variable | Default | Meaning |
|---|---|---|
| `CATALYST_SESSION` | — | Value of an owner `catalyst_session` cookie. Never printed. |
| `ACCEPT_MINT=1` | off | Use this when `CATALYST_SESSION` is unset. The script mints a session with the same claims as PIN login. It reads the signer from region A's `catalyst-api` with `kubectl exec` and pipes it straight into the minter, so the key never touches disk. |
| `ACCEPT_KUBECONFIG_A` / `_B` | `~/.kube/sovereigns/<env>-a.yaml` / `-b.yaml` | A starts as Primary, B as Standby. |
| `ACCEPT_CONSOLE` | `https://console.<env>.omani.works` | |
| `ACCEPT_REGION_IPS` | each region's bootstrap-kit `CILIUM_LB_IPAM_CIDRS` (TEMPORARY) | `regionA=ip,ip;regionB=ip`. Used by the DNS assertion. |
| `ACCEPT_ORG` / `ACCEPT_ENVIRONMENT` | discovered | Discovery picks the Sovereign's own (`kind=internal`) Org and that Org's only multi-region Environment. |
| `ACCEPT_JOURNEYS` | `J1,J2,J3,J4,J5` | |
| `ACCEPT_INSTALL_TIMEOUT` / `_MOVE_` / `_DELETE_` | `25m` / `20m` / `10m` | |
| `ACCEPT_COMMIT_BUDGET` | `5s` | Maximum wall time for one WordPress write. |
| `ACCEPT_KEEP_ON_FAIL=1` | off | Skips J5 cleanup after a failure, so the app stays up for debugging. |
| `ACCEPT_INSECURE_APP_TLS=1`, `ACCEPT_DNS_SERVER`, `ACCEPT_WP_TABLE_PREFIX` | off, system resolver, `wp_` | |

Exit codes: `0` PASS, `1` an assertion failed, `2` usage error, `3` harness error.

In CI, use `.github/workflows/acceptance-sovereign.yaml` (`workflow_dispatch` or
`workflow_call`). It needs the `ACCEPTANCE_KUBECONFIG_A/B` secrets, and a
runner that can reach the Sovereign apiservers.

## What each step asserts

Every step fails fast with a precise message. A step that times out reports the
last state it observed, so the error says what the system was doing.

| Step | Assertion | How it is measured |
|---|---|---|
| D0.1 | The session is an owner session | `GET /api/v1/whoami` |
| D0.2 | Each kubeconfig is the region it claims, and both are in the registry | bootstrap-kit `SOVEREIGN_REGION_KEY` per cluster; `GET /sovereigns/{id}/regions`; the console topology |
| D0.3 | The Org is the Sovereign's own | `GET /api/v1/organizations` |
| D0.4 | That Org has an Environment spanning both regions | `GET /sovereigns/{id}/environments` |
| D0.5 | The Blueprint declares `active-hot-standby` | `GET /api/v1/catalog/{bp}` and its `/versions/{v}` |
| D0.6 | Records which images are delivered (what this run certifies) | `catalyst-system` Deployments in both regions |
| **J1.0** | A create with an unknown Org, Environment or Region each returns **422**, and nothing is created | 3 POSTs, then no namespace and no Application in either cluster |
| J1.1 | Create WordPress `accept-<ts>` with `active-hot-standby`, Primary A, Standby B → **201** | `POST /catalyst/v1/apps/instances`, the same body `NewInstanceDialog` sends |
| J1.2 | Application `Ready=True` (Unknown is not accepted), and placement reads A=Primary, B=Standby | `GET …/applications/{name}`, `…/placement` |
| J1.3 | A HelmRelease is `Ready=True` in **each** region | read from that region's cluster |
| J1.4 | Exactly one healthy CNPG Cluster per region. A's instance has `pg_is_in_recovery()=f`, B's has `t` | the region is the kubeconfig that returned the object, never a label |
| J1.5 | The app FQDN | `GET /catalyst/v1/apps/{uid}/endpoints` |
| J1.6 | The FQDN serves a **configured** WordPress (200, not `install.php`, has a WordPress fingerprint). If the installer shows, the suite completes it through its form first | HTTPS |
| J2.1 | The author credential authenticates | Creates an application password by piping a PHP script on stdin into the WordPress container. Password login is unusable because the chart sets OIDC `login_type=auto`. Checked with `users/me` over HTTPS |
| J2.2 | Publish a post with a unique marker in **< 5 s** | REST `POST /wp/v2/posts` (a blocked ADR-0004 synchronous commit shows up as a slow write) |
| J2.3 | Upload unique PNG bytes in **< 5 s** | REST `POST /wp/v2/media` |
| J2.4 | An anonymous read shows the marker, and the media is **byte-equal** (sha256) | HTTPS |
| J2.5 | **B's replica** holds the post and the attachment | `SELECT` on B's instance, inside `BEGIN READ ONLY` |
| J3.1 | Save placement with Primary B | `GET …/placement`, swap roles, `PUT …/applications/{name}` with `{placement:{targets,ownedDependencies}}`, which is the PlacementEditor Save |
| J3.2 | The **actual** primary is B: the Application's `dr.observedPrimaryRegion` is B and converged, and the Continuum's `status.primaryRegion` is B, converged, not in progress | `GET …/applications/{name}`, `GET …/continuums/{name}` |
| J3.3 | Jobs holds a `topology-transition` record for this app, started at or after the Save, with step rows that **all** say `succeeded` | `GET /deployments/{id}/jobs` |
| J3.4 | B's instance is the writer (`f`), A's is the replica (`t`) | psql `SELECT` |
| J3.5 | DNS for the FQDN answers **only** with B's addresses. If the two regions share an IP, the step fails because it cannot tell them apart | resolver; the answers are recorded |
| J3.6 | Every post written so far, and the byte-equal media, are served after the move | HTTPS |
| J3.7 | A new write in < 5 s succeeds, appears on B (writer) and replicates to A | REST and psql |
| J4.* | J3 again, back to A | |
| J5.1–2 | `DELETE …/applications/{name}` → 2xx. In **both** regions the Application, HelmReleases, CNPG Clusters and PVCs are gone, and the API GET returns 404 | |

A requested journey that never ran counts as **SKIP**, and any SKIP makes the run FAIL.
A `discover` record never certifies a journey.

## Which assertions depend on the other ADR-0015 slices

The suite is written against the **target** contract. On hw307 today
(`--discover`, 2026-11-08) it fails at **D0.5**: `bp-wordpress-tenant@0.4.26`
declares only `[active-passive, singleton]`.

| Assertion | Waits on |
|---|---|
| D0.5 / J1.1: the Blueprint offers `active-hot-standby` | D2 (topology contract + per-region render) |
| J1.0: 422 on an unknown Org, Env or Region, and no namespace created | D4 |
| J1.1 `environment` field honoured; region ids `hw-…-rtz-prod` accepted | D3 / D4 |
| J1.3: HelmRelease Ready **in B** | D1 (region B reconciles from its own Git path) |
| J1.4: exactly one CNPG half **per region**, with B's in recovery | D2 (CNPG native distributed topology, render only the `self` half) |
| J1.6 / J2.4 / J3.6: media survives a move | D6 (replicated media store) |
| J1.2 / J3.2: `Ready` / actual primary in canonical region ids (today `dr` for some apps reports `me-east-215-a`) | D3 / D7 |
| J3.3: the `topology-transition` Jobs record (today 0 such jobs) | D8 |
| J3.5: DNS moves to the serving region; distinct per-region IPs | D2 (PowerDNS health-checked records) |
| D0.2 / D0.4 discovery without the TEMPORARY fallbacks | D3 (region registry endpoint), D4 (environments list) |

**TEMPORARY fallbacks.** Each one is named in its step's evidence. The region id
comes from the bootstrap-kit `SOVEREIGN_REGION_KEY` until
`GET /sovereigns/{id}/regions` exists. Environments come from
`kubectl get environments` (region A) until the console has a list endpoint.
Region IPs come from `CILIUM_LB_IPAM_CIDRS` unless `ACCEPT_REGION_IPS` is set.

## What the suite writes, and what it never does

It writes only through the console API: one create, the placement PUTs and one
delete, plus the WordPress REST writes and the installer form. Two other
`kubectl exec` calls are made, both on the app's own pods. One is the J2.1
author setup in the WordPress container. The other runs single read-only
`SELECT`s on CNPG instances; `PSQLSelect` refuses anything else. The suite
never runs `kubectl apply`, `patch` or `delete`.

## The record, the UAT gate and the gate manifest

`--record` writes `docs/ledger/acceptance/<env>-<run_id>.json` (schema
`openova.io/acceptance-sovereign/v1`). The record holds the run id, env,
timestamps, subject (org, env, regions, app, FQDN, blueprint, delivered
images), per-journey results and every step with its evidence values.

- **UAT.** A function-clause row, as listed in
  `docs/ledger/uat-function-rows.csv`, turns ✅ only if its evidence cites
  `acceptance:<run_id>`. The cited record must be for the env in the ledger
  header, must be a `run`, and must PASS every journey that row needs. This is
  enforced by `scripts/check-function-rows-cite-acceptance.py`, which runs in
  the `uat-walk-respects-scheduler` workflow and has no bypass.
  `scripts/uat-confidence.py --observe` stamps each observation's
  `evidence_class` (`function`/`state`/`presence`). A function row's PASS that
  does not rest on `function` evidence is scored as NOTRUN.
- **Gate.** The gate has no mode and no override, and no job of
  `catalyst-build.yaml` waits for it. It is the manifest
  `docs/ledger/gates.yaml` and the tool `scripts/acceptance-gate.py`. The
  manifest holds one entry per check, in state `not-built`, `expected-fail`
  or `required`. A check moves forward only. Each check also declares its
  kind of proof: `fresh` for a check about installing, handing over or
  wiping, and `running` for every other check. The commands of the tool:
  - `validate --base <ref> --body-file <file>` checks the manifest against
    the manifest at `<ref>`. It refuses a backward move, and a move to
    `required` by a package other than the one the entry names. The workflow
    `gate-manifest` runs it on every pull request.
  - `verdict [--check <id>] [--json]` applies the pass rule to the records in
    `docs/ledger/acceptance/` and prints one line per check. A check that
    runs in the cloud is green when the two newest counting records are of
    one candidate, agree on chart versions and image digests, the newer one
    is no older than the manifest's `max_age_days`, and the check passed in
    each of them in which it applied. For a `running` check the two records
    may be of one Sovereign that was updated in place. For a `fresh` check
    only records of fresh installations count, and the two must be of
    different deployments. A version 1 record never counts. A check that is
    not green is printed with the pass rule that failed. With `--check` the
    exit code is 0 only if that check is green.
  - `validate-record <file>` checks a version 2 record against the schema.
  - `export` prints the manifest as JSON.
  - `--self-test` proves that each rule can fail.

## Layout

```
cmd/sovereign-acceptance   run | discover | mint-session
internal/api               console HTTP client (cookie auth, read-only mode)
internal/kube              kubectl wrapper: get, exec, SELECT-only psql
internal/session           RS256 session mint (stdlib)
internal/suite             discovery, journeys, record; match.go = pure decision helpers (unit-tested to FAIL)
internal/wordpress         installer form, REST writes, anonymous reads, unique PNG
```

Stdlib only, Go 1.23. Unit tests: `cd tests/acceptance/sovereign && go test ./...`.
