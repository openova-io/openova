# machine-runner

The container image of the runner Jobs of the machine plane: one short-lived Job per run and
target, which runs one task and stores what it found.

## Role in Catalyst

Infrastructure plumbing of the Blueprint `bp-machine-operations` (`platform/machine-operations/`).
The image is not an Application and is not installed on its own. It is run as a Job in the
namespace `machine-ops` by the operations controller of that Blueprint; in this version no
controller starts it yet. The machine plane is described
in its decision record,
[`docs/adr/0031-infrastructure-plane.md`](../../docs/adr/0031-infrastructure-plane.md).

## What the image holds

- `/usr/local/bin/machine-runner`, built from `products/catalyst/bootstrap/api/cmd/machine-runner`.
- The tools of `tools.lock.yaml`, each at an exact version whose SHA-256 the build checks:
  ansible-core, ansible-runner, Trivy, the OpenSCAP scanner with `oscap-chroot`, the
  ComplianceAsCode data streams of the Ubuntu and RHEL families under
  `/content/scap/<release>/`, Nuclei with the signed templates of one template release
  under `/content/nuclei-templates/` (only the folders of the protocols tcp, ssl, dns and
  http), and ZAP with its baseline script under `/zap` (only the ten add-ons of the passive
  baseline scan: no active-scan rule, fuzzer, brute-force or out-of-band add-on),
  with the Java runtime from the Debian archive of the base image.
- `LICENSES.md`: the licence of each tool and how it is used.

The default user is `65534`. A node-mode Job runs as root with a read-only root file system and
the node's file system mounted read-only at `/host`.

The home of the image (`HOME`) is the work folder `/run/machine-runner`, which is empty in the
image, and `/tmp` is a link to the same folder. A runner Job mounts a memory volume at
`/run/machine-runner` and no volume at `/tmp`. So a tool that writes in its home or in `/tmp`
(ansible-core with `~/.ansible/tmp`, Trivy, oscap with `/tmp/oscap.XXXXXX`) works with a
read-only root file system, and nothing stays from one run to the next.

## Tasks of this version (node mode)

| Task | Collector | Files stored |
|---|---|---|
| `inventory` | `trivy rootfs --format cyclonedx --skip-db-update --offline-scan /host` | `sbom.cdx.json` |
| `hardening` | `oscap-chroot /host xccdf eval` with the profile, the data stream of the node's release and a tailoring file of the disabled rules | `hardening.arf.xml`, `hardening.results.json` |

The inventory task updates no database and fetches nothing. A node whose release has no data
stream in the image gets outcome `Succeeded` with no rule and the message
`no-profile-for-release`.

## Task test (the Job shape of SSH mode, no SSH session)

`--task=test --mode=ssh` runs the passive profiles of a test scope against one declared machine.
It opens no SSH session: the Job's egress policy allows the target address on the scope's ports
only. The target list is built from `--address` and `--ports` and nothing else, one line
`<address>:<port>` per port; it is written, read back and checked before the tool starts. An
address that is not one unicast address of a machine (a name, a range, loopback, link-local,
multicast, broadcast, unspecified) and a port outside 1 to 65535 or given twice give outcome
`Refused` and start nothing.

| Profile | Tool | File stored |
|---|---|---|
| `network-passive` | `nuclei -duc -t /content/nuclei-templates -l <targets> -pt tcp,ssl,dns,http -dut -tags tech,detect,ssl,tls,dns -etags intrusive,dos,fuzz,bruteforce,default-login,vuln -ni -dr -rl <requests per second> -se <file> -silent -nc` | `test.sarif.json` |
| `web-passive` | `/zap/zap-baseline.py -t <scheme>://<address>:<port>/ -J zap-<port>.json -m 1 -z "-silent -dir <home>"`, once per port | `test.sarif.json` |

Profile `network-passive` runs signed templates only, of the protocols tcp, ssl, dns and http;
no code, headless or fuzzing template runs. It runs only fingerprints of services and
technologies and the TLS and DNS checks, never a template that changes state, denies service,
guesses passwords or probes a vulnerability (vulnerabilities come from the bill of materials).
There is no update check, no template download, no out-of-band interaction server and no HTTP
redirect. A tcp template may dial its own port; the egress policy drops it unless it is a port
of the scope.

Profile `web-passive` is the ZAP baseline scan: the spider for at most one minute and the
passive scan rules, with no active scan, no fuzzing and no attack. Port 443 is https and port 80
is http; on any other port the runner tries a TLS handshake, whatever the certificate, with no
request: https if it completes, else http. Each run has its own home under the work folder,
fetches nothing (`-silent`) and writes its JSON report into `/zap/wrk`, a link to the work
folder. Exit codes 0, 1 and 2 are results; any other is outcome `Failed`. The runner converts
each report into one SARIF run, one result per alert instance, with the levels of ZAP's own
SARIF report: risk High `error`, Medium `warning`, Low `note`, Informational `none`.

`test.sarif.json` (evidence kind `test-sarif`) holds one SARIF 2.1.0 run per tool run, in the
order of the profiles; the result member `test` gives the number of results and the object key
of the file.
`--task=test --mode=node` and every other task in SSH mode give outcome `Refused`.

## Arguments

`machine-runner --task=<inventory|hardening|test> --mode=<node|ssh> --machine=<namespace>/<name>
--run=<namespace>/<name> --os-family=<family> --os-version=<version>
--evidence-endpoint=https://<host>[:<port>] --evidence-region=<region>
--evidence-bucket=<bucket> --evidence-prefix=<machine namespace>/<machine name>/<run name>/`,
for `hardening` also `--profile-id`, `--content-release` and `--disabled-rules`
(comma-separated XCCDF rule identifiers), and for `test` also `--address`, `--ports`
(comma-separated), `--test-profile` (comma-separated, each once) and `--requests-per-second`
(1 to 50, default 10; the rate of Nuclei, not passed to ZAP).

## Evidence and result

Each file is stored in the evidence bucket under `<prefix><file>`, over HTTPS with certificate
validation. The key pair is read from the object-storage binding mounted at
`/var/run/secrets/evidence-store` (files `access-key-id` and `secret-access-key`, and `ca.crt`
when the endpoint's certificate is not publicly trusted). Nothing is stored for a task that did
not finish.

The program writes one result document (`openova.io/machine-run-result/v1`, at most 4096 bytes)
to `/dev/termination-log` and to stdout. `completed` is true only when the task wrote its
completion marker; a collector that was stopped gives outcome `Incomplete`.

## Build and upgrade

`.github/workflows/build-machine-runner.yaml` runs the runner's tests and builds the image on a
pull request, and pushes, signs and attests `ghcr.io/openova-io/openova/machine-runner:<short SHA>`
on `main`. An upgrade of a tool changes its entry in `tools.lock.yaml` (version, source and
sha256 together) and the matching row of `LICENSES.md`.
