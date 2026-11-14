outcome: no

# CMP-S1: new policy kinds, exceptions, time conditions and the test tool on engine line 1.19

Date: 2026-11-13. Evidence: branch `spike/CMP-S1` of openova-io/openova (head 83c098060, never merged). Workflow `.github/workflows/spike-cmp-s1.yaml`, scripts and manifests under `tests/spikes/CMP-S1/`.

## Question

On the 1.19 engine line, the version CMP-058 installs:

1. Under which version is `ValidatingPolicy` served in group `policies.kyverno.io`?
2. Does a background scan write the same report results for it as for the legacy kind?
3. Which chart values switch exceptions on and name their namespace?
4. Is `time.now()` evaluated in an exception's match condition, at admission and in background scans?
5. How long from a resource change to its report?
6. Does `kyverno test` of that tool version evaluate `ValidatingPolicy` and `MutatingPolicy` files?

## What was run

Two runs of the workflow. Each run has three jobs: the engine experiment on two Kubernetes versions, and the tool experiment.

- Run 1: 43349994544 (commit 70e6d412d). All six questions.
- Run 2: 43351527328 (commit 83c098060). The same, plus a follow-up on question 4 (the waiver without the time condition, deletion of the exception, and the native `expiresAt` field).

Versions:

- Upstream chart `kyverno` 3.9.2, which carries engine v1.19.2 (all four controller images v1.19.2).
- kind v0.31.0. Node images `kindest/node:v1.31.14` (the minor line the Sovereigns run; they run k3s v1.31.4) and `kindest/node:v1.34.3`, both pinned by digest.
- helm v3.22.0. kubectl of the same version as the node.
- Command-line tool `kyverno` v1.19.2, release tarball, checksum checked against the release's checksums file.

Engine values: only `features.policyExceptions.enabled: true` and `features.policyExceptions.namespace: catalyst-policy-exceptions`. Everything else is the upstream default, as in the platform's own values today. One replica per controller.

Engine job steps:

1. Read the chart's default values. Install the chart. Read the flags rendered into each controller.
2. Read the served, storage and deprecated versions of every CRD in `policies.kyverno.io`.
3. Create two Deployments (1 and 3 replicas) before any policy exists. Apply a legacy `ClusterPolicy` and a `ValidatingPolicy` with the same rule (replicas 2 or more, audit, background on) under different names. Wait for the background scan to write all four results. Compare them field by field.
4. Three rounds: create a 1-replica Deployment, time until both policies report `fail`; patch it to 3 replicas, time until both report `pass`. Poll every 0.5 s.
5. Apply one mutate policy of each kind (each adds its own label to ConfigMaps). Create a ConfigMap and read its labels.
6. Apply an enforcing `ValidatingPolicy`. Apply an exception with the match condition `time.now() < timestamp(<two minutes ahead>)`, in versions v1 and v1beta1. Then the same exception without the time condition; delete it later, as an expiry commit would. Also an exception with `spec.expiresAt` two minutes ahead, watched for 300 s after its time. Controls: an exception outside the exception namespace, and an exception read again after the switch is turned off by `helm upgrade`. Admission is tested by server dry-run creates. Reports are read with no trigger other than the change under test.

Tool job steps: one suite (one `ValidatingPolicy` file, one `MutatingPolicy` file, three expected results) run once with `policies.kyverno.io/v1` and once with `v1beta1`. Two negative suites per version, each with one wrong expectation, must fail. Then the platform's own policy-chart suites with the same tool, for information.

## Measured values

All values below are the same on Kubernetes v1.31.14 and v1.34.3, in both runs, unless a range is given.

### 1. Served version

- `validatingpolicies`, `mutatingpolicies` and `policyexceptions`: served `v1`, `v1beta1`, `v1alpha1`. Storage version `v1beta1`. `v1alpha1` is marked deprecated. The group's preferred version is `v1`. (Runs 1 and 2, all engine jobs.)
- Every other kind in the group (generating, deleting, image validating, and the namespaced kinds) is served at `v1` too.
- All policies and exceptions of this test were applied as `v1` and accepted.

### 2. Report results of a background scan

Both resources existed before the policies, so only a background scan could judge them. All four results were written 10.4 to 11.0 s after the policies were applied, each with `properties.process: background scan`. (Runs 1 and 2.)

