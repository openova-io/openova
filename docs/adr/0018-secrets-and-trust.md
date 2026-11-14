# ADR-0018 — Secrets and trust: injection, seal, supply-chain verification

Status: Accepted
Date: 2026-11-13

Written by work package SEC-006. It records how the platform keeps and delivers secrets: which secret sits in which tier, where the root of trust rests, how a credential reaches a workload, how a value is born and rotated, which identities a workload uses, and how images are verified at admission. Contract C8 (`docs/contracts/c8-seal-provider.md`) carries the exact form of the seal provider and of the k3s bootstrap. ADR-0018 states the decisions behind it.

Terms used here:

- **Secret object**: a Kubernetes object of kind `Secret`. Its value rests in etcd.
- **Application credential**: a credential that a workload of an Application uses, such as a database password, an administrator password, an API key or a sign-on client secret.
- **Tier 0**: the secrets below the Kubernetes API: the k3s tokens, the etcd encryption key and the OpenBao seal key.
- **Tier A**: a value synced from OpenBao into a Secret object. Allowed only for an entry of the exception register.
- **Tier B**: a value mounted from OpenBao into a pod as a file, and never synced.
- **Tier C**: a value that OpenBao creates or rotates itself: database users, database role passwords, certificates and SSH certificates.
- **Exception register**: the one file that lists every Secret object the platform allows (S17).
- **Producer identity**: an OpenBao identity that may write a value into another namespace's tier B path and may not read it (S20).

## Context

Measured on `main` at commit `8599f27c1`, on 2026-11-13.

- OpenBao is the Blueprint `bp-openbao` (`platform/openbao/chart`), kit slot 08 (`clusters/_template/bootstrap-kit/08-openbao.yaml`). It runs OpenBao 2.1.0 from the upstream chart 0.16.0, with one server in each region.
- A Helm hook Job initialises OpenBao with one unseal key (one share, threshold one) and stores that key as a Secret object in the OpenBao namespace. An unseal reconciler, a Deployment, reads it to open OpenBao after a restart. The chart also carries a script that generates a root token.
- OpenBao has one secrets engine: key-value version 2 at `secret/`. It has no database, PKI, SSH or transit engine, and no audit device. Its listener serves plain HTTP inside the cluster.
- Workloads receive values from OpenBao through External Secrets: the Blueprint `bp-external-secrets` (`platform/external-secrets/chart`, upstream chart 0.10.7, kit slot 15) and its stores (`platform/external-secrets-stores/chart`, kit slot 15a). Every value delivered that way becomes a Secret object. No `SecretProviderClass` exists in the repository, so no workload mounts a value from OpenBao. The upstream chart's agent injector is installed, and no workload is configured to use it.
- External Secrets logs in to OpenBao with Kubernetes auth and service-account tokens. Between regions the stores of `platform/external-secrets-stores/chart` use JWT auth: the primary region writes shared values into each peer region's OpenBao, with a one-hour identity token that the peer verifies against the primary's issuer keys. No key is copied between regions.
- The OpenBao chart carries three more multi-region mechanisms beside this peer push: a stretched-raft helper, a snapshot-replication job and an OpenBao `Continuum` resource. A comment in kit slot 08 names the variable `SOVEREIGN_OPENBAO_RAFT_CONFIG` as the step that activates the stretched raft in the second region, and no other file of the repository names it.
- One chart already births its values the way S12 decides: `platform/wordpress-tenant/chart` renders an External Secrets `Password` generator and a `PushSecret` with `updatePolicy: IfNotExists`.
- Since work package SEC-020, each region has two random k3s tokens of 64 characters, a server token and an agent token. They are the `random_password` resources `k3s_server_token` and `k3s_agent_token` of `infra/providers/huawei/main.tf` and `infra/providers/hetzner/main.tf`.
- Contract C8 is published at `docs/contracts/c8-seal-provider.md` (work package SEC-005).
- The provider module of the reference cloud configures an endpoint of the cloud's key service (`kms` in `infra/providers/huawei/versions.tf`). Whether OpenBao can seal with that service is not known.
- Sealed Secrets is installed as kit slot 05 (`bp-sealed-secrets`). The only sealed secrets in the repository are templates with an empty placeholder.
- Two admission verifiers of image signatures exist and neither verifies an image by default. Kit slot 32 installs the Sigstore policy controller (`bp-sigstore`) with no image policy. The policy chart `platform/kyverno-policies/chart` carries a legacy image rule, `cosign-verified`, which is off by default and whose default log address is the public transparency log `https://rekor.sigstore.dev`. The policy engine is Kyverno `v1.18.0` (`platform/kyverno/chart/Chart.yaml`). No `ImageValidatingPolicy` exists in the repository.
- The custom resource `SecretPolicy` is defined in the platform chart and has no controller.
- No register of the Secret objects that the platform allows exists yet.

