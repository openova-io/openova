outcome: B2
boot_mode: image-from-volume
volume_mode: not measured
max_disks_per_worker: 23
firmware: bios, efi
fixed_mac: false
windows_boot: not-run
landing_apply_seconds: 1004

# MIG-030 Lab test: a written volume boots on the reference cloud (result)

Dates: 2026-11-13 (run 1) and 2026-11-14 (run 2). Type: lab test. Time used in run 2: 55 minutes of a 75-minute box. Result: partial. Outcome **B2**: the boot mode is `image-from-volume`, and the landing design stands. One limit is crossed: the landing apply took more than 600 s for a 100 GiB system volume.

The header lines above are the result. `volume_mode` was not measured, because tests 1 to 3 need a cluster with the storage driver of the reference cloud's provider pack (see "Still to run").

## Question

Can a volume that a worker has written become a running server on the reference cloud, through the OpenTofu module of its provider pack? Which boot mode works? Does it work for a UEFI guest and for a large, full volume? How many volumes can one worker hold? Can a port carry a hardware address that the caller chooses? How long does one landing apply take?

Decision rule: `boot_mode` is the first of `image-from-volume`, `swap-system-disk` and `image-import` that passes for a Linux guest.

- B2: `image-from-volume` passes. The landing design stands.
- B1: only `swap-system-disk` passes.
- B3: only `image-import` passes.
- B4: none passes. The route `cloud-server` is not offered on this cloud.

## What was run

- **Cloud.** The reference cloud, in the project and the region of the reference environment. The region has one zone.
- **Tools.** OpenTofu 1.12.0. Provider huaweicloud/huaweicloud 1.100.0. On 2026-11-17 the module in `infra/providers/huawei` asked for `~> 1.65`, and its lock file pinned 1.91.0. Endpoint overrides as in `infra/providers/huawei/versions.tf`. Read checks and test 10 used signed REST calls.
- **Scratch modules.** A base module made the network, the worker stand-in and the volumes. A separate landing module made the image and the landed server; it was the timed run. Every name started with `nslab-mig-030-`.
- **Network.** A scratch VPC with one subnet. One security group allowed SSH from the lab machine only, plus traffic inside the subnet.
- **Worker stand-in.** A server of flavor m7n.large.8, the same family as the module's default worker flavor on 2026-11-17 (`m7n.xlarge.8`), with the Ubuntu 22.04 public image. No Sovereign cluster was used, so the storage driver was not part of either run.
- **Writing (run 1).** The worker copied a 40 GiB disk raw and sparse onto a blank volume.
- **Writing (run 2).** The worker wrote two volumes. (a) A 100 GiB Linux system volume: an Ubuntu system with a marker file and 82 GiB of random data, 86 GiB used in all. The worker copied it raw onto a blank volume. (b) A 10 GiB UEFI volume: the upstream Ubuntu 22.04 cloud image (GPT, with a BIOS boot partition and an EFI system partition), converted raw onto a blank volume.
- **Landing.** Plain `tofu` on the lab machine, not a unit Workspace.

## Results

