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
| `CATALYST_SESSION` | — | Value of an owner `catalyst_session` cookie. Never printed. A run may outlive it (a minted session lives 3 hours): when the console answers a call HTTP 401, the suite mints one with the same issuer and e-mail from the Sovereign's own signer, read with `kubectl exec` from `catalyst-api` of the first region that gives it (`internal/suite/signin.go`), and sends the call once more; a second 401 fails the call with its own message. |
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

Exit codes: `0` PASS, `1` an assertion failed, `2` usage error or a gate manifest that cannot be used, `3` harness error, `4` NOT MEASURED (the discovery could not reach the Sovereign after its retries; see "When the Sovereign cannot be reached").

In CI, use `.github/workflows/foundation-gate.yaml` (`workflow_dispatch` or
`workflow_call`). It needs the `ACCEPTANCE_KUBECONFIG_A/B` secrets, and a
runner that can reach the Sovereign apiservers.

## When the Sovereign cannot be reached

The discovery steps (D0, including D0.10 and D0.11) read the Sovereign over the
console API and the Kubernetes API of each region. A read that got no answer
(connection refused, reset or closed, a TLS handshake or read that timed out, a
broken exec stream, an early EOF), or an answer that means "try again" (HTTP 429
or 5xx, kubectl's `Error from server` with ServiceUnavailable, InternalError,
Timeout or TooManyRequests, an etcd timeout), is made again: at most 5 attempts,
with waits of 2, 4, 8 and 16 seconds, and none later than 2 minutes after the
first (`internal/retry`). Every other answer, such as 403, 404 or a check's own
verdict, is never retried. Once a target gave up, its later reads in the run get
one attempt until one succeeds. The journeys and the checks never retry, because
a call of theirs may write.

A run is **NOT MEASURED** when a discovery step failed only because a region or
the console gave no answer after the retries, and no discovery step failed on an
answer. It prints `NOT MEASURED: …` and `RESULT NOT-MEASURED`, exits with code
`4`, and writes no record even with `--record`: it is not a gate result, pass or
fail, and never one of the two runs that the pass rule pairs, because the pass
rule reads only the records in `docs/ledger/acceptance/` (and `validate-record`
refuses a record whose result is not PASS or FAIL). A discovery step
that failed on an answer is a measurement: that run is FAIL and is recorded
(`internal/suite/reach.go`). The workflow ends such a run with the annotation
"Gate run NOT MEASURED", commits nothing, and sets its output `result` to
`NOT-MEASURED`. Gate run 3 on hw307 (15 Nov 2026) is the case: one broken exec
stream (D0.10) and one TLS handshake timeout (D0.11) from the runner wrote a FAIL
record without an artefact list, and that record kept the next run from forming
a pair.

## What each step asserts

Every step fails fast with a precise message. A step that times out reports the
last state it observed, so the error says what the system was doing.

| Step | Assertion | How it is measured |
|---|---|---|
| D0.1 | The session is an owner session | `GET /api/v1/whoami` |
| D0.2 | Each kubeconfig is the region it claims, and both are in the registry | `SOVEREIGN_REGION_KEY` of the bootstrap variables per cluster (the ConfigMap `flux-system/sovereign-variables`, else the substitutes of the Kustomization `flux-system/bootstrap-kit`); `GET /sovereigns/{id}/regions`; the console topology |
| D0.3 | The Org is the Sovereign's own | `GET /api/v1/organizations` |
| D0.4 | That Org has an Environment spanning both regions | `GET /sovereigns/{id}/environments` |
| D0.5 | The Blueprint declares `active-hot-standby` | `GET /api/v1/catalog/{bp}` and its `/versions/{v}` |
| D0.6 | Records which images are delivered (what this run certifies) | `catalyst-system` Deployments in both regions |
| D0.11 | A run in the cloud measures the installation under test for the record's object `provision`. It runs last, after the checks | the bootstrap variables, the namespace `kube-system` and the nodes of each region; the ConfigMap `catalyst/self-sovereign-cutover-status` of region A |
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
| J5.1–2 | `DELETE …/applications/{name}` → 2xx. In **both** regions the Application, HelmReleases, CNPG Clusters, PVCs and the Secrets of its namespace named after it are gone, and the API GET returns 404; the failure names each object left, with its region | |

A requested journey that never ran counts as **SKIP**, and any SKIP makes the run FAIL.
A `discover` record never certifies a journey.