| Field | Legacy kind | New kind |
|---|---|---|
| result (1 replica / 3 replicas) | fail / pass | fail / pass |
| severity | medium | medium |
| category | catalyst-compliance | catalyst-compliance |
| scored | true | true |
| source | kyverno | KyvernoValidatingPolicy |
| rule | replicas-at-least-two | empty |
| message on fail | "validation error: <message> rule replicas-at-least-two failed at path /spec/replicas/" | "<message>" |
| message on pass | "validation rule 'replicas-at-least-two' passed." | "success" |
| properties on fail | process | process, cel.validationIndex "0" |

The verdicts, severity, category and scored flag are the same. Source, rule, message text and one property differ.

No native admission policy was generated, although the chart's default `features.generateValidatingAdmissionPolicy.enabled` is `true` (count 0 in every job).

### 3. Chart values for exceptions

- Value names: `features.policyExceptions.enabled` and `features.policyExceptions.namespace`. Upstream defaults: `false` and `''`. So today the platform's engine honours no exception at all.
- They render as `--enablePolicyException=true --exceptionNamespace=catalyst-policy-exceptions` into the admission, background and reports controllers. (Runs 1 and 2.)
- Control, switch on: an exception without a time condition in `catalyst-policy-exceptions` was honoured at admission.
- Control, wrong namespace: the same exception in namespace `default` was created with the warning "PolicyException resource namespace must match the defined namespace." and was ignored (the request was refused).
- Control, switch off: after `helm upgrade --set features.policyExceptions.enabled=false`, the same exception was ignored. The refusal came 42.5 to 45.1 s after the upgrade started (rollout included).

### 4. `time.now()` in an exception's match condition

- The engine's own admission check refuses the exception at creation, in `v1` and in `v1beta1`: "undeclared reference to 'time'" and "undeclared reference to 'now'". The object is not stored. So the condition is evaluated nowhere: not at admission, not in a background scan. (Run 1 in `v1`; run 2 in `v1` and `v1beta1`; both Kubernetes versions.)

Follow-up on the same waiver without the time condition (run 2):

- Admission: a Deployment the policy refuses was admitted 0.105 to 0.147 s after the exception was created.
- Background, resource that existed before: result `skip` 10.8 to 11.1 s after the exception was created, with no other trigger. Message "rule is skipped due to policy exception: catalyst-policy-exceptions/spike-waiver". Properties `exceptions: spike-waiver`, `process: background scan`.
- Background, resource created under the exception: `skip` about 41 s after its creation.
- Deleting the exception: admission refused again after 0.114 to 0.150 s. Both resources reported `fail` again 11.0 to 11.2 s after the deletion.

Follow-up on the native expiry field `spec.expiresAt` (run 2):

- The field is in the served `v1` schema ("Once expired, the exception will no longer be applied to incoming requests").
- Before its time it was honoured: admitted, and background `skip`.
- 300 s after its time it was still honoured: admitted at every probe and still `skip` in the report, on both Kubernetes versions.

### 5. Time from a resource change to its report

- 48 values (4 engine jobs, 3 rounds, create and change, 2 kinds): 19.4 to 20.6 s. (Runs 1 and 2.)
- Both kinds appear in the same report update. The results carry `process: background scan`: the background scan of the changed resource writes them.
- A policy change to its report: 10.4 to 11.0 s (question 2).

### 6. The test tool on the new kinds

- Tool v1.19.2, tarball sha256 `357eaf1df8bc24ddd5a73979fadeaae6a032e8710f11af264229756e4956bf4a`, matching the release checksums. (Runs 1 and 2.)
- With `v1` and with `v1beta1`: the positive suite passed with "3 tests passed and 0 tests failed" (exit 0). The negative validating suite failed with "Want pass, got fail" (exit 1). The negative mutating suite failed with "Patched resource didn't match" (exit 1). So the tool evaluates both files.
- `kyverno apply` agrees: pass 1, fail 1 for the validating file.
- For information: the platform's four policy-chart suites and two shell tests all pass with tool v1.19.2.

### Mutate policies

Both mutate policies took effect within 0.11 to 0.19 s. A new ConfigMap carried both labels. (Runs 1 and 2.)

## Outcome

