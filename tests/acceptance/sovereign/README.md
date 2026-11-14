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
| `ACCEPT_MANIFEST`, `ACCEPT_RUN_IN`, `ACCEPT_CAPABILITIES`, `ACCEPT_CANDIDATE_ID`, `ACCEPT_REPO_ROOT`, `ACCEPT_NODES_REQUESTED` | empty, `cloud`, empty, empty, `.`, empty | The gate checks. See "Gate checks" below. |

Exit codes: `0` PASS, `1` an assertion failed, `2` usage error or a gate manifest that cannot be used, `3` harness error.

In CI, use `.github/workflows/foundation-gate.yaml` (`workflow_dispatch` or
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

It writes through the console API: one create, the placement PUTs and one
delete, plus the WordPress REST writes and the installer form. It writes with
`kubectl create`, `apply` and `delete` in gate checks CMP-G01, CTL-G04, SEC-G01 and
SEC-G02 only. CMP-G01 creates a namespace `cmp-gate-<run>` of its own in the
first region, applies one Deployment with zero replicas in it, and deletes
that namespace before it ends. SEC-G01 and SEC-G02 create a namespace
`sec-etcd-<run>` or `sec-node-<run>` of their own in every region, start one
probe Pod on every control-plane node in it, run a script that only reads
files of the node in each Pod with `kubectl exec`, and delete that namespace
before they end. SEC-G01 also creates one Secret, the canary. SEC-G07
stores nothing: its `create` and `apply` are server-side dry runs. It asks
the API server for a token of the service account of the store
`vault-region1` that lives 10 minutes, logs in to OpenBao with it in an
OpenBao server pod with `kubectl exec`, reads two paths with the output
thrown away, and revokes the OpenBao token. Two other
`kubectl exec` calls are made, both on the app's own pods. One is the J2.1
author setup in the WordPress container. The other runs single
read-only `SELECT`s on CNPG instances; `PSQLSelect` refuses anything else. The
suite never runs `kubectl patch`. Gate checks FND-G05 and FND-G09 run
`scripts/check-live-nodeports.sh` and `scripts/check-live-node-encryption.sh`
of the checkout of `--repo-root`. Both only read: the first lists Services and
policies, the second also runs `cilium-dbg status` in every Cilium agent.
CTL-G04 tries to create, in a namespace `ctl-lock-<run>` of its own, a copy of an
object that the intent lock must refuse, and deletes both; `auth can-i` only asks.

## Gate checks

A gate check is one entry of `docs/ledger/gates.yaml`. Its code is a file
`internal/suite/check_<name>.go` that calls `RegisterCheck` in `init()` and
holds the identifier of the check as a string literal. The contract is in
`internal/suite/registry.go`.

| Flag (variable) | Default | Meaning |
|---|---|---|
| `--manifest` (`ACCEPT_MANIFEST`) | empty | The manifest as JSON: the output of `python3 scripts/acceptance-gate.py export`. Empty: no check runs. |
| `--run-in` (`ACCEPT_RUN_IN`) | `cloud` | `local` or `cloud`. A check is selected when its entry lists this value in `runs_in`. |
| `--capabilities` (`ACCEPT_CAPABILITIES`) | empty | Comma list of the capability flags this run declares, for example `multi-region`. |
| `--candidate-id` (`ACCEPT_CANDIDATE_ID`) | empty | The candidate this run certifies. Empty: the record certifies nothing. |
| `--repo-root` (`ACCEPT_REPO_ROOT`) | `.` | The checkout of the commit under test. Its `HEAD` is written as `candidate.commit`. |
| `--nodes-requested` (`ACCEPT_NODES_REQUESTED`) | empty | What the deployment request asked for, per region id: `<regionA>=6,<regionB>=6`. Check FND-G03 compares it with the Ready nodes. Empty: FND-G03 fails. A value in another form stops the run with exit code `3`. |
| `--previous-deployment-id` (`ACCEPT_PREVIOUS_DEPLOYMENT_ID`) | empty | The deployment whose wipe this fire follows. Check FND-G13 reads it, and the record carries it as `provision.previous_deployment_id`. Give it together with `--previous-wipe-orphans`, or neither: one without the other stops the run with exit code `3`. |
| `--previous-wipe-orphans` (`ACCEPT_PREVIOUS_WIPE_ORPHANS`) | empty | How many resources of that deployment a listing of the cloud account still found after the wipe, a whole number of 0 or more. Check FND-G13 passes on `0`. The record carries it as `provision.previous_wipe_orphans`. Another value stops the run with exit code `3`. |

With `--manifest`, a `run` selects every registered check whose entry is
`expected-fail` or `required`, and runs them after the journeys. An entry that
is selected and has no registered check stops the run before anything runs,
with exit code `2` and no record. A check whose `needs` the run did not
declare is recorded **SKIP** and is not run. When discovery (D0) failed no
check is run; each is recorded **SKIP**. A `required` check that does not
PASS fails the run, except a SKIP for `needs` that were not declared. An
`expected-fail` check never fails the run. `discover` runs no check.

The checks of the suite. Checks FND-G01 to FND-G05 and FND-G09 read every
region; a region that cannot be read fails the check. Checks FND-G08 and
FND-G10 to FND-G12 read no cluster: they report steps that the run recorded
before the checks, and a step that is missing fails the check. The two CMP
checks read the first region, the region the API runs in. The two SEC
checks read every control-plane node of every region; a region without a
node labelled `node-role.kubernetes.io/control-plane=true` fails them. A
platform namespace is a namespace without the label `openova.io/organization`.

| Check | File | It fails when |
|---|---|---|
| FND-G01 Candidate identity | `check_candidate.go` | The suite was not built from the candidate commit. A HelmRelease that is not suspended runs another chart version than `clusters/_template/bootstrap-kit/*.yaml` pins for it in the checkout of `--repo-root`. A container of a platform namespace has no image digest. One image reference has two digests. |
| FND-G02 Zero touch | `check_zero_touch.go` | A HelmRelease, or a Deployment or DaemonSet of `catalyst-system`, `cnpg-system` or `kube-system`, has a field manager named `kubectl` or `kubectl-…`. A Deployment, DaemonSet or StatefulSet of those namespaces or of `flux-system` has the annotation `kubectl.kubernetes.io/restartedAt` on its pod template. The clusters are looked at twice: before the journeys (steps `FND-G02.start.*`) and when the checks run. |
| FND-G03 Nodes | `check_nodes.go` | The Ready nodes of a region differ from `--nodes-requested`, in either direction, or the run did not say the number. |
| FND-G04 Releases and pods | `check_releases.go` | A HelmRelease that is not suspended is not `Ready=True`, or is stalled. A container of a platform namespace is in `CrashLoopBackOff`, or has more than 3 restarts. |
| FND-G05 No NodePort | `check_nodeport.go` | `scripts/check-live-nodeports.sh --kubeconfig <region>` exits with a code other than `0` for a region, or exits `0` without its line `OK: zero live node ports in owned namespaces across …` (the script exits `0` with a warning when it cannot read the Services). |
| FND-G06 Front doors | `check_front_doors.go` | One of four anonymous `GET /` of a public host does not answer 200, 301, 302, 303, 307 or 308, or its certificate is not publicly trusted. A 404 fails. A redirect is not followed. The hosts are `console`, `api`, `auth`, `gitea`, `harbor`, `bao`, `marketplace` and `pdns` under the name of the Sovereign that step D0.1 recorded. A host listed as closed must answer 401 or 403; none is listed today. |
| FND-G08 Mesh | `check_mesh.go` | Step D0.10 of the discovery is missing or did not pass. |
| FND-G09 Transit encryption | `check_encryption.go` | `scripts/check-live-node-encryption.sh` with the kubeconfig of every region exits with a code other than `0`. |
| FND-G10 Install and data | `check_journeys.go` | Journey J1 or J2 recorded no step, or one of its steps is not PASS. |
| FND-G11 Planned move | `check_journeys.go` | Journey J3 or J4 recorded no step, or one of its steps is not PASS. |
| FND-G12 Clean delete | `check_journeys.go` | Journey J5 recorded no step, or one of its steps is not PASS. |
| FND-G13 Clean wipe | `check_wipe.go` | The run did not get `--previous-deployment-id` and `--previous-wipe-orphans`, the previous deployment is the deployment under test, or the number of orphans is not `0`. |
| CMP-G01 The score moves by push | `check_compliance_push.go` | The scorecard of the Sovereign is empty. No frame of the compliance stream names the seed within 120 seconds. The Sovereign score does not count the seed, or the violations do not list it. The violations still list the seed 120 seconds after it was deleted. The seed is the Deployment `cmp-bad` with zero replicas in a namespace `cmp-gate-<run>` of its own; the check applies it with `kubectl` and deletes the namespace before it ends. It makes no request on a timer: it reads a route again only after a frame arrived. |
| CMP-G02 The shown mode is the engine's mode | `check_compliance_mode.go` | A policy that `GET /api/v1/sovereigns/{id}/compliance/policies` lists shows another mode than its engine object carries, or has no engine object. The engine object is the `ClusterPolicy` of that name, or the `ValidatingPolicy` when there is none. `enforcing` and `enforce` are the engine's enforce; `permissive` and `audit` are its audit. A policy of source `evaluator` without an engine object is not compared. |
| CTL-G04 The lock holds | `check_ctl_lock.go` | `scripts/intent-lock-report.py --kubeconfig <region>` fails for a region, or its report has no stage. No stage of the intent lock is at deny. A stage at deny is not at zero: a manager other than Flux owns `spec` of one of its objects, or its audit count is not 0 or null. A kind at deny has no live object to copy, or in a region that serves it a create of a copy is not refused by the lock. The service account of Deployment `catalyst-system/catalyst-api` may create, update, patch or delete a kind at deny. The report of each region is kept as evidence. |
| SEC-G01 etcd at rest | `check_sec_etcd.go` | On a control-plane node the value of the canary is in the clear in the etcd file `/var/lib/rancher/k3s/server/db/etcd/member/snap/db` or in a write-ahead log file beside it, or a record of the canary in the etcd file does not hold `k8s:enc:secretbox:v1:`. The canary is the Secret `etcd-canary` with a random value that the check creates in its namespace. A node whose files hold no record of the canary fails: nothing was measured. A probe Pod on each node reads the files (`sec_probe.go`). |
| SEC-G02 Node hygiene | `check_sec_node.go` | On a control-plane node the part after `::server:` of `/var/lib/rancher/k3s/server/token` is 64 lower-case hexadecimal characters, the form of a token derived from identifiers; `/etc/rancher/k3s/k3s.yaml` is missing or its mode is not 600; or a file `/var/lib/catalyst/*-secret.yaml` or `/var/lib/catalyst/flux-bootstrap.yaml` remains. The probe prints the class of the token, never the token. |
| SEC-G04 OpenBao system paths closed | `check_sec_route.go` | One of four anonymous `GET` of `https://bao.<FQDN>/v1/sys/seal-status` does not answer 200 with a JSON body that holds the boolean `sealed`. One of four anonymous `GET` of one of the 15 system path prefixes that `platform/openbao/chart/templates/httproute.yaml` closes, or of `/v1/sys/generate-root/attempt`, does not answer 302 with the Location `https://bao.<FQDN>/ui/` (the port 443 may be written). A redirect is not followed. `<FQDN>` is the `sovereignFQDN` of step D0.1. |
| SEC-G07 External Secrets scope | `check_sec_scope.go` | In a region, a server-side dry run of an ExternalSecret in namespace `default` with the remote key `sso/sec-g07-probe`, or in namespace `gitea` with the key `catalyst/tofu-phase0-archive`, is not refused by the admission rule `external-secret-remote-key-scope`; one in `gitea` with the key `sso/sec-g07-probe` is refused; an ExternalSecret or PushSecret outside the namespaces labelled `openova.io/organization` would be refused on its next update, or there is none to replay; or a token of the service account of the store `vault-region1`, logged in to OpenBao in a server pod, reads `<kv>/data/catalyst/tofu-phase0-archive` without a 403, or gets a 403 or an error on `<kv>/data/sso/sec-g07-probe`. |

FND-G02 also asserts that the deployment record shows no retry call. The
suite has no source for that yet, so the step is recorded **SKIP** and the
check cannot PASS.

## The record, the UAT gate and the gate manifest

`--record` writes `docs/ledger/acceptance/<env>-<run_id>.json` (schema
`openova.io/acceptance-sovereign/v2`). The record holds the run id, env,
timestamps, subject (org, env, regions, app, FQDN, blueprint, delivered
images), per-journey results and every step with its evidence values.
Version 2 adds where the run ran (`run_in`), the capability flags it declared,
the candidate, who produced the record (a workflow run, or `manual`), and one
result per selected gate check (`checks`). Check FND-G01 adds `artifacts`:
the chart version of every HelmRelease and the digest of every image of the
platform namespaces, per region. A record gets them only when every region
was read; without them it is never half of a pair. Of the object `provision`
the suite writes only `previous_deployment_id` and `previous_wipe_orphans`,
and only when it is given the two flags of a wipe. The other fields of that
object have no writer yet, so `validate-record` accepts a record of a `local`
run without those flags and refuses a record of a `cloud` run.

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
internal/suite             discovery, journeys, record; match.go = pure decision helpers (unit-tested to FAIL);
                           registry.go = the gate checks: contract, registry, selection, results
                           check_<name>.go = one gate check each
internal/wordpress         installer form, REST writes, anonymous reads, unique PNG
```

Stdlib only, Go 1.23. Unit tests: `cd tests/acceptance/sovereign && go test ./...`.