A `run` that requests no journey (`--journeys ''`) does not run D0.3, D0.4 and
D0.5, because only the journeys need what they resolve. Its record holds no step
for them and names them in `subject.discovery_not_requested`. A `discover` run
and a run that requests a journey run all three. An empty `ACCEPT_JOURNEYS` is
not an empty list: it means the default.

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
`kubectl create`, `apply` and `delete` in gate checks CMP-G01, CTL-G04, CTL-G07,
INF-G01, INF-G02, NET-G06, NET-G10, NET-G18, SEC-G01, SEC-G02 and SEC-G03 only. CMP-G01 creates a namespace `cmp-gate-<run>` of its own in the
first region, applies one Deployment with zero replicas in it, and deletes
that namespace before it ends. SEC-G01 and SEC-G02 create a namespace
`sec-etcd-<run>` or `sec-node-<run>` of their own in every region, start one
probe Pod on every control-plane node in it, run a script that only reads
files of the node in each Pod with `kubectl exec`, and delete that namespace
before they end. SEC-G01 also creates one Secret, the canary. SEC-G03 does the same in a
namespace `sec-seal-<run>`, with probe Pods that run as the OpenBao user 100:1000 and mount
only `/var/lib/catalyst/seal`, read-only; it also deletes the Pod `openbao-0` of namespace
`openbao` in every region once, which the StatefulSet starts again, and runs `bao status`
and one token-free `bao write -f` of the root-generation attempt in it with `kubectl exec`.
SEC-G05 writes nothing: it runs one token-free `bao read` of a path no one else reads in
`openbao-0` with `kubectl exec` and reads that Pod's log. NET-G06 creates, in every
region, one dial Pod `pol-dial-<run>-<n>` for each source of
`policy-dials.yaml` in that source's namespace (the namespace `default` for
the rows of today), runs `nc` in it with `kubectl exec`, and deletes the Pods
before it ends; it creates no namespace. NET-G10 creates a namespace
`net-metadata-<run>` of its own in every region, starts two dial Pods in it,
one of them with the label `network.openova.io/metadata-access: "true"`, runs
`nc` in each with `kubectl exec`, and deletes that namespace before it ends. NET-G18
creates a namespace `net-path-<run>` of its own in every region, starts one probe
Pod on the host network of each of two workers in it, runs `ip`, `cat` and `wget` in
each with `kubectl exec`, and deletes that namespace before it ends; it creates no
Service. SEC-G07
stores nothing: its `create` and `apply` are server-side dry runs, like the two `apply` runs per object of NET-G15,
which reads `cilium-envoy` logs and sends the corpus `tests/waf/corpus/` to public hosts. SEC-G07 asks
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
CTL-G07 makes one grant through the console API (`POST …/rbac/assign`, tier
`viewer`) for a subject `ctl-g07-<run>` that no user has and an Application of
that name that does not exist, and removes it through the console API
(`DELETE …/admin/user-access/{name}`) before it ends. In every region that
runs pods of Deployment `catalyst-system/catalyst-api` it deletes those pods
with `kubectl delete pod` and waits with `kubectl rollout status` until the
Deployment has replaced them. It changes no object of the Deployment, so the
Deployment carries no restart annotation after it.
INF-G01 makes kured restart one worker node of every region, one region after
the other; kured never restarts a control-plane node. The worker is the one
that runs the registry's front, Service `harbor/harbor`, through which every
node pulls its images after the cutover, when a worker runs it: the check
makes the drain that can leave a region unable to pull. In a region it creates
a namespace `inf-reboot-<run>` of its own and one Pod on the chosen worker.
The Pod creates the file of its node that kured looks for
(`/var/run/reboot-required` with the settings of the kit) and ends. The check
deletes the namespace and from then on only reads the nodes, every Pod of the
region, the DaemonSet of kured and the HelmReleases.
It reads every Pod of the region at every reading of the nodes, so that each
controller of a Pod the drain moved is timed: from the first reading in which
none of its Pods is on the worker to the first in which it has its Ready Pods
back, at most 180 seconds, about twice the slowest Pod of a healthy drain. A
pull that stalls for minutes fails the check there, long before the time limit
of the node's restart.
It never restarts a node itself. The reboot span of kured is a setting of the
sovereign-admin, at night by default (01:00 to 05:00 in the Sovereign's own
time zone), so the check does not wait for it: when the span is not open for
the whole time limit, it opens a window for its run through the same setting.
It writes `KURED_START_TIME` and `KURED_END_TIME` of the region's variables
document, ConfigMap `flux-system/sovereign-variables`, as the minute of now and
that minute plus 15 minutes, the time limit and one more minute (the minute of
now has begun already), in kured's time zone, and waits at most 15 minutes
until kured runs with that span and every Pod of kured runs it. Kustomization
`bootstrap-kit` substitutes that document into kit slot 04b, whose values are
kured's arguments; nothing applies the document again (the node start-up data
creates it once with `kubectl create`), so the write holds for the run. Before
it writes, the check judges that span with the rule it applies to the
configured span: no upgrade Plan's window overlaps it and each ends at least
30 minutes before it starts. When the span the run needs breaks that rule, it
writes nothing and fails before any node is touched, naming the Plan and its
window. It writes nothing either while Flux applies the document (label
`kustomize.toolkit.fluxcd.io/name`): Flux would undo the change. With the span
it writes, in the same change, a note in the annotation
`catalyst.openova.io/inf-g01-window` of the document: the run, when, the values
before and the values written. After the region's last step, also when a step
failed, it writes the values before back (a key the document did not have is
removed) and removes the note, but only while the document holds exactly the
span it wrote: a document changed during the run is left as it is, and the
step fails, naming both values. Every run starts by looking for the note of an
earlier run in every region. A note is left when a runner is stopped between
the change and the restore; until then kured drains, inside the span, at most
one node at a time, and only a node that asks for a restart. The next run writes
the values before back by the same rule, removes the note, fails its step
`no previous run left its window open` with the earlier run's id and touches no
node.
Its record names the document, the values before, the values written and the
values restored. When it fails after
the Pod ran, its namespace is deleted all the same, and kured still
restarts the worker within its reboot span. The
sentinel file of a node is gone after that node has restarted, so nothing is
left to clean. The check runs only in a run that declares the capability flag
`node-reboot` (`--capabilities`, the input `capabilities` of the workflow
`foundation-gate`); every other run records it SKIP and restarts no node.
INF-G02 creates one MachineRun `catalyst-infra/inf-g02-<run>` of kind `scan`
for ScanPolicy `nodes` in every region and deletes it before it ends. The
operations controller then runs the scan Jobs and writes what a nightly scan
writes: the hardening reports, `Machine.status.sbom` and the files in the
evidence bucket. The check never reads the bucket.

