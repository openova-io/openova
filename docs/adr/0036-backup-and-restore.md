# ADR-0036 — Backup and restore: one native copy per data class, one bucket layout, one restore drill each

Status: Accepted
Date: 2026-11-13

Written by work package FND-181. It records how a Sovereign copies the state that cannot be rebuilt from Git or from images: what is copied, how often, how long it is kept, where it is stored, who may read it, and how each restore is proven. The decisions are B1 to B8. The packages of epic FND-180 and package SEC-180 build them.

Terms used here:

- **Data class**: one kind of state with its own copy mechanism. There are four: etcd, database, Git and OpenBao.
- **The bucket**: the Sovereign's object-storage bucket, created by the provider module. The first-boot template `infra/providers/_shared/cloudinit-control-plane.tftpl` receives its endpoint, region, name and key pair as the variables `object_storage_endpoint`, `object_storage_region`, `object_storage_bucket_name`, `object_storage_access_key` and `object_storage_secret_key`. ADR-0016 (K9) keeps infrastructure state in the same bucket.
- **`<region>`**: the canonical label of a region, the value that the bootstrap kit receives as `SOVEREIGN_REGION_CANONICAL_LABEL`. It is the region's cluster name.
- **Restore drill**: a gate check of `docs/ledger/gates.yaml` that restores the newest copy into a scratch place, compares the result with the source, and deletes the scratch place.

## Context

- Every control-plane server runs k3s with embedded etcd: `--cluster-init` is among the k3s server arguments of `infra/providers/_shared/cloudinit-control-plane.tftpl`. The template sets no snapshot schedule and no snapshot target. The reference cloud's module passes `--etcd-snapshot-retention=5` in its k3s arguments (`infra/providers/huawei/main.tf`). So each server keeps its five newest snapshots, taken on the k3s default schedule of every 12 hours, on its own disk. No snapshot leaves the node, and a lost node loses its snapshots.
- No chart under `platform/` renders a `ScheduledBackup` of the PostgreSQL operator, and the shared database chart `platform/postgres/chart` renders its database clusters with no backup section. The table "Backup Strategy" of `platform/cnpg/README.md` describes a daily base backup at 02:00, kept 30 days, and a continuous archive of the write-ahead log. Nothing renders it.
- Nothing bundles a Git repository. The Git server (`platform/gitea/chart`) holds the only copy of the fleet repository and of every Organization's repositories.
- The OpenBao chart (`platform/openbao/chart`) has a snapshot-replication job that is off by default. ADR-0018 (S14) deletes it and replaces it with a plain snapshot to object storage, built by SEC-180.
- Two cluster-backup charts exist, `platform/velero` (kit slot 34) and `platform/velero-hcs` (kit slot 34a). At most one of them reconciles on a Sovereign. Neither declares a schedule, so neither takes a backup.
- The policy `backup-configured` of `platform/kyverno-policies/chart` passes a volume claim that carries a label or an annotation naming a schedule. It does not check that the schedule exists or has run. Package CMP-111 replaces it with checks that read the schedule and backup objects.
- The gate manifest `docs/ledger/gates.yaml` holds four restore checks, all `not-built`: FND-G16 (etcd), FND-G17 (database) and FND-G18 (Git), delivered by FND-185, and SEC-G10 (OpenBao), delivered by SEC-180.

## Decision

The governing rule: **each data class is copied by its own native mechanism to the bucket, on the schedule of this decision, and each has one restore drill that is a gate check.** No single cluster-backup tool copies everything.

**B1 The four data classes.** One row per class. A schedule is a cron expression. Its interval is binding; its time of day is a default and follows the time zone of the component that runs it.

