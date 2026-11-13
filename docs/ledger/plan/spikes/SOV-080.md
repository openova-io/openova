# SOV-080: whole-state move, encrypted object-store state, zero-change plan, offline restore (lab test result)

Date: 2026-11-13. Type: lab test on the reference cloud. Outcome: **no**.

Rows: E1 yes, E2 yes, E3 not run, E4 no, E5 no, E6 yes, E7 yes.

## Question

Can the bootstrap state move whole into the reference cloud's object store, encrypted with an OpenBao Transit key? Then:
- does a plan of the bootstrap module with the bootstrap inputs show zero changes,
- does it show zero changes with a scoped key,
- does it finish inside the provider's run timeout,
- can the state be read back with only the offline backup of the key?

Seven rows:
- E1: an encrypted push to an S3-compatible store.
- E2: the same push to the reference cloud's object store.
- E3: an observe-only Workspace.
- E4: a zero-change plan with the real bootstrap state.
- E5: the same plan with the scoped key.
- E6: the plan's duration.
- E7: the offline restore.

## What was run

- **Where.** The test ran from an operator machine outside every cluster, against the reference cloud (Huawei Cloud Stack), in the project and the region of an existing two-region test Sovereign. It ran from 07:13 to 07:37 UTC.
- **Tools.** OpenTofu 1.12.0, OpenBao 2.1.0 (the platform's pin), SeaweedFS 4.22, Python boto3 1.43 and the Huawei Cloud Python SDK.
- **Encryption.** `TF_ENCRYPTION` used the form fixed by CLD-050:
  - key provider `openbao` with block name `sovereign`, Transit key `tofu-state`;
  - method `aes_gcm`;
  - state and plan encryption both enforced.
- **Test OpenBao.** A dev-mode server, in memory. The key was created with `exportable=true` and `allow_plaintext_backup=true`. A backup of 1,948 bytes was taken at once.
- **Back end.** The S3 back end used path-style addressing and the four `skip_*` flags. The region and endpoint came from environment variables, and so did the credentials. Nothing secret was written to disk.
- **Lab bucket.** `nslab-sov-080-state-cbf63a`, created for this test and deleted at the end.
- **Bootstrap module.** It was identified exactly. All six files in the provisioning working directory match public commit 762402db21 (`infra/providers/huawei/*` plus the shared control-plane template, unchanged since 893a4ff905). The run's own lock file was used: huaweicloud 1.98.0, aws 5.100.0, time 0.14.1.
- **Bootstrap inputs.** All inputs of the bootstrap run were passed as `TF_VAR_*` environment variables.
- **Bootstrap state.** 222,986 bytes, 53 resources, 154 instances, serial 15, written by OpenTofu 1.11.6. It was streamed from the provisioning working directory into `tofu state push -`, so no plain copy was written to disk.
- **Plans.** Every plan used `-detailed-exitcode -lock=false`. Plans are read-only, and nothing in the running Sovereign was changed.

## Results

| Row | Result | Measured |
|---|---|---|
| E1 | yes | Local SeaweedFS 4.22: push exit 0. Stored object of 1,104 bytes with top-level keys `encrypted_data`, `encryption_version`, `lineage`, `meta`, `serial`. No `resources` key. The canary resource name, its value and the type name `terraform_data` are absent from the stored bytes. Pull returned 1 resource. |
| E2 | yes | Reference cloud's object store. Toy push exit 0 in 0.11 s, ciphertext as in E1. Real bootstrap state push exit 0 in 3.7 s: 297,688 bytes stored, ciphertext. `huaweicloud_compute_instance`, `huaweicloud_obs_bucket`, `control_plane` and `"resources"` are absent from the stored bytes. `meta` holds one key, `key_provider.openbao.sovereign`. Pull returned 53 resources and 154 instances. No checksum setting was needed. |
| E3 | not run | Needs a Kubernetes cluster with Crossplane 2 and provider-opentofu. See "What is left". |
| E4 | no | Bootstrap module 762402db21, bootstrap inputs, real state: exit 2, "2 to add, 0 to change, 0 to destroy". Both planned changes are listed below. |
| E5 | no | Stand-in scoped identity with Tenant Administrator (`te_admin`) on the project only. Back end with the scoped key: state read refused (S3 HeadObject 403 Forbidden). Providers with the scoped key: plan exit 1, bucket read refused (403 AccessDenied). |
| E6 | yes | Plan 14.6 to 19.2 s against the default run timeout of 20 min (provider-opentofu v1.1.3 and v1.1.11; the poll is 10 min). |
| E7 | yes | Test OpenBao stopped. Key backup restored into a scratch OpenBao as `tofu-state`. `tofu init` plus `tofu state pull` succeeded in 6.4 s with 53 resources and 154 instances, equal to the source. |

### E4: every planned change (bootstrap module at its own revision)

1. `huaweicloud_vpc_eip.nat["<region>-b"]`, create. The NAT elastic IP of the second region was deleted outside OpenTofu. Refresh reported "Resource not found".
2. `huaweicloud_nat_snat_rule.region["<region>-b"]`, create. This rule is absent from the bootstrap state itself. A plan without refresh shows it alone: exit 2, "1 to add" in 5.6 s.

Neither change is a module or input defect, so neither can become an `ignore_changes` entry. No existing resource would be changed or replaced.

Refresh found drift on 41 objects that produce no planned change:
- 11 instances: the `volume_attached` blocks changed, because data volumes were attached after boot.
- 28 load-balancer members: `weight` changed.
- The Sovereign's bucket: object counters changed.
- The deleted elastic IP.

The test Sovereign was about ten weeks old and two-region, not a fresh single-region spike environment.

### Extra: the same state with today's module (origin/main 14c335d6e)

Exit 2, "30 to add, 12 to change, 12 to destroy":
- `user_data` updated in place on all 12 nodes (2 control planes, 10 workers);
- 12 security-group rules destroyed and 24 created, because the rules are re-keyed;
- 4 new `random_password` k3s tokens;
- the two creates above.

The lock file on main installs huaweicloud 1.91.0. The bootstrap run used 1.98.0. The older provider read the state without error.

### E5 detail

SOV-070 has no key yet, so a stand-in was created and deleted:
- an IAM group with the role `te_admin` granted on the project only;
- one user in that group;
- one access key.

The bucket refusals are in the table above. With the providers on the scoped key there was one more effect. The SSH key pair was "Resource not found", because key pairs belong to the IAM user that created them. The plan wanted to create a new key pair and update both control-plane nodes in place.

### E6 detail

- Unconstrained plans: 19.2, 16.8 and 17.4 s. Refresh-only: 15.1 s.
- Inside a 512 MiB memory cap, which is the provider pod's limit: 16.8, 16.4 and 16.0 s with exit 2, as unconstrained. A fourth run ended with exit 1 in 14.6 s. Its error was not captured.
- At the cap the cgroup was throttled 975 to 2,333 times. There was no OOM kill.
- CPU per plan: 7.4 to 8.2 s (user plus system). At the pod's 0.5 CPU limit this means at least about 16 s.
- Largest single process: 304 to 319 MiB.
- The first `tofu init` took 42 s, downloading three providers from the public registry.

### E7 detail

- With the test OpenBao stopped and no scratch server: init failed with "Unable to fetch encryption key data".
- A different Transit key in the scratch server: pull failed with OpenBao 400 "failed to decrypt ciphertext".
- A plan with the restored key ran in 17.4 s with exit 2 and the same two creates.

## Other measured facts

- **Lineage on first push.** The first `tofu state push` into an empty key gives the remote state a new lineage and serial 1. This held for:
  - encrypted and plain pushes;
  - a path and stdin;
  - with and without `-force`;
  - the reference object store and SeaweedFS.

  A second push of the same file is refused: "cannot import state with lineage X over unrelated state with lineage Y". With `-force` the second push takes the source lineage, at serial 2. This is OpenTofu 1.12.0 behaviour, not the store's.
- **No locking on the reference object store.** A PUT with `If-None-Match: *` on an existing key answered 200 and overwrote the object. SeaweedFS answered 412. With `use_lockfile = true` and another writer's lock object present, `tofu plan` went ahead ("No changes", exit 0), and afterwards the other writer's lock object was gone. A lock taken with no rival present was created and released normally.
- **Plans write nothing.** Nine plans ran against the pushed state. The state object kept the last-modified time and ETag of the push. provider-opentofu's Observe step runs the same command: `tofu plan -detailed-exitcode -lock=false`, then `tofu state list` and `tofu output -json` (source of v1.1.3 and v1.1.11, which are identical there).
- **Certificates.** The object store's certificate chains to a public root and verified with the system trust store. So did the compute, network, load-balancer and identity endpoints. No private certificate authority bundle was needed from outside the cloud. The object store's certificate is valid until 2026-11-26.
- **Identity endpoint.** The `iam` host named in the module's endpoint map answered 404 `APIGW.0101` ("The API does not exist or has not been published") for all eight Keystone and v3.0 paths tried. The same calls work on the host whose name starts with `iam-pub`. The provisioning key belongs to the account's admin group: it can list users and create users, groups, grants and access keys.
- **Versioning.** Versioning on a new bucket is off.

## Decision

E1, E2 and E7 are `yes`. E4 is `no`. SOV-410 needs E1, E2 and E4 `yes`, so the outcome is **no**.

E6 is `yes`. E5 is `no` with a project-scoped Tenant Administrator. E3 was not run.

## What follows for the plan

1. **SOV-410 does not start.** E4's two changes come from a resource deleted outside OpenTofu and from a bootstrap state that already lacked a resource. The decision rule's remedy (`ignore_changes` or an input fix) does not apply to either. Re-run E4 on a fresh single-region spike environment from lane A, right after its bootstrap. On such an environment both causes are absent. Also decide what the gate does with drift on a long-running Sovereign.
2. **The plan gate at the released revision will fail on older Sovereigns.** Before it can pass on a Sovereign bootstrapped from an earlier revision, the cloud area's module package needs:
   - `user_data` (boot data) in `ignore_changes`;
   - stable keys or `moved` blocks for the security-group rules;
   - a decision on the k3s token resources;
   - a pack lock file that does not downgrade the provider used at bootstrap.
3. **E5 no feeds SOV-070 and the identity profile.** The Sovereign's identity needs object-store rights on the state bucket and on the module's own bucket. A project-scoped role does not give them. The SSH key pair must be owned by that identity or left out of the base Workspace's comparison, because key pairs are per user.
4. **No state locking on this object store.** OpenTofu's S3 lock file gives no mutual exclusion here, and a second writer deletes the first writer's lock. Contract C6 must not rely on it. Keep exactly one writer, the provider pod. Use `-lock=false` only for read-only plans. Run no outside apply (decommission) while the Workspace is managed. Report this to the cloud area.
5. **State-push idempotency.** SOV-410 must read back and record the lineage after its first push. Later runs compare against that lineage, not against the custody bundle's.
6. **E6 margin.** A plan takes well under the 20-minute timeout. Memory does not have the same margin: the plan sits at the provider pod's 512 MiB cap. The cloud area should raise that limit before the base Workspace exists.
7. **E7 yes: SOV-440 may proceed.** Keep the key-provider block name and both backup flags on the key, as CLD-050 found.
8. **The identity endpoint.** Identity calls must use the `iam-pub` host. The module's `iam` endpoint entry would fail for any identity resource.

## What is left

- **E3.** It needs a Kubernetes cluster with Crossplane 2 and provider-opentofu v1.1.11, the state in the reference object store and a key with object-store rights. The test machine has no container runtime. CLD-050 measured observe-only behaviour in CI with a non-cloud resource.

  From the provider source: while the plan exits 2, an Observe-only Workspace reports Synced but never Ready. If the plan exits 1, as with E5's key, the Workspace becomes Synced=False. So the 30-minute Synced-and-Ready wait in `workspace-commit` would time out on the drift measured in E4.
- **E4 on a fresh single-region spike environment** (lane A).
- **E5 with SOV-070's real scoped key.**
- **The capped plan that ended with exit 1** needs a repeat with its error captured.

## Evidence

Outcome: **no**.

- The test ran on 2026-11-13 from 07:13 to 07:37 UTC against the reference cloud. Every value above was read from command output during the run.
- Public commits used: 762402db21 (bootstrap module) and 14c335d6e (origin/main).
- Cloud resources created, all deleted and checked by a read:
  - bucket `nslab-sov-080-state-cbf63a` and its 5 objects: HEAD answers 404, and the bucket count is back to its prior value;
  - IAM user `nslab-sov-080-scoped` with group `nslab-sov-080-scoped-group` and its project role grant: a name lookup finds 0 users and 0 groups, and the user count is back to 4;
  - the user's 2 access keys.
- The local working folder, the two in-memory OpenBao servers and the SeaweedFS server were removed after the test.
- No cluster was written to.
- No artifacts were kept, because the test rules require the working folder to be deleted.

## Evidence

Outcome: **no**. The measurements were made on the reference cloud on 13 November 2026.

- 2026-11-13T07:17Z E2: toy state pushed to the reference cloud's object store, encrypted with a Transit key in a test OpenBao 2.1.0 (OpenTofu 1.12.0)
- 2026-11-13T07:18Z extra: object-store conditional write (If-None-Match) and OpenTofu use_lockfile against a held lock
- 2026-11-13T07:19Z extra: lineage after the first push, measured on 4 pushes (encrypted, plain, path, stdin, with and without -force)
- 2026-11-13T07:20Z E4: real bootstrap state of the existing two-region test Sovereign pushed unsplit and encrypted (53 resources, 154 instances)
- 2026-11-13T07:21Z E4/E6: plan of the bootstrap module at 762402db21 with the bootstrap inputs: exit 2, 19.2 s (repeat 16.8 s; refresh-only 15.1 s; no refresh 5.6 s)
- 2026-11-13T07:23Z extra: plan of the module at origin/main 14c335d6e against the same state: exit 2, 16.1 s
- 2026-11-13T07:24Z E6: four plans inside a 512 MiB memory cap (the provider pod limit): exit 2, 2, 2 in 16.8, 16.4, 16.0 s; one exit 1 in 14.6 s
- 2026-11-13T07:28Z E1: toy state pushed encrypted to a local SeaweedFS 4.22 S3 store
- 2026-11-13T07:29Z E7: test OpenBao stopped; key backup restored into a scratch OpenBao; init and state pull 6.4 s, 53 resources; wrong key refused; plan with the restored key exit 2 in 17.4 s
- 2026-11-13T07:32Z E5: stand-in scoped identity (role te_admin on the project only); plan refused: state read 403, bucket read 403
- 2026-11-13T07:34Z cleanup: IAM user, group, keys deleted and checked; 07:35Z bucket and objects deleted and checked

Resources the test created, and their end state:

- OBS bucket nslab-sov-080-state-cbf63a: deleted (HEAD 404; bucket count back to 49)
- OBS objects in nslab-sov-080-state-cbf63a (5 state objects, incl. encrypted copy of the bootstrap state): deleted
- IAM user nslab-sov-080-scoped: deleted (name lookup 0; user count back to 4)
- IAM group nslab-sov-080-scoped-group with role te_admin granted on the project: deleted (name lookup 0; grant removed with the group)
- IAM access keys of nslab-sov-080-scoped (2): deleted
- local working folder /home/openova/repos/tw-SOV-080: deleted
- local OpenBao 2.1.0 dev servers (test and scratch, in memory): stopped
- local SeaweedFS 4.22 server and its socket/log files under /tmp: stopped and deleted

## What later tasks must take into account

Six of the seven rows ran on the reference cloud: E1 yes, E2 yes, E4 no, E5 no, E6 yes, E7 yes. E3 was not run. The outcome is no, and SOV-410 is blocked on E4.

E4: the bootstrap module at its own revision (762402db21) planned two creates against the real state, with no change to any existing resource:
- the second region's NAT elastic IP, which was deleted outside OpenTofu;
- its SNAT rule, which was already missing from the bootstrap state.

These are environment causes, not module churn. Re-run E4 on a fresh single-region lane-A environment.

The same state planned with today's module (origin/main 14c335d6e) gives 30 add, 12 change, 12 destroy:
- `user_data` in place on all 12 nodes;
- security-group rules re-keyed;
- new `random_password` k3s tokens.

The cloud area needs boot data in `ignore_changes` and stable rule keys or `moved` blocks before a gate at the released revision can pass on any older Sovereign. The pack lock file on main also installs huaweicloud 1.91.0, while the bootstrap used 1.98.0.

What later packages must take into account:
- **E5 / SOV-070 / CLD identity.** A project-only Tenant Administrator gets 403 on the object store, both for state and for the module's bucket. SSH key pairs are per IAM user, so a second identity sees the key pair as missing and plans to replace it. The identity API answers only on the `iam-pub` host. The module's `iam` endpoint gives 404 `APIGW.0101` on every path. The provisioning key is an account admin and can create users, groups, grants and keys.
- **Contract C6 locking.** The reference object store ignores `If-None-Match`. OpenTofu `use_lockfile` gives no mutual exclusion, and a second writer deletes the first writer's lock. Keep a single writer and no outside apply while the Workspace is managed.
- **SOV-410 state-push.** The first `tofu state push` into an empty key creates a new lineage at serial 1. This is OpenTofu 1.12.0 behaviour, seen on both stores. A second push is refused as unrelated unless `-force` is used. Record the lineage read back after the push.
- **E6.** A plan takes 14.6 to 19.2 s against the 20-minute default timeout. Memory peaks at the 512 MiB provider pod limit (largest process about 310 MiB). One of four capped runs ended with exit 1, cause not captured. Raise the provider memory limit.
- **E7.** OK. Keep the key-provider block name `sovereign` and the key flags `exportable` and `allow_plaintext_backup`.
- **E3.** Not run here, because the machine has no container runtime and a cluster would need credentials written into it. The provider source shows that an Observe-only Workspace stays not-Ready while the plan exits 2. So the `workspace-commit` Ready wait times out on drift.
- **Certificates.** No private CA was needed from outside the cloud. The object store's certificate expires 2026-11-26.

Disclosure: this agent's host is the bastion machine (`bastion-openova`). The local processes started there (two in-memory OpenBao dev servers and SeaweedFS) are stopped. SeaweedFS had written socket and log files under `/tmp`, and these were removed. The working folder was deleted. No cloud setting of the bastion was touched.