BSS-G01 writes through the console API and the storefront, never with
`kubectl`. A sovereign-admin issues one voucher
(`POST /api/v1/org/billing/vouchers/issue`). Then a new customer of
`https://marketplace.<FQDN>/api` signs in with the code that the sign-in
service mails to an address of the gate lane's mailbox, creates one
Organization `g01-<run>`, places one order paid from that voucher and starts
its provisioning, as the checkout page does. At the end of the run the
customer deletes the Organization (`DELETE /api/tenant/orgs/{id}`, which ends
its subscription), the sovereign-admin deletes it through the platform as the
console does (`DELETE /api/v1/organizations/{id}`) and revokes the voucher,
and the check waits until no region holds the Organization object or its
namespace; it fails naming each one left, with its kind and region.
The mailbox is read over IMAP with `EXAMINE` and `BODY.PEEK`: nothing in it is
changed. With all four of `BSS_GATE_MAILBOX_IMAP` (`host:port`, implicit TLS),
`BSS_GATE_MAILBOX_USER`, `BSS_GATE_MAILBOX_PASSWORD` and
`BSS_GATE_MAILBOX_ADDRESS` (an address that holds `{tag}` once,
`gate+{tag}@example.org`) set, the check reads that mailbox. With none set it
reads the Sovereign's own: it reads the Secret
`platform/stalwart-sovereign-conformance`, which the setup Job of the mail
server (`bp-stalwart-sovereign`, kit slot 95) writes, with the kubeconfig of the
region that holds it, reaches the server's IMAP port through that region's API
server (`kubectl port-forward`), checks the certificate against `mail.<FQDN>`
and signs in `conformance+<run>@<FQDN>`. The password goes into no evidence.
Otherwise the check fails at its first step and sends nothing.

## Gate checks

A gate check is one entry of `docs/ledger/gates.yaml`. Its code is a file
`internal/suite/check_<name>.go` that calls `RegisterCheck` in `init()` and
holds the identifier of the check as a string literal. The contract is in
`internal/suite/registry.go`. The checks run in the order of the manifest,
except that a check that needs `node-reboot` runs before all the others: it
judges every container of a region, and a check before it may leave what it
bought there until the end of the run (BSS-G01 removes its Organization then).

| Flag (variable) | Default | Meaning |
|---|---|---|
| `--manifest` (`ACCEPT_MANIFEST`) | empty | The manifest as JSON: the output of `python3 scripts/acceptance-gate.py export`. Empty: no check runs. |
| `--run-in` (`ACCEPT_RUN_IN`) | `cloud` | `local`, `cloud` or `self`. A check is selected when its entry lists this value in `runs_in`. `self` is a Job inside a Sovereign; see "A run in self" below. |
| `--capabilities` (`ACCEPT_CAPABILITIES`) | empty | Comma list of the capability flags this run declares, for example `multi-region`. |
| `--candidate-id` (`ACCEPT_CANDIDATE_ID`) | empty | The candidate this run certifies. Empty: the record certifies nothing. |
| `--repo-root` (`ACCEPT_REPO_ROOT`) | `.` | The checkout of the commit under test. Its `HEAD` is written as `candidate.commit`. |
| `--nodes-requested` (`ACCEPT_NODES_REQUESTED`) | empty | What the deployment request asked for, per region id: `<regionA>=6,<regionB>=6`. Check FND-G03 compares it with the Ready nodes. Empty: FND-G03 fails. A value in another form stops the run with exit code `3`. |
| `--api-port-outside` (`ACCEPT_API_PORT_OUTSIDE`) | empty | What a TCP connection to port 6443 of every control-plane address gave from a machine that is in no list of the Sovereign's firewall: `<address>=<outcome>,<address>=<outcome>`, an outcome of `open`, `timeout`, `refused` or `error`. The gate workflow writes it with `scripts/gate-outside-dial.py` on a runner hosted by GitHub. Check NET-G02 (`check_api_port.go`) passes only when every control-plane address is `timeout` there and answers on 6443 from the machine that runs the suite. Empty: NET-G02 fails. A value in another form stops the run with exit code `3`. |
| `--peer-ports-outside` (`ACCEPT_PEER_PORTS_OUTSIDE`) | empty | What a TCP connection to port 2379 (the embedded etcd) and to port 12379 (the mesh endpoint) of every control-plane address gave from that same machine: `<address>:<port>=<outcome>,...`, an IPv6 address in brackets (`[2001:db8::1]:2379=timeout`), an outcome of `open`, `timeout`, `refused` or `error`. The gate workflow writes it with `scripts/gate-outside-dial.py dial-peer` on a runner hosted by GitHub. Check NET-G01 (`check_peer_ports.go`) passes only when both ports of every control-plane address are `timeout` there, the address answers on 6443 from the machine that runs the suite, and mesh step D0.10 passed. The check never dials 2379 or 12379 itself: the firewall lists the machine that runs the suite, so its dial is no outside dial. Empty: NET-G01 fails. A value in another form stops the run with exit code `3`. |
| `--previous-deployment-id` (`ACCEPT_PREVIOUS_DEPLOYMENT_ID`) | empty | The deployment whose wipe this fire follows. Check FND-G13 reads it, and the record carries it as `provision.previous_deployment_id`. Give it together with `--previous-wipe-orphans`, or neither: one without the other stops the run with exit code `3`. |
| `--previous-wipe-orphans` (`ACCEPT_PREVIOUS_WIPE_ORPHANS`) | empty | How many resources of that deployment a listing of the cloud account still found after the wipe, a whole number of 0 or more. Check FND-G13 passes on `0`. The record carries it as `provision.previous_wipe_orphans`. Another value stops the run with exit code `3`. |
| `--preflight` (`ACCEPT_PREFLIGHT`) | empty | What the flight checklist counted in the cloud account before this deployment was fired. See "The pre-flight numbers" below. Check FND-G13 reads it, and the record carries it as `provision.preflight`. Empty: FND-G13 fails. A value in another form stops the run with exit code `3`. |
| `--suite-image-digest` (`ACCEPT_SUITE_IMAGE_DIGEST`) | empty | A run in self: the digest of the image that the Job runs, `sha256:` and 64 hexadecimal characters. The record carries it as `suite_image_digest`. Another value stops a run in self with exit code `3`. |
| `--wait-for-release` (`ACCEPT_WAIT_FOR_RELEASE`) | `30` | A run in self: how many minutes step D0.12 waits until every HelmRelease runs the chart version its own spec names. `0` reads them once. |
| `--report-name` (`ACCEPT_REPORT_NAME`) | `conformance` | A run in self: the name of the ClusterPolicyReport that the run writes. |