## Decision

The governing rule: **a credential that an Application uses is delivered into the running workload from OpenBao and never becomes a Secret object.** A synced Secret object is an exception. Every exception is listed in one register, with a reason and a removal plan, and a gate check counts the exceptions. Encrypting etcd and moving the seal are hygiene. They are done, and they are not the answer to this rule.

**S1 Threat model and the wording of the claim.** The platform claims four things, each with a test. (1) An Application's credential never exists as a Secret object. (2) Every Secret object in a platform namespace matches an entry of the exception register (S17). (3) An offline reader of etcd, of an etcd snapshot or of a backup obtains no secret value. (4) No key material of OpenBao exists as a Secret object. The platform does not claim protection against root on a control-plane node, unless the Sovereign's seal kind is `cloud-key-service`.

**S2 Tiers.** Tier 0 is the root of trust and never enters the Kubernetes API. Tier B (mounted, never synced) and tier C (created or rotated by OpenBao) are the default for every credential a workload uses. Tier A (synced to a Secret object under encrypted etcd) is only for entries of the exception register. Every class of secret sits in exactly one tier. Tier A and tier B live in two different OpenBao mounts, `secret/` and `mounted/`, so that "tier B is never synced" is enforced by OpenBao policy and not by convention.

**S3 Root of trust through a seal-provider contract.** The seal has two ranked implementations. Rank 1, `cloud-key-service`: the cloud's key service holds the key. Rank 2, `static-node-file`: a 32-byte key born in the provider's OpenTofu module and delivered by cloud-init to the control-plane nodes on a volume separate from the system disk, never as a Secret object. A provider pack uses rank 1 when the cloud has a key service that OpenBao can use, and rank 2 otherwise. Spike SEC-135 answers with yes or no whether one mechanism for the reference cloud's key service works. Until it answers yes, and on every cloud without a key service, the pack ships rank 2 (SEC-150; SEC-155 only after a yes). The second provider keeps the key in a directory of the system disk until it is proven again (CLD-183). Contract C8 states the form.

**S4 The k3s tokens are tier 0.** The k3s server token is a random value of 64 characters per region, and workers join with a separate agent token. k3s wraps the etcd encryption key with the server token, so etcd encryption is worth nothing while the token can be computed. SEC-020 made both tokens random for new Sovereigns. SEC-170 changes the tokens of existing clusters.

**S5 Encryption of etcd.** k3s encrypts Secret objects with its native `secretbox` provider, switched on at first boot in the shared bootstrap template, with the flags of contract C8. k3s gets no KMS plug-in. OpenBao's transit engine in the same cluster is never the provider of the etcd key.

**S6 OpenBao upgrade, seal and bootstrap are one change.** OpenBao moves from 2.1.0 to the 2.7 series together with the seal change and declarative self-initialisation, in one pull request (SEC-162), after spike SEC-130. After it there is no init Job, no unseal-key Secret, no unseal loop, no root-generation script, no root token and, by default, no recovery key.

