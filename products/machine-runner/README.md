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
  ansible-core, ansible-runner, Trivy, the OpenSCAP scanner with `oscap-chroot`, and the
  ComplianceAsCode data streams of the Ubuntu and RHEL families under
  `/content/scap/<release>/`.
- `LICENSES.md`: the licence of each tool and how it is used.

The default user is `65534`. A node-mode Job runs as root with a read-only root file system and
the node's file system mounted read-only at `/host`.

## Tasks of this version (node mode)

| Task | Collector | Files stored |
|---|---|---|
| `inventory` | `trivy rootfs --format cyclonedx --skip-db-update --offline-scan /host` | `sbom.cdx.json` |
| `hardening` | `oscap-chroot /host xccdf eval` with the profile, the data stream of the node's release and a tailoring file of the disabled rules | `hardening.arf.xml`, `hardening.results.json` |

The inventory task updates no database and fetches nothing. A node whose release has no data
stream in the image gets outcome `Succeeded` with no rule and the message
`no-profile-for-release`. Every other task and SSH mode give outcome `Refused`.

## Arguments

`machine-runner --task=<inventory|hardening> --mode=node --machine=<namespace>/<name>
--run=<namespace>/<name> --os-family=<family> --os-version=<version>
--evidence-endpoint=https://<host>[:<port>] --evidence-region=<region>
--evidence-bucket=<bucket> --evidence-prefix=<machine namespace>/<machine name>/<run name>/`,
and for `hardening` also `--profile-id`, `--content-release` and `--disabled-rules`
(comma-separated XCCDF rule identifiers).

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