**The pre-flight numbers.** The value of `--preflight` is one line:

```
<UTC time>;<kind>=<used>/<allowed>/<left>,<kind>=<used>/<allowed>/<left>,...
```

for example
`2026-11-20T05:55:00Z;servers=1/-/0,volumes=38/400/0,addresses=1/-/0,load_balancers=0/-/0,networks=1/5/0,security_groups=3/-/0,buckets=41/100/0,dns_records=-/-/0`.
The time is when the numbers were read: after the wipe and before the fire.
A kind is a lower-case name and is given once. `used` is how many resources
of the kind the account held, `allowed` how many it may hold, and `left` how
many the wiped deployment left. Each is a whole number of 0 or more, or `-`
when it was not measured. Nothing is ever written that was not measured. The
numbers come from the two tools of the flight checklist:

- `used` and `left` of a kind of the cloud account: the report that
  `scripts/flight-sweep.sh` printed in its last listing before the fire. Each
  entry of its list `kinds` holds `kind`, `used` and the list `leftovers`;
  `left` is the length of that list. Every kind of the report is given.
- `allowed`: the limit that the pre-flight gate `scripts/prov-preflight.sh`
  printed after the used count in its checks 3c (`networks`), 3d (`volumes`)
  and 3e (`buckets`). A kind whose limit no tool printed gets `-`.
- `dns_records`: `left` is the length of the list `leftovers` of the entry
  `dns_records` of the same report: the names of the wiped deployment that the
  authoritative servers of its parent domain still answer. That entry has no
  `used`; `used` and `allowed` are `-`. Check FND-G13 never assumes 0: a run
  without that entry gets `dns_records=-/-/-` and fails.

With `--manifest`, a `run` selects every registered check whose entry is
`expected-fail` or `required`, and runs them after the journeys. An entry that
is selected and has no registered check stops the run before anything runs,
with exit code `2` and no record. A check whose `needs` the run did not
declare is recorded **SKIP** and is not run. When discovery (D0) failed no
check is run; each is recorded **SKIP**. A `required` check that does not
PASS fails the run, except a SKIP for `needs` that were not declared. An
`expected-fail` check never fails the run. `discover` runs no check.