| # | What | Verdict | Measured |
|---|---|---|---|
| 1-3 | Raw block claim, release and re-use, wait-for-first-consumer | not run | Each needs a cluster with the provider pack's storage driver. Making one needs the cloud credential inside the cluster, which the rules of these runs did not allow. |
| 4 | Mode image-from-volume | pass | Run 1: a 40 GiB sparse copy became an image in 212 s and booted. Run 2: a worker-written 100 GiB volume with 86 GiB of data became an image in 936 s (created to active). That is 9.4 s per GiB of volume size and 11.0 s per GiB of stored data (image size 84.7 GiB). The landed server booted on the fixed address. It had the new host name and the marker. Two 1 GiB sample checksums, at the start of the data and at 78 GiB, matched the worker's. |
| 4 | Volume attached to a running server | pass | Run 2 made both images while the volume was attached to the running worker. Run 1 had shown that a detached volume is refused with `IMG.0120`. |
| 4 | Volume made from a cloud image | refused | A volume created from the public image carries that image's metadata. Imaging it was refused: `IMG.0079` "The system disk image created from a charged image cannot be exported." A blank volume written by the worker was accepted. |
| 5 | Mode swap-system-disk | not run | Not needed, because test 4 passed. |
| 6 | UEFI guest | pass, with one direct call | The image from the 10 GiB UEFI volume took 163 s. It came out with firmware `bios`. The provider refuses `hw_firmware_type` on `huaweicloud_ims_evs_system_image`: the attribute is computed only ("Value for unconfigurable attribute"). One IMS image update call set the firmware to `uefi` (HTTP 200). The server from that image started in UEFI mode. It answered SSH on its fixed address about 15 s after create, and the marker was present. |
| 7 | Windows guest | not run | No Windows image and no licence were available. The region's public image list has 10 images, all Linux. |
| 8 | Server on the pre-made port | fail as specified; two alternatives work (run 1) | The provider refuses `network { port }` without `uuid`, with the error "network ID should not be empty". With both set, the port was ignored: the server got a new port with an address the cloud chose, and the pre-made port stayed DOWN with no device. Alternative (a): attach the port after create (two cards, hardware address kept, address by DHCP). Alternative (b): fix the address at create with `fixed_ip_v4` (one card, hardware address assigned at create). Run 2 used (b) for both landed servers. |
| 9 | Hardware address chosen by the caller | refused (run 1) | HTTP 400, NeutronError "Attribute 'mac_address' cannot be set." |
| 10 | Volumes per worker until refused | measured | The flavor declares 24 VBD disks, 60 SCSI disks and 60 in all. The worker held its system disk and 3 data volumes. 20 more attached, 4-6 s each by REST, for 24 disks in all. The 25th was refused: HTTP 400 `Ecs.0005` "reach the upper limit". That leaves 23 data volumes beside the system disk, with the default VBD disk type. SCSI disks were not tried. |
| 11 | Conversion time on a worker node | not run | Needs a Sovereign worker node. For reference, the stand-in worker converted a 2.2 GiB cloud image to raw onto a volume in 21 s. |
| 12 | Data volume re-used without a copy | pass, cloud level (run 1) | Detach 20 s, attach 21 s, marker unchanged. |
| 13 | Server in an isolated subnet, address assigned by the cloud | pass (run 1) | Three servers were made this way. |
| 14 | Landing through a unit Workspace | partial | No Workspace was run. Plain `tofu` on the 100 GiB volume: init 4 s, plan 6 s, apply 952 s. The image step took 940 s. The server step then failed on a local name lookup, and a second apply made the server in 32 s (run 42 s). Wall total 1004 s. Without the retry: 4 + 6 + 940 + 32 = 982 s. Run 1, on a 40 GiB sparse copy: 291 s in two runs. The Observe policy and the deletion behaviour were not measured. |

Other measured facts:
- Worker write rates on 100 GiB SSD volumes: an 82 GiB fill took 493 s, and a raw volume-to-volume copy of 100 GiB took 602 s. Both are about 178 MB/s.
- The provider's volume attach took 20-21 s, against 4-6 s for the same call over REST. The provider's waits, not the cloud, set that time.
- Server create took 32-38 s and server delete 45 s. A volume made from an image took 16 s. A blank volume took 15-18 s.
- Images made from volumes carry the disk format `zvhd2`, the virtual environment `FusionCompute` and the firmware `bios`.
- The compute API's console-log call works on this cloud, which helps boot checks.
- Name lookups of the cloud endpoints on the lab machine fail when many happen at once ("no such host"). The runs used parallelism 2-4 and retries.
- The project's VPC quota is 5, and other labs filled it. Run 1 waited 9 minutes for a free slot.

## Decision

Outcome B2. The boot mode is `image-from-volume`. A written Linux volume becomes a bootable server through the module, for BIOS and, with one image update call, for UEFI. `fixed_mac: false`. `max_disks_per_worker: 23`.

