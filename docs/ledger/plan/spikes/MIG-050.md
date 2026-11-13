outcome: D1
convert_seconds: 235
boot_seconds: 50
fixture_url: https://cloud.debian.org/images/cloud/bookworm/20261026-2643/debian-12-nocloud-amd64-20261026-2643.qcow2
fixture_sha256: 6e3b51890412842822d09925744543b8f60185fa73964efa7107a379457c8a20
virt_v2v_in_place: packaged
distribution: Debian GNU/Linux 13 (trixie)
dev_kvm_on_runner: present
fixture_raw_sha256: 7a4e0182d5588c2e6c9ad656023b76e74f37444827f555c03d5d058cba4367a9

# MIG-050 result: hermetic conversion and boot on a standard CI runner

## Question

How long do a `virt-v2v-in-place` conversion and a boot of the converted disk take on a standard GitHub-hosted runner? The answer decides how often the hermetic conversion-and-boot test can run:

- D1: conversion plus boot take 20 minutes or less, so the test runs on every pull request that touches the module.
- D2: they take longer, so the test runs on merge groups only.

## Answer

The outcome is D1.

The test will run inside a Debian-stable worker image with no KVM. Under those conditions, conversion plus boot took 259 to 285 seconds over three runs. That is under 5 minutes, a quarter of the limit. The slowest condition measured at all took 718 seconds, which is still under 20 minutes.

The header values come from the slowest of the three samples in the worker-image condition (run 1).

## What was run

Everything ran in GitHub Actions in `openova-io/openova`, on branch `spike/MIG-050`, which is never merged. The workflow is `.github/workflows/spike-mig-050.yaml` and the script is `tests/spikes/MIG-050/measure.sh`. Each condition ran on its own fresh runner. The workflow used `actions/checkout@v4` and `actions/upload-artifact@v4`.

The runner was GitHub-hosted `ubuntu-24.04`:

- Runner images: ubuntu24 20261101.360.1 and 20261111.370.1.
- 4 vCPUs and 15,983 MiB of memory.
- Kernel 7.0.0-1017-azure.
- `/dev/kvm` exists, mode `crw-rw----`, owned by root:kvm.

Each job ran these steps:

1. Install `nbdkit`, `libguestfs-tools`, `virt-v2v`, `qemu-system-x86` and `qemu-utils` from the distribution.
2. Download the fixture. Check it against the SHA-512 that Debian publishes, then record its SHA-256.
3. Copy it to a raw file with `qemu-img convert -f qcow2 -O raw`.
4. Run `virt-v2v-in-place -v -x -i disk <raw>` with `LIBGUESTFS_BACKEND=direct`. This is timed as `convert_seconds`.
5. Boot with `qemu-system-x86_64 -machine q35,accel=tcg -m 1024 -nographic -nic user,model=virtio-net-pci,mac=52:54:00:aa:bb:cc -drive file=<raw>,format=raw,if=virtio`. Poll the serial output once a second for `login:`. This is timed as `boot_seconds`.
6. Runs 2 and 3 only: as root, launch the libguestfs appliance with `guestfish -a /dev/null run`, once with and once without `--network`.
7. Run 3 only: a second case. A fresh raw copy of the fixture and an empty 64 MiB raw file are described in a libvirt domain XML and converted with `virt-v2v-in-place -i libvirtxml <xml>`.

There were four conditions. The guest boot uses TCG in all of them, as the test will.

- **Debian container, no KVM.** Image `debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2`, started with a plain `docker run` as root: no `--privileged` and no `/dev/kvm`. This is how a test inside a Debian-stable worker image runs.
  - Packages: virt-v2v 2.6.0-1, libguestfs 1:1.54.1-2+deb13u1, nbdkit 1.42.3-1, qemu 1:10.0.13+ds-0+deb13u1 and supermin 5.2.2-6+b3.
  - `linux-image-amd64` (6.12.116+deb13) was installed as the appliance kernel.
  - `passt` is not installed.
- **Debian container with KVM.** The same, plus `--device /dev/kvm`.
- **Ubuntu on the runner, appliance on KVM.** Ubuntu 24.04 packages on the runner itself.
  - Packages: virt-v2v 2.4.0-2build4, libguestfs 1:1.52.0-5ubuntu3, nbdkit 1.36.3-1ubuntu10, qemu 1:8.2.2+ds-0ubuntu1.18, supermin 5.2.2-4ubuntu4 and passt 0.0~git20240220.1e6f92b-1.
  - Conversion and boot run as the `runner` user, who can open `/dev/kvm`.
- **Ubuntu on the runner, appliance on TCG.** The same, with `LIBGUESTFS_BACKEND_SETTINGS=force_tcg`.

The fixture is the Debian 12 "nocloud" amd64 image, dated build 20261026-2643. Debian keeps its dated builds; the listing goes back to 2023.