**S7 Published with its security fix.** This decision belongs to a security fix. Its text enters the repository with the ticket of that fix, not before.

**S8 Workload identity.** Workloads prove their identity with projected service-account tokens only: Kubernetes auth inside a cluster, JWT auth between clusters (already used between regions). There is no AppRole and no SPIRE for secret delivery. There is no static OpenBao token, with one registered exception: the token that OpenTofu needs for state encryption, which is minted again every 15 minutes and lives one hour (S21).

**S9 Policy layout.** Two identity-templated policies and two generic auth roles replace policy objects per Organization and per Application. The path holds the host namespace and the service account, so a workload can reach only its own subtree. The host namespace is the boundary of an Organization (ADR-0019).

**S10 Scope of External Secrets.** Each namespace gets namespaced stores that log in as a service account of that namespace, and OpenBao enforces the path (SEC-200, SEC-205). The cluster-wide store stays only for a frozen list of legacy keys that may only shrink, fenced by conditions on the store and by an admission rule on the remote key (SEC-207). An Organization namespace never gets the cluster-wide store.

**S11 Injection mechanism.** Values are injected by the Secrets Store CSI driver with the OpenBao provider, with Secret sync switched off in the driver (SEC-211). A workload that reads files uses the mounted file. A workload that reads only environment variables uses, in this order: the image's own file variable (for example a `_FILE` suffix) where it has one; otherwise a first-party entrypoint shim that exports the mounted files as variables and then starts the original command. Only where neither is possible does the value stay a synced Secret object, as a register entry with a removal plan. The agent injector is switched off: it has no consumer and pulls a second vendor's image.

**S12 Birthplace.** A value is generated by an External Secrets generator and pushed straight to OpenBao with `updatePolicy: IfNotExists`. There is no Helm random function, no plain Secret manifest and no secret value in Git. A static value is rotated by a Git commit that raises its epoch.

**S13 Database credentials, PKI and SSH.** First-party services get dynamic database users from the OpenBao database engine. An Application that cannot reconnect with new credentials gets a static database role whose password OpenBao sets and rotates; the Application reads it from a CSI mount, and the database operator needs no password Secret for that role. Both follow the injection roll-out and wait for spike SEC-280 (SEC-290, SEC-300). PKI: one root in the primary region and one intermediate per region, issued through cert-manager by two issuers, `openbao-internal` and `sovereign-mesh-ca` (SEC-250). A region's signing request travels once over the north-south gateway and never over the mesh. If the mesh test NET-120 shows that intermediates per cluster under one root do not work, one authority in the first region is used, reached over the north-south gateway. SSH: one certificate authority per region (SEC-310), delivered together with its first consumer, INF-410.

**S14 Multi-region.** One design: an independent OpenBao in each region, and shared material pushed by the primary region into each peer (peer push). The stretched-raft helper, the snapshot-replication job and the OpenBao `Continuum` resource are deleted (SEC-100). A plain raft snapshot backup to object storage replaces the snapshot half (SEC-180).

**S15 Audit.** From OpenBao 2.7 the audit device is declared in the server configuration (`audit "file" "stdout"`), so it exists before the first request and needs no API call. It writes to standard output, and the log pipeline collects it. It is not enabled through the API before the upgrade of S6: a device created through the API would collide with the declared one after the upgrade.

**S16 Deletions.** Sealed Secrets (kit slot 05) leaves the tree (SEC-105). The `SecretPolicy` definition without a controller is deleted by the foundation's delete list (FND-099); this decision record confirms that nothing needs it.

