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
| `openova.secretStores` | the root context, or `dict "ctx" . "namespace" <name>` | ServiceAccount `eso-reader`, SecretStore `openbao` (mount `secret`, role `eso-ns`) |
| `openova.generatedSecrets` | `dict "ctx" . "secrets" .Values.generatedSecrets "peers" <list> "primary" <bool> "self" <region>` and optional `"namespace" <name>` | per value, below |
| `openova.envFromFiles` | `dict "map" <var: file> "exec" <list>` | `command` and `args` of the shim |

**`namespace`** (SEC-217) — the namespace of every value and of every object
the template renders for it: the Password, PushSecret and ExternalSecret,
the birth Job, its ServiceAccount `secrets-birth` and plan ConfigMap, the
paths `mounted/<ns>/...` and `openova/apps/<ns>/...`, and the peer roles
`secrets-birth-<ns>` and `openbao-peer-birth-<ns>`. Absent or null: the
release's namespace. A chart whose reader runs in another namespace names
it here; the birth Job then runs there, because OpenBao lets a birth Job
create only below its own namespace (`mounted/data/<ns>/*`). Give the same
`namespace` to `openova.secretStores` for a synced value. The namespace must
exist, and the release must be allowed to write there.

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
- **synced, `template`** (SEC-219) — the target Secret's `type`, `labels`,
  `annotations` and `data`, rendered as the ExternalSecret's `target.template`
  (engine v2). Each `data` entry is a template over the value, `{{ .password }}`,
  and over each joined value, `{{ index . "<name>" }}`; text around them is
  kept (`postgresql://app:{{ .password }}@host/app`). Labels, annotations and
  data are strings (quote `"true"`). Without `template` the ExternalSecret is
  as before.
- **synced, `copies`** (SEC-219) — each entry (`name`, optional `namespace`,
  optional `template`) is one more ExternalSecret `<name of the copy>` of the
  same value, register id and remote key, in the value's own namespace,
  through SecretStore `openbao`. A copy that names another namespace is
  refused: there the namespace's store reads only its own prefix, and a value
  read in another namespace is written through by its producer (SEC-225).
- **synced, `join`** (SEC-219) — names of other synced values of the chart;
  every ExternalSecret of this value (its own and its copies) also carries
  each joined value, under its name, so one Secret holds several values, each
  under its own key (with `template.data` under the keys the reader expects).
  A joined value is born (Password and PushSecret) and renders no
  ExternalSecret of its own, so it takes no `registerId`, `template`,
  `copies`, `join` or `mountedCopies`.

```yaml
generatedSecrets:
  shared-pg-harbor:            # the role Secret of a database operator
    delivery: synced
    registerId: <register entry>
    epoch: 1
    template:
      type: kubernetes.io/basic-auth
      labels: {cnpg.io/reload: "true"}
      data: {username: harbor, password: "{{ .password }}"}
    copies:
      - name: harbor-db        # same namespace; its own keys
        template:
          data: {password: "{{ .password }}", uri: "postgresql://harbor:{{ .password }}@pg-rw:5432/harbor"}
  powerdns-api-credentials:
    delivery: synced
    registerId: <register entry>
    epoch: 1
    join: [webserver-password]
    template:
      data:
        api-key: "{{ .password }}"
        webserver-password: '{{ index . "webserver-password" }}'
  webserver-password:          # joined: born, no Secret of its own
    delivery: synced
    epoch: 1
```

The render fails on a missing or unknown `delivery`, a synced value without
`registerId` (or a mounted one with it), a mounted `scope: sa` value without
`serviceAccount`, `mountedCopies` on a mounted value, an epoch below 1, a
name outside `[a-z0-9-]+`, a shared value for peers without `self`, `key` or
`placeholder` on a synced value, an empty or malformed `key`, a `placeholder`
that is not a boolean, a placeholder with `length` or `symbols`, a
`namespace` that is not a namespace name (a DNS label; an empty string too),
and (SEC-219) `template`, `copies` or `join` on a mounted value; a template
field other than `type`, `labels`, `annotations` and `data`, a value in them
that is not a string, a malformed data key or type; a copy without a name, of
an unknown field, with a name that is not a Secret name, that is a value's
or an earlier copy's, or in another namespace; a join of an unknown or
mounted value, of the value itself, of `password` or of one value twice; a
joined value with its own `registerId`, `template`, `copies`, `join` or
`mountedCopies`; and `mountedCopies` with template data whose `password` is
not `{{ .password }}`.

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

- `chart/tests/render.sh` — every template, every refused input (helm, awk);
  R11 to R13 and F25 to F44 the template, copies and join of SEC-219.
- `chart/tests/birth-live.sh` — the birth script against two real OpenBao
  servers configured by bp-openbao's own reconciler: present in every region
  after one run, a second run changes nothing, the create-only rights, the
  failures that name a region, a named key, a placeholder that a later
  writer replaces and the Job never writes again, and a named namespace: a
  Job of the release's namespace is refused its path, the Job there writes it.