- The qcow2 file is 421,527,552 bytes and the disk inside is 3 GiB.
- The raw copy made by `qemu-img convert` has SHA-256 `7a4e0182d5588c2e6c9ad656023b76e74f37444827f555c03d5d058cba4367a9`. All 11 jobs got this same value.

## Measured values

"Total" is conversion plus boot, computed from milliseconds and rounded up.

| Run | Job | Condition | CPU | Convert s | Boot s | Total s | Two-disk s |
|---|---|---|---|---|---|---|---|
| 1 | 131633599297 | Debian container, no KVM | EPYC 7763 | 235 | 50 | 285 | – |
| 2 | 131639055451 | Debian container, no KVM | EPYC 7763 | 218 | 48 | 266 | – |
| 3 | 131645092957 | Debian container, no KVM | EPYC 7763 | 212 | 48 | 259 | 212 |
| 2 | 131639055437 | Debian container with KVM | EPYC 9V74 | 42 | 41 | 82 | – |
| 3 | 131645092503 | Debian container with KVM | Xeon 8573C | 36 | 40 | 75 | 37 |
| 2 | 131639055414 | Ubuntu on runner, appliance on KVM | EPYC 9V45 | 65 | 34 | 98 | – |
| 3 | 131645093094 | Ubuntu on runner, appliance on KVM | EPYC 9V74 | 63 | 43 | 105 | 45 |
| 2 | 131639055068 | Ubuntu on runner, appliance on TCG | EPYC 7763 | 656 | 50 | 705 | – |
| 3 | 131645092994 | Ubuntu on runner, appliance on TCG | EPYC 7763 | 669 | 50 | 718 | 626 |
| 1 | 131633598904 | Ubuntu on runner, as root | EPYC 9V45 | failed after 19 s | – | – | – |
| 1 | 131633599279 | Ubuntu on runner, as root | EPYC 7763 | failed after 48 s | – | – | – |

Other measured values:

- **Smaller steps.** The fixture download took 5 to 7 s and the raw copy 2 to 3 s. Package install took 41 to 96 s; once the worker image is built, the test does not pay this.
- **Two-disk case (run 3).** It exited 0 in all four conditions. The appliance log shows both disks attached: sda at 3 GiB and sdb at 64 MiB.
- **Where the conversion time goes.** Most of it is the rebuild of the guest initramfs (`update-initramfs`), which runs inside the appliance.
  - Run 3, Debian with no KVM: 156.5 s of the 212 s conversion.
  - Run 2, Ubuntu on TCG: 550 s of the 656 s conversion.
  - Opening the source (building and starting the appliance) took about 21 s under TCG and 12 s under KVM.
- **Starting the appliance as root.**
  - On Ubuntu, without network it starts: 3 s with KVM, 12 s on TCG. With network it fails within 1 s with "passt exited with status 1".
  - In the Debian container, both start: 3 s and 9 s with KVM, 13 s and 19 s without KVM.

## Findings beyond the timing

1. **As root, conversion fails wherever `passt` is installed.**
   - `virt-v2v` always turns on the appliance network.
   - libguestfs 1.52 and later then starts `passt` if it is installed.
   - `passt` started as root drops to the user `nobody`. It then cannot write its PID file into the libguestfs temporary directory, which root owns. The log shows "PID file open: Permission denied" and then "libguestfs error: passt exited with status 1".
   - Run 1 hit this in both Ubuntu jobs. Ubuntu 24.04 installs `passt` along with libguestfs.
   - Debian 13 does not install `passt` with this package set. libguestfs then uses QEMU user networking, and root works.
   - As a non-root user, `passt` works (Ubuntu, runs 2 and 3).
2. **A non-root libguestfs needs to read the host kernel.** On the runner, `/boot/vmlinuz-*` has mode 0600, so the script set it to 0644. In the Debian container the kernel package installs it as 0644.
3. **`--device /dev/kvm` cuts the Debian conversion from about 220 s to about 40 s.** That flag is not `--privileged`. Root in the container can open the device.
4. **Ubuntu's packages are about three times slower than Debian 13's under TCG**: 656 to 669 s against 212 to 235 s. Almost all of the difference is the initramfs rebuild. The runner's own distribution is therefore a poor stand-in for the worker image.
5. **The hosted runner pool mixes CPUs**: AMD EPYC 7763, 9V45 and 9V74, and Intel Xeon 8573C. Every job without KVM ran on an EPYC 7763, the slowest of them for the TCG boot (47 to 50 s, against 34 to 43 s on the others). The no-KVM figures therefore sit at the slow end of the pool.

## What follows for the plan