**S17 Exception register.** One file, secret-object-register.yaml in the folder scripts/secret-guard-baselines, lists every Secret object that the platform allows, with its class, consumer, reason and removal plan (SEC-095). Class K: a Kubernetes or controller interface accepts only a Secret reference; kept and reviewed yearly. Class U: an upstream chart's pod can consume nothing else today; it names a removal package. Class T: a transitional stop-gap; it names a removal package. An Application credential may appear only as class U or T, never as class K. A guard in CI checks chart sources against the register. Gate check SEC-G11 counts the Secret objects on the gate Sovereign that match no entry. The count may only fall (SEC-096, SEC-248).

**S18 Delivery into an Organization's virtual cluster.** A workload inside an Organization's virtual cluster receives its credentials through the same CSI mount: the virtual cluster syncs the pod to the host, and the host's kubelet mounts the volume. Spike SEC-140 confirms the identity that the host presents and the one object that must be synced. Until the injection roll-out reaches them (SEC-214, SEC-215), the credentials of the marketplace Organization path are a transitional register entry of class T, born in OpenBao, with no value in Git.

**S19 Signature verification at admission.** There is one verifier: the policy engine that is already installed. One `ImageValidatingPolicy` checks platform images against the release public key and needs no transparency log. It runs in audit first and then denies (SEC-272, SEC-273, gate check SEC-G14). The second verifier, kit slot 32, is deleted (SEC-274). Spike SEC-271 confirms that the engine can verify with no route to a public log. If it cannot, the controller of slot 32 stays as the one verifier and the engine rule is not written. Verification when Flux pulls a source is the work of area SOV and not part of this decision record.

**S20 Write-only producer identities.** A party that must put a value into another namespace's tier B path gets a named producer identity: one policy and one Kubernetes-auth role, with create and update on a listed path pattern and no read of the data (SEC-192). One named exception: the model simulation back end writes and reads its own model-credential path, with its own role and policy, `axon-credential`.

**S21 State key and its offline backup.** Infrastructure state is encrypted with the transit key `tofu-state`. The provider pod gets a token that is minted again every 15 minutes, through the Secret object `crossplane-system/tofu-state-token`, a class K entry. A Job logs in with its own service account. The second key is one offline backup of that transit key, taken wrapped by the sovereign-admin. There is no passphrase fallback and no long-lived token (SEC-190). ADR-0016 records the same token from the side of the cloud contract.

## Alternatives rejected

**S1.** (a) No Secret objects at all. Flux sources, image pulls, gateway certificates, cert-manager, CloudNativePG and Crossplane accept only Secret references. Removing every Secret breaks the bootstrap chain and forces forks of upstream charts. These are platform plumbing, not Application credentials, and each one is a register entry. (b) Answering the rule with etcd encryption alone. A reader of Secret objects through the API still obtains every synced value. (c) Claiming protection against root on a control-plane node for the kind `static-node-file`. Nothing in the cluster delivers it, and the platform makes no claim that nobody can test.

**S2.** (a) One key-value mount, with the tier marked by the path only. External Secrets would then hold read on tier B paths, and one stray ExternalSecret would copy a tier B value into etcd. Two mounts cost one extra mount and make the rule a fact of policy. (b) Three tiers without tier 0. The k3s token, the etcd key and the seal key would then have no owner, which is how the token came to be computed from public values.

**S3.** (a) Writing the node-file seal straight into the shared template with no contract. Two ranked implementations are needed, and the contract is one set of template arguments, not a framework. (b) A small external OpenBao per Sovereign as a transit seal. Something must unseal that instance too, so it adds machines without moving the root of trust. (c) A transit seal against the parent. A Sovereign must not depend on its parent to start. (d) Generating the key on the node with no durable copy. A replaced control-plane node would lose the key while the OpenBao volume survives, and every secret would be lost. (e) Delivering the static key as a Secret object. It puts the key back in etcd. (f) Keeping the key file on the system disk. That disk holds etcd; a separate volume means that a copy of the system disk alone does not carry the seal key.

**S4.** (a) Keeping a computed token and relying on closed ports. The token also wraps the etcd key inside every etcd snapshot. (b) Generating the token in catalyst-api. The provider module is the one contract that both clouds share, and a `random_password` resource keeps the value stable across applies and out of the API's memory.

