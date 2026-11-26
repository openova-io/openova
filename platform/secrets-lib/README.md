# secrets-lib

A Helm **library chart** (`type: library`) that holds the one way a platform
chart declares its secrets: the namespace's External Secrets reader, the
values the chart owns, and the entrypoint shim for mounted files (SEC-200).

## Role in Catalyst

Control-plane plumbing, never a user surface. It renders nothing on its own:
a chart lists it as a file dependency and `helm dependency build` vendors it
into that chart's package, the way `platform/topology-lib` is used. It is not
published on its own (`blueprint-release.yaml` skips library charts). The
model it implements is the secrets design of [`docs/SECURITY.md`](../../docs/SECURITY.md)
and the bp-openbao layout in [`platform/openbao`](../openbao/README.md):
tier A values are synced Secrets under `openova/apps/<namespace>/`; tier B
values live only in OpenBao mount `mounted/` and reach a pod as a file.

```yaml
# Chart.yaml of a consumer
dependencies:
  - name: secrets-lib
    version: 0.1.0
    repository: "file://../../secrets-lib/chart"
```

## Templates

| Template | Input | Renders |
|---|---|---|
| `openova.secretStores` | the root context | ServiceAccount `eso-reader`, SecretStore `openbao` (mount `secret`, role `eso-ns`) |
| `openova.generatedSecrets` | `dict "ctx" . "secrets" .Values.generatedSecrets "peers" <list> "primary" <bool> "self" <region>` | per value, below |
| `openova.envFromFiles` | `dict "map" <var: file> "exec" <list>` | `command` and `args` of the shim |

```yaml
generatedSecrets:
  db-password:
    delivery: mounted        # mounted (tier B) | synced (tier A)
    epoch: 1                 # raising it rotates
    length: 32               # default 32
    symbols: 0               # default 0
    scope: sa                # sa | shared (mounted only)
    serviceAccount: wordpress
    sharedAcrossRegions: true   # default true
  model-credential:
    delivery: mounted
    epoch: 1
    serviceAccount: axon
    sharedAcrossRegions: false
    key: document            # mounted only; default password
    placeholder: true        # mounted only; default false
  api-key:
    delivery: synced
    registerId: <register entry>
    epoch: 1
    mountedCopies:
      - serviceAccount: organization-controller
```

- **mounted** — no Secret, ExternalSecret or PushSecret. The birth Job
  (`secrets-birth-<hash of the plan>`, service account `secrets-birth`,
  OpenBao role `secrets-birth`) writes the value at
  `mounted/<ns>/sa/<sa>/<name>/e<epoch>` or `mounted/<ns>/shared/<name>/e<epoch>`
  with create-only rights. A shared value is written by the primary region's
  Job into **every** region's OpenBao: its own first, then each peer through
  bp-openbao's peer trust (identity role `openbao-peer-birth-<ns>` here, role
  `secrets-birth-<ns>` on the peer's `jwt-peer-<self>` mount, create-only on
  `mounted/data/<ns>/*`). A region that is not the primary renders nothing for
  a shared value. A second run writes nothing: a value that exists here is
  read as present from its metadata, and a peer's refusal of the write while
  the token holds `create` is read as present.
- **mounted, `key`** — the document holds the value under that key
  (`[A-Za-z0-9][-._A-Za-z0-9]*`) instead of `password`. A value without `key`
  is written under `password`, as before.
- **mounted, `placeholder: true`** — the Job creates the document once,
  create-only, holding the key with an empty string (no `length` or
  `symbols`), so a CSI mount of the path succeeds before the real value
  exists. A later writer with update rights replaces it; the Job reads the
  path as present and never writes it again. Raising the epoch creates a new
  empty placeholder at the new path.
- **synced** — Password generator and PushSecret (`IfNotExists`) into
  SecretStore `openbao` at `openova/apps/<ns>/<name>/e<epoch>`, plus
  ClusterSecretStore `vault-<peer>` per peer in the primary; ExternalSecret
  `<name>` with the register id. Each `mountedCopies` entry makes the birth Job
  copy Secret `<name>` to `mounted/<ns>/sa/<sa>/<name>/e<epoch>` in each region.

The render fails on a missing or unknown `delivery`, a synced value without
`registerId` (or a mounted one with it), a mounted `scope: sa` value without
`serviceAccount`, `mountedCopies` on a mounted value, an epoch below 1, a
name outside `[a-z0-9-]+`, a shared value for peers without `self`, `key` or
`placeholder` on a synced value, an empty or malformed `key`, a `placeholder`
that is not a boolean, and a placeholder with `length` or `symbols`.

## Configuration

`.Values.openova.secrets` of the consumer (all optional): `openbaoAddress`
(default `http://openbao.openbao.svc.cluster.local:8200`), `openbaoNamespace`
(default `openbao`), `birthImage` (default the Harbor proxy of
`curlimages/curl:8.10.1`).

A namespace whose Job writes shared values into the peers is listed in
bp-openbao `peerTrust.birthNamespaces` of every region.

## Operations

- **The Job failed with exit 1**: nothing was written (a region was not
  reachable or refused the login or the `create` right); the Job retries.
- **The Job failed with exit 3**: something was written and the run failed
  after it, or a peer was missing a value that this region holds; the log
  names the region. Raise the value's epoch: the new path is written fresh in
  every region and the pods roll to it.
- **Network**: the library renders no network policy (the policy chart owns
  them). The Job's pods carry `app.kubernetes.io/name: secrets-birth`;
  bp-openbao's peer ingress admits them on 8200 from any ClusterMesh cluster.
  A namespace whose egress is closed, and a single-region Sovereign, admit the
  Job to OpenBao through bp-plane-isolation.

## Tests

- `chart/tests/render.sh` — every template, every refused input (helm, awk).
- `chart/tests/birth-live.sh` — the birth script against two real OpenBao
  servers configured by bp-openbao's own reconciler: present in every region
  after one run, a second run changes nothing, the create-only rights, the
  failures that name a region, a named key, and a placeholder that a later
  writer replaces and the Job never writes again.