The landing time is over the 600 s line for a 100 GiB volume with 86 GiB of data. The decision taken on it:

- `image-from-volume` stays the default boot mode, because it is the mode that is proven.
- The landing time is stated per GiB in the output of a migration plan (11 s per GiB of stored data), not promised as a fixed number.
- The mode `swap-system-disk` gets its own lab run (test 5). It replaces the default only if it is measured faster and boots both firmware types.
- Workers write onto blank volumes only.
- A UEFI guest needs one image update call after the image is made, because the provider cannot set the firmware.

## What follows for the plan

- **Landing time.** Image creation is almost all of the landing time. It grows with the data on the system volume: about 11 s per GiB stored here, and 5.3 s per GiB on a sparse copy. OpenTofu itself adds about 10 s (init 4 s, plan 6 s). Direct cloud calls would not shorten the image step. The mode `swap-system-disk` skips image creation, so it is the only measured lever.
- **Landing order.** Make the image while the written volume is still attached to the worker node; that works on a running server. Do not detach first (run 1, `IMG.0120`). No stopped holder server is needed. This applies to MIG-120 and MIG-131.
- **Blank volumes only.** The worker must write onto blank volumes. A volume made from a cloud image cannot be imaged (`IMG.0079`).
- **UEFI.** With provider 1.100.0, the module cannot set the image firmware. MIG-131 needs one of these: a provider version that accepts `hw_firmware_type` on this resource; one narrow IMS image update call; or `firmware: bios` only on this cloud.
- **Disks per worker.** 24 VBD disks per server, system disk included. Volumes that pods already use on a node count against this. The disk type of the provider pack's storage driver decides the limit; SCSI would allow up to 60 (flavor value, not tested).
- **Port.** Either two cards (port attached after create) or one card with the address fixed at create and the hardware address learned after create. No landing variable for a hardware address is added, because the cloud refuses a caller-chosen one.
- **Still to run.** Tests 1-3, 11 and the Workspace part of 14 (the Observe policy and the deletion behaviour), on a scratch cluster with the provider pack's storage driver and Crossplane. That needs a scoped cloud credential handed to the cluster through the gate's secret path. Test 7 needs a Windows image and a licence; until it passes, `windows_boot` stays `not-run`.
- **Lab capacity.** Lab tests that need their own network compete for the 5 VPC slots of the project. Book the slots first, use low parallelism with retries for endpoint lookups, and keep free disk space on the lab machine for provider caches.

## Evidence

- Run 1, 2026-11-13, 06:49 to 07:28 UTC. Run 2, 2026-11-14. OpenTofu 1.12.0, huaweicloud provider 1.100.0.
- Run 1: VPC create refused with "conflict" at 06:49 UTC, because the VPC quota was full; a slot was free at 06:59 UTC.
- Run 1: image from a detached blank volume refused twice with `IMG.0120` "The specified data disk ID is unavailable."
- Run 1: the 40 GiB sparse copy took 336 s inside the worker. The image from the written volume took 212 s from created to active. The two timed landing runs took 231 s (init 1 s, plan 5 s, apply 225 s) and 60 s (init 1 s, plan 4 s, apply 55 s).
- Run 1: a port with a caller-chosen hardware address refused with HTTP 400, NeutronError "Attribute 'mac_address' cannot be set."
- Run 1: the landed server, checked over SSH, had the new host name, the marker file and the data-volume marker, firmware `bios`, and its card's address equal to its port's address. The pre-made port attached as a second card in 12 s kept its hardware address; the guest saw it as `eth1` and got the fixed address by DHCP.
- Run 2: image from the 100 GiB volume with 86 GiB of data, 936 s from created to active, image size 84.7 GiB. Landing apply wall total 1004 s.
- Run 2: two 1 GiB sample checksums of the landed volume matched the worker's.
- Run 2: image of a volume made from the public image refused with `IMG.0079`.
- Run 2: the UEFI image came out with firmware `bios`; one IMS image update call (HTTP 200) set `uefi`, and the server started in UEFI mode.
- Run 2: the 25th disk of the worker refused with HTTP 400 `Ecs.0005` "reach the upper limit".
- Deletion: each deletion of both runs was checked by a read call. Server, volume, image, VPC, subnet, security group and bandwidth listings with the lab prefix came back empty, and the key pairs read 404.