**S5.** (a) A KMS v2 plug-in against the OpenBao in the same cluster. The API server needs the key before OpenBao's pods can start. (b) A KMS v2 plug-in against a cloud key service on k3s. k3s does not accept KMS providers in its managed configuration, and no production-grade OpenBao plug-in exists. (c) `aescbc`, the k3s default. Kubernetes rates it weak, and `secretbox` needs only a patch-level k3s bump.

**S6.** (a) Upgrading first and reworking the seal later. From 2.5.3 the unauthenticated root-generation endpoint that the bootstrap calls is off by default, so the upgrade alone breaks every chart upgrade. (b) Keeping Shamir with more shares. A Sovereign that is provisioned with zero touch has nobody to hold shares. (c) Generating recovery keys by default. With no owner to receive them they would rest in the cluster, which recreates the defect. (d) Cleaning up the init Job on the old version first. While the unseal key rests as a Secret object in the same namespace, removing the Job's traces reduces no risk, and SEC-162 deletes the Job whole.

**S7.** Published with the decision, in the ticket of its security fix.

**S8.** (a) SPIRE. It was dropped from the kit, and projected tokens already give an identity per workload with no extra component. (b) Kubernetes auth across clusters. OpenBao would need a reviewer credential for every other cluster.

**S9.** (a) One rendered policy and role per Organization and per Application. That is hundreds of objects to write and a second Organization pipeline to keep in step. Identity templating is the standard pattern for this and needs no write per Organization. (b) OpenBao namespaces per Organization. They arrive only with the upgrade, and nothing here needs delegated administration yet. It stays a recorded option.

**S10.** (a) Only an admission rule on the cluster-wide store. Admission can be switched to audit or bypassed while a webhook is down, and OpenBao would still serve every key. (b) Deleting the cluster-wide store at once. Every existing ExternalSecret reads it; it shrinks by an allow-list instead.

**S11.** (a) The agent sidecar as the default. It is one sidecar per pod, a mutating webhook in the start path, and an image from another vendor. (b) The agent's process-supervisor mode, or an injecting webhook, for workloads that read environment variables. Both sit in the start path of every pod. The entrypoint shim does the same job with a few lines of shell that the platform owns. (c) Leaving workloads that read environment variables on synced Secret objects for good. The governing rule forbids it for Application credentials.

**S12.** (a) Helm `lookup` with `randAlphaNum`, kept because it is stable. The value is born in the Helm controller and stored twice in etcd, never in OpenBao. (b) Scheduled automatic regeneration with `updatePolicy: Replace`. Regions would disagree on a shared value for up to one refresh interval. (c) Building a rotation controller for `SecretPolicy`. The valuable classes move to tiers B and C instead.

**S13.** (a) Dynamic database users for every Application. A third-party application that cannot reload its credentials fails when its user expires; it gets a static role rotated by OpenBao instead. (b) One static role per Application kept as a synced Secret object. It is an Application credential, so it may not be a Secret object. (c) The agent sidecar for first-party services. They are Go services the platform owns, and a small package is smaller than a sidecar per pod. (d) One certificate authority key copied to every region. A private key cannot be pushed like a key-value entry; an intermediate per region under one root gives the same trust without copying a private key.

**S14.** (a) Stretched raft across regions. Its activation in the second region was never wired. Independent stores let a surviving region serve on its own when the other region is lost. (b) OpenBao on replicated PostgreSQL. It makes the database that needs secrets the store of the secrets, and nothing here proves it. It stays a recorded option.

**S15.** (a) An audit file on a volume with a shipping sidecar. Standard output is already collected, and a full audit volume blocks every request. (b) Enabling the device through the API in a bootstrap request. The configuration stanza is applied at every start and reload and cannot be forgotten.

