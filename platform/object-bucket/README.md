# bp-object-bucket

The in-cluster implementation of the capability `object-storage`, contract `v1`
(`platform/_capabilities/object-storage.yaml`): one bucket on the Sovereign's own SeaweedFS for each
Context, each with its own access key that may act on that bucket only.

**Role in Catalyst.** A `data-service` Blueprint, `shareable`, `unlisted`. It is the default
implementation of `object-storage`. A consumer Blueprint declares
`requires: [{capability: object-storage, contract: v1, alias: <alias>}]`; the install API resolves the
requirement to this Blueprint, adds a Context entry to `spec.parameters.buckets` of its Application
and gives the consumer `spec.parameters.bindings.<alias>` (S4 and S7 of
[ADR-0020](../../docs/adr/0020-service-model-classes-and-capability-contracts.md); [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)).

## What one Context gets

For each entry of `buckets` the chart runs one Job (`chart/files/context.sh`) in the SeaweedFS image:

1. It creates the bucket `<name>-<8 hex of sha256 of namespace/name/alias>` when it is absent, and
   sets its storage rule: one volume per growth (spike SVC-250 measured that a new bucket otherwise
   takes 7 of the kit's 30 volume slots), and a time to live of `retentionDays` when it is above 0.
   `fs.configure` rewrites the filer's whole rule file with no lock, so the Job reads its rule back
   after a pause and writes it again, up to 5 times, until the filer holds it. SeaweedFS takes a
   time to live of 1 to 255 days, so the render refuses a `retentionDays` above 255.
2. It enables versioning when `versioning` is true, and suspends it when it is false on a bucket
   that has it.
3. It makes a SeaweedFS identity of the bucket's name with `Read`, `Write`, `List` and `Tagging` on
   that bucket only, with a fresh key, and writes the nine keys of the contract to the entry's
   `binding.path` in OpenBao, as the producer `binding` of bp-openbao, which may write and never read.
   A key whose binding already exists is kept, so a re-run never changes the key a consumer holds.

Removing an entry runs a pre-upgrade Job that deletes the identity and the binding and keeps the
bucket. The keys never become a Secret object; the platform's own administrator key is never used.

## Configuration

| Value | Default | Meaning |
|---|---|---|
| `buckets` | `[]` | The Context entries; written by the install API only |
| `seaweedfs.namespace` | `seaweedfs` | A release with a Context must run here |
| `seaweedfs.master`, `seaweedfs.filer` | the kit's services | What the Jobs' `weed shell` dials |
| `seaweedfs.s3Endpoint` | `http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333` | The binding's `endpoint` |
| `seaweedfs.caBundle` | `""` | The binding's `ca.crt` |
| `openbao.role` | `producer-binding` | The producer identity the Jobs log in as |
| `serviceAccount` | `object-bucket` | The identity that role binds |

## Operational notes

- **One instance.** Spike SVC-250 recorded outcome 2: a key scoped to one bucket can be made only
  from the SeaweedFS namespace, so one instance runs there and Organizations reuse it (SVC-275 adds
  it to the kit).
- **Endpoint.** The kit's S3 gateway serves HTTP inside the cluster (slot 18, `s3.httpsPort: 0`).
  The contract allows `http` only for a service name of the cluster.
- **Regions.** A bucket lives in the SeaweedFS of the region its Job ran in; the contract does not
  replicate a bucket to another region.
- **Uninstall.** Uninstalling the release removes no key and no binding; remove the entries first.
- **A failed Job.** A Job's name is a digest of its Context, the settings and the script, so a
  reconcile with the same values does not run a Job again after it used up its 3 retries (OpenBao
  sealed, for example). Its last log line `OBJECT-BUCKET fail <context> <what>` names the cause.
  After the cause is fixed, delete the Job (`kubectl -n seaweedfs delete job <name>`) and run the
  upgrade once more (`flux reconcile helmrelease <release> -n <its namespace> --force`): Helm
  creates the missing Job again under the same name, and the script keeps the bucket, the rule and
  a key whose binding exists.
- **Not yet enforced.** Until NET-163 enforces the SeaweedFS NetworkPolicy, any pod can reach the
  filer; when it does, its rules must keep this release's egress to OpenBao.
- Tests: `chart/tests/render.sh` (the render, and the script against stubs of `weed` and `curl`).