## Appendix: module resource blocks of the passing mode

Placeholders stand for every identifier.

```hcl
resource "huaweicloud_ims_evs_system_image" "landing" {
  name       = "<prefix>-landing-image"
  volume_id  = "<written-volume-id>"     # blank volume written by the worker, still attached
  os_version = "<os-version-string>"     # for example "Ubuntu 22.04 server 64bit"
  # hw_firmware_type cannot be set here with provider 1.100.0 (computed only)
}

resource "huaweicloud_compute_instance" "landed" {
  name               = "<prefix>-landed"
  image_id           = huaweicloud_ims_evs_system_image.landing.id
  flavor_id          = "<flavor>"
  key_pair           = "<key-pair-name>"
  security_group_ids = ["<security-group-id>"]
  availability_zone  = "<zone>"
  system_disk_type   = "SSD"
  system_disk_size   = <size-of-written-volume-GiB>
  network {
    uuid        = "<subnet-id>"
    fixed_ip_v4 = "<fixed-address>"
  }
}
```

## Resources of the run

Run 1:
- vpc nslab-mig-030-ca5b502a-vpc: deleted
- subnet nslab-mig-030-ca5b502a-subnet: deleted
- security group nslab-mig-030-ca5b502a-sg (with 2 ingress rules, ssh and icmp): deleted
- key pair nslab-mig-030-ca5b502a-key: deleted
- port nslab-mig-030-ca5b502a-landing-port: deleted
- port nslab-mig-030-ca5b502a-macport: never created (refused by the cloud), absent
- server nslab-mig-030-ca5b502a-writer (with its system disk and port): deleted
- server nslab-mig-030-ca5b502a-landed, first instance (with its system disk and port): deleted
- server nslab-mig-030-ca5b502a-landed, second instance (with its system disk and port): deleted
- server nslab-mig-030-ca5b502a-fixed (with its system disk and port): deleted
- volume nslab-mig-030-ca5b502a-sysvol (40 GiB): deleted
- volume nslab-mig-030-ca5b502a-datavol (10 GiB): deleted
- image nslab-mig-030-ca5b502a-image: deleted
- image nslab-mig-030-ca5b502a-probe-image: never created (refused `IMG.0120` twice), absent
- elastic IP nslab-mig-030-ca5b502a-eip with bandwidth nslab-mig-030-ca5b502a-bw: deleted

Run 2:
- vpc nslab-mig-030-a7e231-vpc: deleted
- subnet nslab-mig-030-a7e231-subnet: deleted
- security group nslab-mig-030-a7e231-sg (with 3 rules: ssh, intra-subnet, egress): deleted
- key pair nslab-mig-030-a7e231-key: deleted
- elastic IP with bandwidth nslab-mig-030-a7e231-bw: deleted
- server nslab-mig-030-a7e231-worker (with its system disk and port): deleted
- server nslab-mig-030-a7e231-uefi-landed (with its system disk and port): deleted
- server nslab-mig-030-a7e231-landed (with its system disk and port): deleted
- volume nslab-mig-030-a7e231-sysvol (100 GiB, made from the public image): deleted
- volume nslab-mig-030-a7e231-sysvol2 (100 GiB, blank, worker-written): deleted
- volume nslab-mig-030-a7e231-uefivol (10 GiB): deleted
- volumes nslab-mig-030-a7e231-t10-0 to -23 (24 x 10 GiB): deleted
- image nslab-mig-030-a7e231-uefi-image: deleted
- image nslab-mig-030-a7e231-landing-image: deleted
- image from the image-made volume: never created (refused `IMG.0079`), absent
- 20 volume attachments of test 10 (REST): detached

The working folders of both runs were deleted.