| Class | What is copied, and who writes the copy | Schedule | Retention | Path in the bucket | Who may read it | Restore drill |
|---|---|---|---|---|---|---|
| etcd | The embedded etcd of each region. Every control-plane server writes its own snapshot; k3s itself takes it and uploads it, uncompressed. | `0 */12 * * *`, k3s option `--etcd-snapshot-schedule-cron` | The 5 newest snapshots of each server, k3s option `--etcd-snapshot-retention` | `etcd/<region>/`, k3s option `--etcd-s3-folder`; file names as k3s writes them, with the server's node name and the time | The region's control-plane servers, with the key of B3; the drill FND-G16; the sovereign-admin in a restore | FND-G16, as B4 states |
| database | Every database cluster of the PostgreSQL operator that `platform/postgres/chart` renders. The operator writes a base backup on the schedule (one `ScheduledBackup` per cluster) and archives the write-ahead log continuously. | `0 0 2 * * *`: daily at 02:00, in the operator's six-field form with seconds first | A recovery window of 30 days, the cluster's `retentionPolicy: "30d"` | `database/<region>/<namespace>/<cluster>/`: the cluster's `destinationPath` is `s3://<bucket>/database/<region>/<namespace>`, where `<bucket>` is the bucket's name, and its server name is the cluster's name | The operator of that cluster; the drill FND-G17; the sovereign-admin in a restore | FND-G17, as B4 states |
| Git | Every repository of the Git server, as `git bundle create --all`. One CronJob in namespace `gitea` of the region whose Git server takes the writes. A run writes a bundle only for a repository whose default-branch head has no bundle yet. A repository with no commit has no bundle. | `*/15 * * * *` | The 10 newest bundles of each repository. A repository deleted from the Git server keeps its newest bundle. | `git/<owner>/<repository>/<head>.bundle`, where `<head>` is the full commit id of the default branch when the bundle was made | The bundle Job; the drill FND-G18; a rebuild that fills an empty Git server (B5); the sovereign-admin | FND-G18, as B4 states |
| OpenBao | The raft store of each region's OpenBao, read from `sys/storage/raft/snapshot`. CronJob `openbao-backup` in namespace `openbao` writes it; it logs in to OpenBao with the role `openbao-snapshot`. | `17 */6 * * *` | 30 days. The Job deletes older objects under its own prefix. | `openbao/<region>/<time>.snap`, where `<time>` is the UTC time of the snapshot in the form `YYYYMMDDTHHMMSSZ` | The role `openbao-snapshot`; the drill SEC-G10; the sovereign-admin in a restore. A snapshot is of no use without the region's seal key. | SEC-G10, as B4 states |

The numbers come from what exists. The etcd numbers are what the reference cloud runs today: the k3s default interval and the retention its module passes. The database numbers are those that `platform/cnpg/README.md` describes. Git is copied every 15 minutes, so that losing every copy of the Git server loses at most 15 minutes of commits to a default branch. The OpenBao numbers are the ones SEC-180 builds: a snapshot every 6 hours, so its drill SEC-G10 looks for one younger than 7 hours.

**B2 One bucket, one layout.** The four prefixes `etcd/`, `database/`, `git/` and `openbao/` at the root of the bucket belong to this decision. No other writer writes under them. A path of B1 is a key relative to the root of the bucket. The database, Git and OpenBao writers run inside the cluster. When the object-storage binding of ADR-0020 (S9) serves platform consumers, they get their bucket and key through it, and the layout of B1 stays the same in that bucket. The etcd snapshots are written by the servers before any binding exists, and they stay in the Sovereign's bucket.

**B3 How each writer gets the key.** etcd: the first-boot template writes the bucket's key pair into a k3s configuration file on each control-plane server, readable by root only (mode `0600`). The key is never a command-line argument and never a Secret object. Database: the operator accepts a static object-store key only as a Secret reference, so that Secret is a class K entry of the exception register (ADR-0018, S17). Git and OpenBao: each Job is a platform workload and receives the key under the rules of ADR-0018: a mounted file, or a registered exception where the mount is not yet available in its namespace.

**B4 The restore drills.** Each drill is a gate check that runs on the standing environment. No drill restores over live data, and each deletes what it created.

- etcd (FND-G16): the newest snapshot of the region's servers is younger than 24 hours, twice the interval. It is restored into a scratch single-member etcd in a Pod of a scratch namespace, never into the running cluster. Every namespace that the live cluster lists and that was created before the snapshot was taken is present under `/registry/namespaces/` in the restored store. The scratch namespace is then deleted. The drill changes nothing in the running cluster.
- database (FND-G17): the newest completed scheduled backup of one platform database cluster is younger than 48 hours, twice the interval. It is restored, with its archived write-ahead log, into a scratch cluster in a scratch namespace. A row that existed before that backup started is read there. The scratch namespace is then deleted.
- Git (FND-G18): the newest bundle of one repository is cloned into an empty scratch repository. Its default-branch head equals the commit id in the bundle's file name. The scratch repository is then deleted.
- OpenBao (SEC-G10): a snapshot younger than 7 hours exists under the region's prefix. It is restored into a throwaway OpenBao in a scratch namespace that uses the same seal, one known key is read from it, and the throwaway OpenBao is deleted.