**S16.** Keeping Sealed Secrets for bootstrap. It carries no real ciphertext, and it keeps a second decryption key in etcd.

**S17.** (a) A list of exceptions in prose in the decision record only. A list that nothing checks drifts; the register is read by a guard and by a gate check. (b) One allow-list per chart. One file gives one number to burn down.

**S18.** (a) An ExternalSecret inside the virtual cluster. The virtual cluster has no External Secrets definitions, and no host controller sees its objects. (b) A generated password committed to Git and read back.

**S19.** (a) Two verifiers, the policy engine and the controller of slot 32. That is two rule sets to keep in step for one check. (b) Keeping the legacy image rule. It defaults to a public transparency log, which a Sovereign cannot reach after hand-over. (c) Keyless signing. A Sovereign must verify offline.

**S20.** (a) Giving External Secrets of the producer's namespace rights on other namespaces' paths. The admission rule of S10 exists to forbid exactly that. (b) One broad write policy for catalyst-api on `mounted/`. Each path pattern is listed, so a new producer is a reviewed line.

**S21.** (a) A passphrase as a fallback key provider. OpenTofu writes with one method, so the passphrase would have to sit in the cluster for every write. (b) A long-lived OpenBao token for the provider pod. A token that is minted again every 15 minutes needs no rotation and dies on its own.

## Consequences

- Every Application credential moves from a synced Secret object to a mounted file, chart by chart. Until a chart has moved, its Secret objects are register entries of class U or T, each with the package that removes it.
- OpenBao becomes a dependency of pod start for every pod that mounts a value. With the static seal a restarted OpenBao is open again in seconds; gate check SEC-G03 measures it. OpenBao runs one member per region until the control-plane node count per region is fixed.
- Running pods keep their mounted files while OpenBao is unreachable. A new or rescheduled pod waits until OpenBao answers. There is no fallback to a Secret object.
- The refresh timer of the CSI driver and the refresh interval of External Secrets are the two polls this decision record permits.
- The agent injector, Sealed Secrets, the second image verifier and the three other multi-region mechanisms of OpenBao are removed.
- Limits accepted now: with the kind `static-node-file`, root on a control-plane node can read the seal key; the etcd key rests on the control-plane disk, wrapped by the k3s server token; the OpenBao listener serves plain HTTP inside the cluster until SEC-330.

## Shipped via

Work packages of area SEC in `docs/ledger/plan.yaml`:

- Contract and record: SEC-005 and SEC-006. The rewrite of the security document: SEC-110 and SEC-115.
- Tokens, etcd and existing clusters: SEC-020, SEC-125, SEC-120 and SEC-170.
- Seal and OpenBao 2.7: spikes SEC-130 and SEC-135, and epic SEC-160 (SEC-150, SEC-161, SEC-162 and SEC-155). Snapshot backup and restore: SEC-180. TLS on the listener: SEC-330.
- Mounts, policies, roles and identities: SEC-190, SEC-192, SEC-196 and SEC-198.
- Stores: SEC-200, SEC-205 and SEC-207.
- Injection: spike SEC-140 and epic SEC-210 (SEC-211 to SEC-215); the roll-out epics SEC-220, SEC-230 and SEC-240; spike SEC-335 and epic SEC-340 for the pods of upstream charts.
- Register, guards and census: SEC-071, SEC-090, SEC-095, SEC-096 and SEC-248.
- Deletions: SEC-100, SEC-105, SEC-238 and SEC-274.
- External Secrets upgrade: epic SEC-260 (SEC-261, SEC-262 and SEC-263).
- Database engine, PKI and SSH: spike SEC-280, SEC-290, SEC-300, SEC-250 and SEC-310.
- Image verification: spike SEC-271, SEC-272 and SEC-273.
- The security fixes SEC-010, SEC-030, SEC-040, SEC-060, SEC-073 and SEC-080, whose text is published with their tickets.