`no`. Only question 4 is no. Questions 2 and 6, the ones that stop the epic, are yes.

## What follows for the plan

- Epic CMP-060 proceeds as written.
- The policy template takes `apiVersion: policies.kyverno.io/v1`. The engine accepts it and the tool tests it.
- CMP-061 step 2: under the umbrella's `kyverno:` key, set `features.policyExceptions.enabled: true` and `features.policyExceptions.namespace: catalyst-policy-exceptions`. Step 4: the informer version is `v1`.
- CMP-083 drops the time condition. It must not render it: the engine refuses the object, so a commit carrying it fails to apply. The residual stands: the exception ends only when the expiry commit deletes it. Measured, a deletion takes effect at admission within 0.2 s and in the report within about 11 s.
- Do not use `spec.expiresAt` as the expiry on v1.19.2. It is in the schema but was not honoured in 300 s after its time.
- Read model and report adapter: treat `rule` as optional (empty for the new kinds). Accept source `KyvernoValidatingPolicy` beside `kyverno`. Do not match on message text. An excepted resource is `skip` with `properties.exceptions` naming the exception; that maps to `waived`.
- The waiver gate check (CMP-G11) has room: an existing resource's finding turns `skip` about 11 s after the exception exists, and a new one about 41 s after creation.
- An exception placed outside `catalyst-policy-exceptions` is accepted by the API with only a warning and has no effect. The exception's location must be guarded.
- On 1.19 every apply of a legacy `ClusterPolicy` prints a deprecation warning. Nothing fails.
- CMP-058: the tool checksum above is the v1.19.2 value. The current suites pass with that tool.

## Not measured

- k3s itself. The nodes ran upstream Kubernetes v1.31.14 and v1.34.3, not k3s v1.31.4.
- The platform's umbrella chart. Its cert-manager TLS wiring, proxied image paths and two admission replicas were not installed; the upstream chart with defaults and one replica each was.
- Load. The 20 s and 11 s figures come from an idle one-node cluster.
- `spec.expiresAt` after a controller restart or later than 300 s after its time.

## Answers

1. yes: served at `v1` (also `v1beta1`, the storage version; `v1alpha1` deprecated).
2. yes: same verdicts, severity, category and scored flag; source, rule and message text differ.
3. yes: `features.policyExceptions.enabled` and `features.policyExceptions.namespace`.
4. no: the engine refuses `time.now()` in an exception's match condition at creation, so it is evaluated neither at admission nor in background scans.
5. yes: about 20 s (19.4 to 20.6 s) for both kinds.
6. yes: tool v1.19.2 evaluates `ValidatingPolicy` and `MutatingPolicy` files, in `v1` and `v1beta1`.

## Evidence

Outcome: **no**. The experiment is the branch `spike/CMP-S1` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43349994544
- https://github.com/openova-io/openova/actions/runs/43351527328

## What later tasks must take into account

All six questions were answered in CI, on Kubernetes v1.31.14 and v1.34.3, twice, with the same results. Only question 4 is no. The epic proceeds; CMP-083 must drop the time condition. Do not just omit it from the gate: the engine refuses an exception that carries `time.now()` at creation (in v1 and v1beta1). In a waiver commit that would make the region's policy apply fail.

The native `spec.expiresAt` field is not a substitute on v1.19.2. It is in the schema and honoured before its time, but it was still honoured 300 s after its time, at admission and in reports. So the expiry commit that deletes the exception remains the only end. Measured, a deletion takes effect at admission within 0.2 s and in the report in about 11 s.

For later packages:
- New-kind results have an empty `rule` and source `KyvernoValidatingPolicy`. The report adapter and anything keyed on policy plus rule must treat rule as optional.
- The upstream default leaves exceptions off, so today the platform's engine honours none.
- An exception outside `catalyst-policy-exceptions` is accepted with only a warning and has no effect. Guard where the exception is placed.

Could not be measured in CI:
- k3s itself. kind runs upstream Kubernetes; the Sovereigns run k3s v1.31.4.
- The platform umbrella chart's extras: cert-manager TLS, proxied images, two admission replicas.
- Behaviour under load.
- `expiresAt` after a controller restart.

These need a gate environment after the CMP-058 train. Two of the five allowed pushes were used. The branch `spike/CMP-S1` is left in place as the evidence.
