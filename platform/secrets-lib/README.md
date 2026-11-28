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
  master-key:
    delivery: mounted
    epoch: 1
    serviceAccount: chargeback
    length: 43
    seedFrom: {name: chargeback-master-key, key: master-key}   # mounted, or synced (SEC-254)
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
- **mounted, `seedFrom`** (SEC-226) — `name` and `key` of a Secret of the
  value's namespace that holds the value today. When the store's path is
  absent the Job writes that key's value; only when the Secret does not exist
  (404) a new random value of `length` and `symbols`; a value the store holds
  is never read against the Secret or replaced. A Secret without the key, an
  empty value, a value that is not printable ASCII and any other answer of the
  API (a 403 too) fail the run before anything is written (exit 1), never a
  random value. The Job reads as its ServiceAccount `secrets-birth`, with a
  projected token of the API's audience and the cluster CA
  (`kube-root-ca.crt`); Role `secrets-birth-seed` grants `get` on exactly the
  Secrets its plan names (`resourceNames`) and RoleBinding
  `secrets-birth-seed` binds it. The value passes only through pipes and the
  Job's memory directory: never an argument, the environment or the log. A
  plan without a seed renders none of this, and the same script. Raising the
  epoch of a seeded value writes the Secret's value again at the new path
  while that Secret exists: to rotate, drop `seedFrom` with the raise.
- **synced** — Password generator and PushSecret (`IfNotExists`) into
  SecretStore `openbao` at `openova/apps/<ns>/<name>/e<epoch>`, plus
  ClusterSecretStore `vault-<peer>` per peer in the primary; ExternalSecret
  `<name>` with the register id. The PushSecret has `deletionPolicy: None`:
  no finalizer, and the born value stays in the store when the PushSecret is
  deleted (an epoch raise, a roll back, the namespace deleted); a later roll
  forward finds it. With `Delete` the finalizer sends a DELETE on the data
  path, which policy `eso-ns` does not grant, so the PushSecret would stay
  Terminating. Each `mountedCopies` entry makes the birth Job
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
- **synced, `source: external`** (SEC-228) — a value given from outside (a
  cloud credential, a value another chart produces). No Password and no
  PushSecret, in any region: the ExternalSecret `<name>` (and each copy)
  only reads `openova/apps/<ns>/<name>/e<epoch>`, with `template` and `join`
  as above, and carries the annotation `secrets.openova.io/writer`, from the
  required `writer`: who writes that path (printable text). The register
  entry of `registerId` names the same writer. While the path is absent the
  ExternalSecret is not ready and writes nothing (below), so the value is
  never generated. It takes no `length`, `symbols`, `sharedAcrossRegions`,
  `mountedCopies` or `seedFrom`, and joins only external values (a joined
  one takes no `writer`: one writer writes every path of the Secret). Raising the epoch
  reads a new path: its writer writes it first. `source: generated` is the
  default, byte for byte.
- **synced, `seedFrom`** (SEC-254) — as for a mounted value: `name` and
  `key` of a Secret of the value's namespace that holds the value today, the
  same checks and the same rules (the Secret's key where the path is absent,
  a new random value of `length` and `symbols` only when the Secret does not
  exist, a value the store holds never read against it or replaced). No
  Password and no PushSecret: a PushSecret beside it could write a random
  value first. The seed Job `secrets-seed-<hash of its plan>` (the birth
  script, mount `secret/`, the same ServiceAccount, Role `secrets-birth-seed`
  and API token) is the value's one writer, create-only, at
  `openova/apps/<ns>/<name>/e<epoch>` (key `password`): here, and for a
  shared value of the primary in every peer through the peer trust (role
  `secrets-birth-<ns>`), so its namespace is then in bp-openbao
  `peerTrust.birthNamespaces`. bp-openbao grants role `secrets-birth`
  `create` on `secret/data/openova/apps/<its namespace>/*` and `read` on its
  metadata, and a peer's `secrets-birth-<ns>` `create` there. The
  ExternalSecrets, `template`, `copies` and `join` are as without it; a
  joined value takes it too. A `mountedCopies` entry stays in the birth Job,
  which mounts Secret `<name>`: it starts once that Secret exists, and
  copies its key `password` as it is then (on a Sovereign that runs the
  chart today, the Secret that existed before the store held the seed).