**B5 The Git bundles fill an empty Git server.** A rebuild fills an empty Git server from the newest bundle of each repository, and it needs no input other than the bucket. That is the rebuild drill of CTL-610. A bundle is not encrypted with a key of its own. The bucket's key is the access boundary for Git bundles and for database backups. An etcd snapshot holds its Secret objects encrypted with the region's etcd key (ADR-0018, S5), and an OpenBao snapshot is encrypted under OpenBao's seal.

**B6 Recovery point of a lost copy.** When a region's data and the copies on its nodes are lost, the copies in the bucket lose at most: 12 hours of etcd; for a database, what the write-ahead log archive had not yet received; 15 minutes of commits to a default branch; 6 hours of OpenBao. The loss of one region while its peer region survives is the subject of record 0027, allocated to CTL-800.

**B7 Defaults for Blueprints and Organizations.** A Blueprint's state is copied only through the four classes. Every database cluster that `platform/postgres/chart` renders gets the database row's schedule and retention as default values, whichever Organization installs it; an installation may change them through the chart's values. An Organization's repositories are copied by the Git row. A volume that belongs to none of the four classes has no default schedule, and the checks of CMP-111 show it without a schedule, which is the truth. The cluster-backup charts of kit slots 34 and 34a stay as they are; this decision adds no schedule to them.

**B8 Owners.** FND-182 builds the etcd row, FND-183 the database row and FND-184 the Git row. SEC-180 builds the OpenBao row and its drill SEC-G10. FND-185 builds the drills FND-G16, FND-G17 and FND-G18. A change of a default schedule, a retention or a path of B1 is made by a new decision record.

## Alternatives rejected

**B1.** (a) One cluster-backup tool for all four classes, the charts of kit slots 34 and 34a. A copy of cluster objects restores neither etcd as a whole, nor a database to a consistent point, nor OpenBao's raft store. (b) Restore drills done by hand once a quarter. A drill done by hand leaves no gate record; a drill is a gate check. (c) A shorter interval for etcd. Each server would write more snapshots for a store whose content is also declared in Git, and nothing measured asks for it.

**B2.** (a) One bucket for each class now. No mechanism gives a platform consumer its own bucket yet, and a layout of prefixes moves unchanged into such a bucket later. (b) Paths without the region. Each region has its own etcd and its own OpenBao, and a database cluster's name is chosen in each region; nothing would keep the copies of two regions apart.

**B3.** (a) The etcd key in a k3s configuration Secret. k3s reads the key from a file, so the class K reason of the register does not apply, and the key would sit in every etcd snapshot that it protects. (b) The key on the k3s command line. It would be visible in the process list of the node.

**B4.** (a) Restoring etcd in place on the gate environment as the last step before a wipe. The gate runs against the standing environment, which is updated in place and not wiped after each run (FND-073). An in-place restore would turn that environment back to the time of the snapshot. (b) Checking only the size or the checksum of a copy. That proves that a file exists, not that it restores.

**B5.** (a) Encrypting each bundle to a key that only the sovereign-admin holds offline. Every rebuild would then need that key as one more input, and Git holds no secret value (ADR-0018, S12). (b) Bundling only the fleet repository. An Organization's repository is its own system of record as well.

## Consequences

- A region's etcd, its platform databases, every repository and each region's OpenBao have a copy outside the nodes once FND-182, FND-183, FND-184 and SEC-180 have merged.
- The drill FND-G16 does not change the running cluster.
- The policy `backup-configured` stays until CMP-111 replaces it.
- A database backup and a Git bundle can be read by anyone who holds the bucket's key.
- A restore of a whole region outside a drill uses the same copies. This decision does not describe that procedure.

## Shipped via

FND-181 writes this decision record. In epic FND-180: FND-182 (etcd), FND-183 (database), FND-184 (Git) and FND-185 (the drills FND-G16, FND-G17 and FND-G18). SEC-180 builds the OpenBao row and SEC-G10. CMP-111 reads the schedules. CTL-610 uses the Git bundles.