- **Outcome D1.** MIG-112 keeps the hermetic test on every pull request, and its acceptance item for that test stays.
- **`virt_v2v_in_place: packaged`.** No source build is needed, so no tag is recorded. Debian 13's `virt-v2v` 2.6.0-1 and Ubuntu 24.04's 2.4.0-2build4 both ship `/usr/bin/virt-v2v-in-place`.
- **Expected test cost in the worker image without KVM.** Case 1 (fetch, raw copy, conversion, boot) takes about 4.5 minutes and case 2 about 3.5 minutes, so about 8 minutes in all, plus building the image. If `--device /dev/kvm` is added to the test's `docker run`, the total drops to about 2 minutes.
- **Root and `passt` do not mix.** If the test runs as root, the worker image must not install `passt`. The other choice is to run the test as a non-root user with a readable kernel. A plain Debian-stable install of the listed packages does not pull in `passt`; this was measured.
- **The worker image needs a kernel in `/boot` for supermin.** The spike installed `linux-image-amd64` explicitly. It did not test whether the listed packages pull in a kernel on their own.
- **MIG-G01.** The fixture fetch, the raw copy and the hash together cost well under a minute. If that path also runs a conversion on a runner without KVM, add about 4 minutes. The SHA-256 of the fixture converted to raw is stable across all 11 jobs (`7a4e0182…`).

## Evidence

- Run 1, commit 94bed7936: https://github.com/openova-io/openova/actions/runs/43348520958. Root on Ubuntu fails because of `passt`. Debian container with no KVM: 235 + 50 s.
- Run 2, commit edf9abc57: https://github.com/openova-io/openova/actions/runs/43350173222. All four conditions.
- Run 3, commit 9f21dc7b7: https://github.com/openova-io/openova/actions/runs/43352017295. All four conditions, plus the two-disk case.

Each job prints its result block under "===== MIG-050 RESULT =====". It also uploads `result.txt`, the conversion log, the stage stamps and the serial log as an artifact, kept for 30 days.

## Caveats

- **Run 2, Debian container with no KVM.** The script's KVM-open check created a plain file named `/dev/kvm` in the container, where the device does not exist. The appliance still ran on TCG: its kernel log shows no hypervisor. Run 3 fixed the check and gave a clean sample (212 + 48 s).
- **Run 2, `convert_variant` label.** The line says `nonroot` for the container jobs, but they ran as root, as `conv_user: root uid=0` shows. Run 3 fixed the label.
- **Not measured:**
  - A Windows guest (the virtio-win path).
  - The guest network-rename step and its `guestfish` checks.
  - The `vmcopy` wrapper, which is not built yet.

  Each extra appliance start for a `guestfish` check costs about 13 s without KVM, measured by the root probe.

## Evidence

Outcome: **D1**. The experiment is the branch `spike/MIG-050` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43348520958
- https://github.com/openova-io/openova/actions/runs/43350173222
- https://github.com/openova-io/openova/actions/runs/43352017295

## What later tasks must take into account

I measured everything the decision needs in CI; nothing is missing for D1.

Not measured, because the inputs do not exist yet or are out of scope for a Linux fixture:
- a Windows guest and the virtio-win path;
- the guest network-rename step and its guestfish checks;
- the not-yet-built vmcopy wrapper.

What later packages must take into account:

1. MIG-112, test as root. libguestfs starts `passt` for the appliance network, which virt-v2v always turns on. As root, `passt` drops to `nobody`, cannot write its PID file, and the conversion fails with "passt exited with status 1". Ubuntu 24.04 installs `passt` with libguestfs. A Debian 13 install of the listed packages does not, so root works there through QEMU user networking. If the worker image ever gains `passt`, the test must run as a non-root user, and that user needs a readable `/boot/vmlinuz` (mode 0600 on the runner).

2. MIG-112, timing. In the worker image without KVM, case 1 (convert plus boot) takes about 259–285 s and the two-disk libvirtxml case about 212 s, so about 8 minutes per test run. Adding `--device /dev/kvm` to the test's `docker run` cuts this to about 2 minutes. That flag is not `--privileged`, and hosted runners expose `/dev/kvm`.

3. MIG-112, kernel. The spike installed `linux-image-amd64` explicitly as the supermin appliance kernel. It did not test whether the listed packages pull a kernel in on their own, so keep it in the package list.

4. Where the time goes. The guest initramfs rebuild inside the appliance takes about 75% of a no-KVM conversion. Ubuntu's packages are about 3 times slower than Debian 13's under TCG (about 660 s against about 215 s). Do not use the runner's own distribution as a stand-in for the worker image.

5. MIG-G01. The fixture's raw SHA-256 is stable: 7a4e0182d5588c2e6c9ad656023b76e74f37444827f555c03d5d058cba4367a9. Fetch, raw copy and hash add well under a minute. Add about 4 minutes if the path also converts without KVM.

Budget used: 3 of the 5 allowed pushes. Branch spike/MIG-050 is left in place at 9f21dc7b7 as the evidence, with no pull request. Nothing was built or started on the host. Only small job artifacts were downloaded, into the session scratchpad.