```yaml
generatedSecrets:
  velero-objectstorage-credentials:
    delivery: synced
    source: external
    registerId: <register entry>
    writer: "the hand-over Job, as sovereign-ops"
    epoch: 1
    join: [s3-secret-key]
    template:
      data: {cloud: 'aws_access_key_id={{ .password }} aws_secret_access_key={{ index . "s3-secret-key" }}'}
  s3-secret-key:               # joined, external: read, never born
    delivery: synced
    source: external
    epoch: 1
```

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
`mountedCopies`; `mountedCopies` with template data whose `password` is
not `{{ .password }}`; and (SEC-226, SEC-254) a `seedFrom` that is not a
map of `name` and `key`, a name that is not a Secret name, a key that is not
a Secret key, and `seedFrom` on a placeholder; and (SEC-228) a `source`
other than `generated` and `external`, `source` or `writer` on a mounted
value, `writer` on a generated value or on a joined one, an external value
without `writer` (or with one that is blank, not a string or not printable),
with `length`, `symbols`, `sharedAcrossRegions`, `mountedCopies` or
`seedFrom` (SEC-254), and a join of a value of the other source.

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
  Job to OpenBao through bp-plane-isolation. A Job with a seed also dials the
  Kubernetes API (`kubernetes.default.svc:443`); where egress is closed it
  fails with exit 1 and names the Secret it could not read.
- **A Secret that Helm kept** (SEC-228, fact X3 of the spike SEC-227). A
  roll-out that stops rendering a Secret with `helm.sh/resource-policy: keep`
  and renders a synced value of the same name: what External Secrets 0.10.7
  (the version `platform/external-secrets/chart` pins) then does, tested by
  `chart/tests/eso-takeover/` in its own envtest suite:
  - it **takes the object over**: the same object (uid), its controller owner
    the ExternalSecret; the data is the store's keys only (a key only Helm
    wrote is dropped), or with `template.data` the template's keys over the
    old data (a key only Helm wrote stays); Helm's labels and annotations,
    `keep` too, stay (T1, T2);
  - with a `template` its `type` is set on the object, and a Secret's type
    cannot change: a template without the kept Secret's own type (say
    `kubernetes.io/basic-auth`) is refused, the ExternalSecret is not ready
    and the Secret stays Helm's (T3, T4);
  - while the store's path is absent the ExternalSecret is not ready and
    writes nothing: a kept Secret keeps Helm's value, and none is created
    (T5, T6). This holds because the library sets no `deletionPolicy`
    (default `Retain`).

  Read, not tested (envtest runs no garbage collector): once taken over, the
  Secret is deleted with its ExternalSecret; `keep` guards it only from
  Helm. The first roll-out watches, per Secret: the ExternalSecret's Ready
  condition; the Secret's uid before and after (the same: taken over, not
  recreated) and its owner; its type; the keys left from Helm; and whether
  the value changed (a value the store holds that differs from Helm's
  reaches the pods only when they restart).

## Tests

- `chart/tests/render.sh` — every template, every refused input (helm, awk);
  R11 to R13 and F25 to F44 the template, copies and join of SEC-219; R14 and
  F46 to F51 the seedFrom of SEC-226; R15, R16 and F52 to F66 the source
  external of SEC-228; R17, F45, F67 and F68 the seedFrom of a synced value
  (SEC-254).
- `chart/tests/eso-takeover/run.sh <work directory>` — fact X3 (above):
  `x3_test.go` (T1 to T6) in the envtest suite of the pinned External
  Secrets. It builds External Secrets, so no workflow runs it: run it again
  when the pin moves.
- `chart/tests/birth-live.sh` — the birth script against two real OpenBao
  servers configured by bp-openbao's own reconciler: present in every region
  after one run, a second run changes nothing, the create-only rights, the
  failures that name a region, a named key, a placeholder that a later
  writer replaces and the Job never writes again, and a named namespace: a
  Job of the release's namespace is refused its path, the Job there writes it;
  L10 seedFrom against a Kubernetes API stand-in (a KV mount at the API's
  path, its token's policy the rendered Role less the Secret of the path
  that holds a value): seeded, fresh (43), already present and unread, a
  second run with the token revoked, and refused with nothing written (not
  even an absent path before the seed): a Secret without the key, an empty
  value, a value with a trailing newline, a 403; L11 seedFrom on a synced
  value (SEC-254): its seed Job writes below `secret/` with the rights
  bp-openbao renders, here and in the peer, and role `eso-ns` reads the seed.