**A run in self.** `run --run-in self` is a Job inside a Sovereign. It needs
`--manifest`, `--suite-image-digest` and `--region-a`, skips the discovery and
the journeys, and reads the cluster of its pod with the pod's ServiceAccount.
Step D0.12 waits until every HelmRelease that is not suspended runs the chart
version its spec names. The run selects the checks that list `self` and are
not destructive, and writes one ClusterPolicyReport (`internal/suite/report.go`)
with one result per check: `pass`, `fail` (required), `warn` (expected-fail),
`skip`. The record's producer is `job` when `ACCEPT_JOB_NAMESPACE`,
`ACCEPT_JOB_NAME` and `ACCEPT_JOB_UID` name the Job. The binary exits `0` when
the report was written, also when a check failed.

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
| FND-G01 Candidate identity | `check_candidate.go` | The suite was not built from the candidate commit. A HelmRelease that is not suspended runs another chart version than `clusters/_template/bootstrap-kit/*.yaml` pins for it in the checkout of `--repo-root`. A container of a platform namespace has no image digest; a Pod that has not finished and has a container without one yet (the Pod of a CronJob seconds after it started) is read again every 5 seconds for up to 2 minutes and judged as last read, and one that is gone by then is listed and not judged. One image reference has two digests. In its `self` form, in a run in self, there is no checkout and no candidate: the commit of the suite and the pins are not judged, and a HelmRelease that is not suspended runs another chart version than its own `spec.chart.spec.version` names; a HelmRelease whose spec names no version is not compared. The digest rules are the same. |
| FND-G02 Zero touch | `check_zero_touch.go` | A HelmRelease, or a Deployment or DaemonSet of `catalyst-system`, `cnpg-system` or `kube-system`, has a field manager named `kubectl` or `kubectl-…`. A Deployment, DaemonSet or StatefulSet of those namespaces or of `flux-system` has the annotation `kubectl.kubernetes.io/restartedAt` on its pod template. The clusters are looked at twice: before the journeys (steps `FND-G02.start.*`) and when the checks run. |
| FND-G03 Nodes | `check_nodes.go` | The Ready nodes of a region differ from `--nodes-requested`, in either direction, or the run did not say the number. |
| FND-G04 Releases and pods | `check_releases.go` | A HelmRelease that is not suspended is not `Ready=True`, or is stalled. A container of a platform namespace is in `CrashLoopBackOff`, or has more than 3 restarts. |
| FND-G05 No NodePort | `check_nodeport.go` | `scripts/check-live-nodeports.sh --kubeconfig <region>` exits with a code other than `0` for a region, or exits `0` without its line `OK: zero live node ports in owned namespaces across …` (the script exits `0` with a warning when it cannot read the Services). |
| FND-G06 Front doors | `check_front_doors.go` | One of four anonymous `GET /` of a public host does not answer 200, 301, 302, 303, 307 or 308, or its certificate is not publicly trusted. A 404 fails. A redirect is not followed. The hosts are `console`, `api`, `auth`, `gitea`, `harbor`, `bao`, `marketplace` and `pdns` under the name of the Sovereign that step D0.1 recorded. `api` serves only an API and has no page at `/`, so it is asked at `GET /healthz` instead. A host listed as closed must answer 401 or 403; none is listed today. |
| FND-G08 Mesh | `check_mesh.go` | Step D0.10 of the discovery is missing or did not pass. |
| FND-G09 Transit encryption | `check_encryption.go` | `scripts/check-live-node-encryption.sh` with the kubeconfig of every region exits with a code other than `0`. |
| FND-G10 Install and data | `check_journeys.go` | Journey J1 or J2 recorded no step, or one of its steps is not PASS. |
| FND-G11 Planned move | `check_journeys.go` | Journey J3 or J4 recorded no step, or one of its steps is not PASS. |
| FND-G12 Clean delete | `check_journeys.go` | Journey J5 recorded no step, or one of its steps is not PASS. |
| FND-G13 Clean wipe | `check_wipe.go` | Step 1: the run did not get `--previous-deployment-id` and `--previous-wipe-orphans`, the previous deployment is the deployment under test, or the number of orphans is not `0`. Step 2: the run did not get `--preflight`; one of the kinds `servers`, `volumes`, `addresses`, `load_balancers`, `networks`, `security_groups`, `buckets` and `dns_records` has no count of leftovers; a kind has a count of leftovers above `0`; `networks` or `volumes` lacks its used or its allowed count; a kind has more used than allowed; or the numbers were not read before the environment was created (the earliest creation of the namespace `kube-system` in the regions), or that time cannot be read. |
| BSS-G01 Self-service onboarding | `check_bss_onboarding.go` | The run holds no mailbox: neither the gate lane's nor the Sovereign's own (see "What the suite writes"). The storefront lists no plan without a product and with a price, or no deployable application that is not a backing service. The voucher worth twice the cheapest plan is not issued. No sign-in mail reaches the address within 5 minutes, the code is refused, or the customer is a sovereign-admin. The voucher preview shows another credit, or the checkout of that plan with the application of the fewest backing services is not paid from credit. Within 30 minutes no region holds the Organization with `Ready=True`, the ResourceQuota `plan-quota` in its namespace and an HTTPRoute whose host `<application>.<organization>.*` answers 200. A call of the customer's steps was made with the console session. The price document lists the package bought within 5 minutes and the assertion of BSS-G02 then fails; when it does not list it, the evidence `price_list_state` is `not-seen` and the check does not fail. At the end of the run, after the customer's delete, the platform's delete (`DELETE /api/v1/organizations/{id}`; 404 is no failure by itself) and the revocation of the voucher, a region still holds the Organization object or its namespace after 10 minutes (a region that serves no kind Organization holds no such object), the reason naming each one left with its region and what each delete answered; or the platform's delete or the revocation answers neither 2xx nor 404. Later checks of the billing area use the Organization of this journey (`onboardedOrg`). |
| CMP-G01 The score moves by push | `check_compliance_push.go` | The scorecard of the Sovereign is empty. No frame of the compliance stream names the seed within 120 seconds. The score of the seed's own subject (`GET …/compliance/resource`, read by its name before the seed and after) does not enter the denominator or does not fail, or the violations do not list it; the whole Sovereign score is not compared, because other resources come and go between two readings. The violations still list the seed 120 seconds after it was deleted. The seed is the Deployment `cmp-bad` with zero replicas in a namespace `cmp-gate-<run>` of its own; the check applies it with `kubectl` and deletes the namespace before it ends. It makes no request on a timer: it reads a route again only after a frame arrived. |
| CMP-G02 The shown mode is the engine's mode | `check_compliance_mode.go` | A policy that `GET /api/v1/sovereigns/{id}/compliance/policies` lists shows another mode than its engine object carries, or has no engine object. The engine object is the `ClusterPolicy` of that name, or the `ValidatingPolicy` when there is none. `enforcing` and `enforce` are the engine's enforce; `permissive` and `audit` are its audit. A policy of source `evaluator` without an engine object is not compared. |
| CTL-G04 The lock holds | `check_ctl_lock.go` | `scripts/intent-lock-report.py --kubeconfig <region>` fails for a region, or its report has no stage. No stage of the intent lock is at deny. A stage at deny is not at zero: a manager other than Flux owns `spec` of one of its objects, or its audit count is not 0 or null. A kind at deny has no live object to copy, or in a region that serves it a create of a copy is not refused by the lock. The service account of Deployment `catalyst-system/catalyst-api` may create, update, patch or delete a kind at deny. The report of each region is kept as evidence. |
| CTL-G07 Audit survives a restart | `check_ctl_audit_survives_restart.go` | The grant is not answered with 201 and `created`. `GET /api/v1/sovereigns/{id}/audit/rbac` lists no `rbac-grant-created` event of that grant with an `id`, or its `transport` is not `jetstream:CATALYST_ORG`. No region runs a pod of the API, a pod is not replaced, the rollout does not finish, or a new pod is not Ready. After the restart the listing no longer holds the event with that `id`: the audit was kept in the memory of the pod only. The listing is asked at most five times, three seconds apart, and again only after no answer or a 502, 503 or 504. The grant cannot be removed. The check needs a console session and the deployment identifier. |
| INF-G01 Coordinated reboot | `check_inf_g01.go` | Journey J1 recorded no step, or one of its steps is not PASS; no node is touched then. In a region: DaemonSet `kube-system/kured` is missing, or carries the install hold of the chart bp-kured (the annotation kured locks in holds a lock of `catalyst-install-hold`, until the hand-over chain releases it), or can run on control-plane nodes (kured never restarts a control-plane node; the chart keeps it off them), or its arguments do not give the period, the lock release delay, the drain timeout, the start, the end and the time zone of the reboot span, or the sentinel file. An upgrade Plan (`plans.upgrade.cattle.io`, in any namespace) has no `spec.window` with `startTime` and `endTime` written as `HH:MM` and a `timeZone`, or its window overlaps the reboot span or ends less than 30 minutes before the span starts; a cluster that does not serve the kind, or has no Plan, passes this rule. The window and the span are compared as times of the day in UTC, each with the offset of its time zone at the time of the check; the days of the window and of the span are not read, and both count as open every day. The region has no node labelled `node-role.kubernetes.io/control-plane=true`, fewer than two worker nodes, no worker node that runs a workload Pod (a Running Pod not of a DaemonSet and not static), a node that is unschedulable or not Ready, or a container, init or main, that waits on an image (reason `ErrImagePull` or `ImagePullBackOff`) before the check starts. The chosen worker is the first by name that runs a Ready endpoint of Service `harbor/harbor`, the registry front through which every node pulls after the cutover (an endpoint of its EndpointSlices whose condition `ready` is true or not set); when no worker runs one, the first by name that runs a workload Pod. The evidence `chosen_by` says which rule chose. For each workload Pod on the worker the check records its controller (the owner reference marked controller; a ReplicaSet of a Deployment counts as the Deployment; a Pod of a Job or without a controller is not judged) and that controller's Ready Pods in the region. When the reboot span is not open for the whole time limit, the check writes a span for its run into `flux-system/sovereign-variables` (`KURED_START_TIME` and `KURED_END_TIME`, from the minute of now for 15 minutes, the time limit and one more minute, in kured's time zone): the document cannot be read or written, or DaemonSet `kube-system/kured` does not run with that span, every Pod updated and ready, within 15 minutes; no node is touched then. The values before are written back after the region's last step, also when a step failed. Just before the Pod is created no HelmRelease is Ready, so their recovery could not be judged. The Pod that creates the sentinel file does not finish within 180 seconds. After that the check reads the nodes and every Pod of the region every 5 seconds, and it fails when a control-plane node shows a new boot identifier; when two nodes are unschedulable in one reading; when a node is unschedulable for longer than drain timeout + lock release delay; when the worker shows a new boot identifier and was never read unschedulable; when a workload Pod that ran on the worker is still on it after its restart (the drain did not move it off); when a controller of a workload Pod that ran on the worker does not have as many Ready Pods as when the worker was chosen within 180 seconds of the first reading in which none of its Pods that ran there is on the worker (gone, being deleted or ended), the reason naming the controller, its images and the seconds, and the evidence `controllers_seconds_back` giving each controller's seconds to be back; or when, at the time limit of period + lock release delay + 15 minutes (50 minutes with the settings of the kit), the worker has not restarted, or a node is unschedulable or not Ready, DaemonSet `kube-system/kured` does not have every Pod ready, a HelmRelease that was Ready just before the Pod was created is not Ready again, a controller of a workload Pod that ran on the worker has fewer Ready Pods than it had when the worker was chosen, or a container, init or main, of a Pod of the region waits on an image, even when every HelmRelease is Ready; the reason names each such controller and container with its image. A reading that fails is no observation. A region is started only when every region before it passed. The check runs only in a run that declares the capability flag `node-reboot`; every other run records it SKIP and touches no node. |
| INF-G02 Node host scans | `check_inf_g02.go` | In a region: ScanPolicy `catalyst-infra/nodes` is missing, or a Node has no Machine of its name in `catalyst-infra` of class `node` and in phase `Active`, so the policy does not select it; no MachineRun is created then. The MachineRun `inf-g02-<run>` that the check creates is refused, or the runs of all regions do not reach a final phase within 30 minutes, or a run ends other than `Succeeded`. For a Node: its target of the run is not `Succeeded`, lists no evidence `catalyst-infra/<node>/<run>/sbom.cdx.json` or names another report than `machine-<node>-hardening`; `Machine.status.sbom` holds another key, a `sha256` that is not 64 lower-case hexadecimal digits, or a collection time that is missing or not in RFC 3339 form; PolicyReport `catalyst-infra/machine-<node>-hardening` is missing, has no result, holds only the row `no-profile-for-release` (the baseline has no profile for the release of the node, so no hardening rule was checked), or has a result whose `runName` is another run. The bill of materials is judged by the key and checksum in `Machine.status.sbom`, read through the Kubernetes API; the runner refuses to upload a file that is not CycloneDX. |
| NET-G06 Default deny | `check_policy_dials.go` | In a region, a row of `policy-dials.yaml` does not give what it expects. Each row is dialed with busybox `nc` (TCP, 3 seconds) from a Pod in its `from.namespace` with its `from.labels`, to `<service>.<namespace>.svc.cluster.local` or an IP address, on its port: `allow` passes when the connection is made, `deny` when the dial times out. A `deny` row to a Service also needs a ready endpoint of that Service (its EndpointSlices), or it fails, because nothing listens. A refused connection fails both. The Pod first dials the cluster DNS service on TCP port 53; when that does not connect, the row fails, because nothing was measured. The table cannot be read, or a dial Pod is not Ready within 180 seconds. |
| NET-G10 Instance metadata blocked | `check_metadata_blocked.go` | In a region, a dial Pod without the label `network.openova.io/metadata-access` connects to `169.254.169.254` on TCP port 80, or is refused there, instead of timing out; or a dial Pod with that label set to `"true"` does not connect there, because then nothing answers at the address and a timeout proves nothing. Both Pods are on the Pod network in a namespace `net-metadata-<run>` of the check's own and dial with busybox `nc` (TCP, 3 seconds), each after a dial of the cluster DNS service on TCP port 53 that must connect, or its step fails. A dial Pod is not Ready within 180 seconds. |
| NET-G15 Firewall proof | `check_waf.go` | No region holds a WebApplicationFirewall object (`webapplicationfirewalls.compose.openova.io`), or none serves the kind: one step fails with the name "no web application firewall in the path". For an object: its Gateway (`spec.gatewayRef.name`, in the namespace of the object) has no listener, or no HTTPRoute whose parentRefs name it gives a host name without a wildcard. WAF-1: a GET of `https://<host>/` does not answer 200 or a redirect. WAF-2: a request of the corpus `tests/waf/corpus/` to a host in mode `block` (its entry of `spec.hostOverrides`, else `spec.mode`) does not answer 403; an exclusion that lets one through fails it too. WAF-3: a corpus request to a host in mode `detect` answers 403, or no line in the logs of the `cilium-envoy` Pods of all regions holds `Coraza` and the query argument `net-g15=<run>-<host>-<entry>-` that marks the request; an implementation other than `coraza` has no log reader yet. A request that gets no answer fails, and the other requests to its host are not sent. A mode that no host is in measured nothing and fails its step. WAF-4: a TCP dial (5 seconds) connects to an ExternalIP of a Node on a listener port of the Gateway, or on 80 or 443 for implementation `native`, or to an address that a host resolves to on a listener port other than 80 and 443; with implementation `coraza` a listener of the Gateway is on 80 or 443; or there was no address to dial. WAF-5: a server-side dry-run update of the object with one more exclusion that expired yesterday is accepted, or the same with tomorrow is refused. |
| NET-G18 Full-size transfers between workers | `check_network_full_size.go` | A region has fewer than two workers (Ready, schedulable, without the label `node-role.kubernetes.io/control-plane`): not measured. A probe Pod on the host network of one of the first two workers by name is not Ready within 180 seconds. On a worker, the MTU of `cilium_host` (`/sys/class/net`) differs from the MTU of the interface that holds the node's InternalIP. A worker's `wget` of `http://<InternalIP>:9965/metrics` (the Hubble metrics listener of the other worker of its region, or of the first worker of another region from the first worker of its own) under `timeout 5` does not exit 0 with at least 16384 bytes. Before each such GET the same GET of the control-plane node of the destination's region must pass, else the pair fails as not measured. Runs in the cloud only: the local pack has at most one worker per region. |
| SEC-G01 etcd at rest | `check_sec_etcd.go` | On a control-plane node the value of the canary is in the clear in the etcd file `/var/lib/rancher/k3s/server/db/etcd/member/snap/db` or in a write-ahead log file beside it, or a record of the canary in the etcd file does not hold `k8s:enc:secretbox:v1:`. The canary is the Secret `etcd-canary` with a random value that the check creates in its namespace. A node whose files hold no record of the canary fails: nothing was measured. A probe Pod on each node reads the files (`sec_probe.go`). |
| SEC-G02 Node hygiene | `check_sec_node.go` | On a control-plane node the part after `::server:` of `/var/lib/rancher/k3s/server/token` is 64 lower-case hexadecimal characters, the form of a token derived from identifiers; `/etc/rancher/k3s/k3s.yaml` is missing or its mode is not 600; or a file `/var/lib/catalyst/*-secret.yaml` or `/var/lib/catalyst/flux-bootstrap.yaml` remains. The probe prints the class of the token, never the token. |
| SEC-G03 OpenBao seal | `check_sec_seal.go` | On a control-plane node there is no key file `/var/lib/catalyst/seal/openbao-seal-*.key`, or one does not have mode 400, owner `100:1000` and 32 bytes (a probe Pod as user 100:1000 reads it, `sec_probe.go`; whether the directory is a mount of its own is recorded). In a region, one of the four Secrets of the old init flow (unseal keys, `openbao-root-token`, `openbao-init-trace`, `openbao-recovery-seed`; `secSealSecrets`) or a Job exists in namespace `openbao`; `bao status` in `openbao-0` reports a seal of type `shamir`, sealed or not initialised; a token-free `PUT` of the root-generation attempt in that Pod is not refused with a 4xx answer; or after `delete pod openbao-0` the new Pod is not Ready and unsealed within 60 seconds. |
| SEC-G04 OpenBao system paths closed | `check_sec_route.go` | One of four anonymous `GET` of `https://bao.<FQDN>/v1/sys/seal-status` does not answer 200 with a JSON body that holds the boolean `sealed`. One of four anonymous `GET` of one of the 15 system path prefixes that `platform/openbao/chart/templates/httproute.yaml` closes, or of `/v1/sys/generate-root/attempt`, does not answer 302 with the Location `https://bao.<FQDN>/ui/` (the port 443 may be written). A redirect is not followed. `<FQDN>` is the `sovereignFQDN` of step D0.1. |
| SEC-G05 OpenBao audit device | `check_sec_seal.go` | In a region, ConfigMap `openbao-seal-config` declares no file audit device `stdout/` writing to `stdout`, or the server does not load it; or a token-free `bao read` of `secret/data/sec-g05-<run>` in `openbao-0` is not refused with 403, or the log of `openbao-0` of the last 60 seconds holds no audit response of that path. The evidence holds the request identifier of the line. Listing the audit devices needs a token with `sudo`; the workflow `test-bp-openbao-fresh-prov` lists `stdout/` with a test-only token. |
| SEC-G06 Keycloak client secrets not computable | `check_sec_differ.go` | No region holds a realm import of bp-keycloak (a ConfigMap of namespace `keycloak` whose data value is a JSON realm), none is named `<release>-sovereign-realm-config`, or the imports hold no client secret that is not a placeholder: nothing was measured. In a region, the secret of a client or the `clientSecret` of an identity provider equals the SHA-256 of `<release>\|keycloak\|<form>` for the form `tier1-sso\|<client>`, `tier2-sso\|<client>`, `catalyst-api-server-sa`, `catalyst-pin-broker` or `tenant-broker\|<org>`: a seed without the salt of the Sovereign. The evidence holds the first 16 hexadecimal characters of the SHA-256 of each secret, never the secret. |
| SEC-G07 External Secrets scope | `check_sec_scope.go` | In a region, a server-side dry run of an ExternalSecret in namespace `default` with the remote key `sso/sec-g07-probe`, or in namespace `gitea` with the key `catalyst/tofu-phase0-archive`, is not refused by the admission rule `external-secret-remote-key-scope`; one in `gitea` with the key `sso/sec-g07-probe` is refused; an ExternalSecret or PushSecret outside the namespaces labelled `openova.io/organization` would be refused on its next update, or there is none to replay; or a token of the service account of the store `vault-region1`, logged in to OpenBao in a server pod, reads `<kv>/data/catalyst/tofu-phase0-archive` without a 403, or gets a 403 or an error on `<kv>/data/sso/sec-g07-probe`. |
| SEC-G08 Injection negative | `check_sec_inject.go` | In a region, a canary born in OpenBao at `mounted/data/<namespace>/sa/sec-g08-reader/canary/e1` by role `eso-ns` is not in the file a Pod of `sec-g08-reader` mounts through the Secrets Store CSI driver (provider `openbao`, role `workload`, audience `openbao`); a Secret object of the region holds the canary in a data value, or no Secret is listed; `kubectl auth can-i create secrets` as `kube-system/secrets-store-csi-driver` does not print `no`; or a token of `sec-g08-other` logged in with role `workload` gets no 403 on the canary path, or one of `sec-g08-reader` cannot read it. Runs in the cloud only: the local pack leaves bp-openbao out, and with it slot 15b, which depends on it. |
| SEC-G11 Secret census | `check_sec_census.go` | In a region, more Secrets of the platform namespaces match no entry of `scripts/secret-guard-baselines/secret-object-register.yaml` than its `allowedUnmatched`, or no Secret was judged. The rule is `scripts/check-secret-object-register.sh --census` of the checkout of `--repo-root`: it does not judge the types `kubernetes.io/service-account-token`, `helm.sh/release.v1` and `bootstrap.kubernetes.io/token`, and a Secret is matched when an entry has its namespace (or `*`) and a `name` pattern that matches the whole name. The check reads the namespace, name and type of each Secret, never a value. The evidence names the unmatched Secrets and the class U and T entries past their `reviewBy`. |

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
was read; without them it is never half of a pair. A run in the cloud
always writes the object `provision`, measured in step D0.11; the rules for
each field are in `internal/suite/provision.go`. It is `fresh` only when the
run names a candidate, the environment was created after that candidate was
cut, and check FND-G02 passed at the start and at the end in every region, so
an environment that was updated in place is never fresh. The two flags of a
wipe add `previous_deployment_id` and `previous_wipe_orphans`, and
`--preflight` adds the object `preflight`: the time the numbers were read
(`measured_at`) and one entry per kind (`kind`, and `used`, `allowed` and
`leftovers` where they were measured). A run that is not in the cloud writes
only these three, and only when it is given them.

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
    runs in the cloud is judged on the newest valid pair: the newest two
    counting records that are of one candidate, ran one after the other and
    agree on chart versions and image digests. It is green when the pair's
    newer record is no older than the manifest's `max_age_days`, the check
    passed in each record of the pair in which it applied, and no counting
    record newer than the pair failed it where it applied. The first record
    of a newly delivered candidate therefore does not take the verdict away
    while it waits for its second run: where the check passes in it, it
    changes nothing until its candidate has a pair; where the check fails in
    it, the check is not green and that record is named. For a `running`
    check the records may be of one Sovereign that was updated in place. For
    a `fresh` check only records of fresh installations count, and the two
    of the pair must be of different deployments. A version 1 record never
    counts. A check that is not green is printed with the pass rule that
    failed. With `--check` the exit code is 0 only if that check is green.
  - `validate-record <file>` checks a version 2 record against the schema.
  - `export` prints the manifest as JSON.
  - `--self-test` proves that each rule can fail.

## Layout

```
cmd/sovereign-acceptance   run | discover | mint-session
internal/api               console HTTP client (cookie auth, read-only mode)
internal/kube              kubectl wrapper: get, exec, SELECT-only psql
internal/retry             which failed reads are retried, and the bound (the discovery only)
internal/session           RS256 session mint (stdlib)
internal/suite             discovery, journeys, record; match.go = pure decision helpers (unit-tested to FAIL);
                           registry.go = the gate checks: contract, registry, selection, results
                           check_<name>.go = one gate check each
internal/wordpress         installer form, REST writes, anonymous reads, unique PNG
```

Stdlib only, Go 1.23. Unit tests: `cd tests/acceptance/sovereign && go test ./...`.
