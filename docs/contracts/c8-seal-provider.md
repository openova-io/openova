# Contract C8: seal provider and k3s bootstrap

This is contract C8 of the index `docs/contracts/README.md`. Area SEC (secrets and trust) owns it, and only that area changes this file. It states a target that work packages build against. Part 1 is the seal-provider contract: how a provider pack gives OpenBao its seal, so that the key that opens OpenBao is never a Secret object and never rests in etcd. Part 2 is the k3s bootstrap contract: the flags and the tokens that every k3s server and worker starts with. In this file the reference cloud is the pack `huawei`.

## Part 1: seal-provider contract

A seal hook is an OpenTofu child module at `infra/providers/<name>/seal/`, called from the provider's `main.tf` as `module "seal"`. It produces a seal stanza, a list of node files and a volume device. Each provider pack carries one hook.

Ranked implementations. A pack implements exactly one.

| Rank | `kind` | Where the key rests | Use when |
|---|---|---|---|
| 1 | `cloud-key-service` | In the cloud's key service. The node holds only a credential that may ask the service to encrypt and decrypt with one key | The cloud has a key service and a mechanism OpenBao can use is proven (SEC-135 for the reference cloud) |
| 2 | `static-node-file` | 32 bytes in a file on a dedicated volume of each control-plane node, and in the Sovereign's encrypted infrastructure state | Every other case, including the hermetic local provider pack |

Cloud profile section. The profile is the data file `infra/providers/<name>/profile.yaml` of each pack. Area CLD (cloud abstraction) owns the file and its schema `infra/providers/_contract/cloudprofile.schema.json`, and reserves `spec.seal` for area SEC. Package CLD-092 adds the one field below to the schema and to each pack's profile, together with its reader: the provider conformance test compares the field with the hook's output `kind` and fails when they differ.

```yaml
spec:
  seal:
    kind: static-node-file   # string, required; enum: static-node-file | cloud-key-service. Must equal the hook's output "kind".
```

No other profile field belongs to this contract. A section `spec.controlPlaneVolumeEncryption` is not added: it has no reader, and no implementation is scheduled.

Hook module inputs (every hook MUST declare exactly these):

| Name | Type | Required | Meaning |
|---|---|---|---|
| `region_keys` | `set(string)` | yes | The provider module's own region keys (one seal key per region) |
| `seal_key_generation` | `number` | no, default `1` | Raising it rotates the key (see "Key rotation") |

Hook module outputs (every hook MUST produce exactly these):

| Name | Type | Sensitive | Meaning |
|---|---|---|---|
| `kind` | `string` | no | `static-node-file` or `cloud-key-service` |
| `stanza_by_region` | `map(string)` | no | The complete HCL `seal "<type>" { ... }` block for each region key. It contains file references, never key bytes |
| `node_files_by_region` | `map(list(object({path=string, owner=string, permissions=string, content_b64=string})))` | yes | Files cloud-init writes on control-plane nodes only. `path` MUST start with `/var/lib/catalyst/seal/` |
| `volume_device` | `string` | no | Block device of the dedicated seal volume as the node sees it at first boot, such as `/dev/vdb`. Empty string means the pack has no separate volume and the directory is on the system disk |

The provider module's `main.tf` adds, beside the hook call: `random_password.k3s_server_token` and `random_password.k3s_agent_token` (each `for_each` over the region keys, `length = 64`, `special = false`), and, when `volume_device` is not empty, one data disk of the smallest size the cloud sells on every control-plane instance.

The `static-node-file` hook is exactly this file (`infra/providers/<name>/seal/main.tf`); only the value of the last output differs per pack:

```hcl
terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}
variable "region_keys" { type = set(string) }
variable "seal_key_generation" {
  type    = number
  default = 1
}
resource "random_bytes" "seal" {
  for_each = var.region_keys
  length   = 32
  keepers  = { generation = var.seal_key_generation }
}
locals {
  key_id = { for k in var.region_keys : k => nonsensitive(substr(sha256(random_bytes.seal[k].base64), 0, 16)) }
}
output "kind" { value = "static-node-file" }
output "stanza_by_region" {
  value = { for k in var.region_keys : k => <<-EOT
    seal "static" {
      current_key_id = "${local.key_id[k]}"
      current_key    = "file:///openbao/seal/openbao-seal-${local.key_id[k]}.key"
    }
  EOT
  }
}
output "node_files_by_region" {
  sensitive = true
  value = { for k in var.region_keys : k => [{
    path        = "/var/lib/catalyst/seal/openbao-seal-${local.key_id[k]}.key"
    owner       = "100:1000"
    permissions = "0400"
    content_b64 = random_bytes.seal[k].base64
  }] }
}
output "volume_device" { value = "/dev/vdb" } # reference cloud. "" for a pack with no separate volume.
```

The key identifier is the first 16 hexadecimal characters of the SHA-256 of the key's base64 text. It is not secret. The hook marks it `nonsensitive`; without that call OpenTofu treats the stanza as sensitive, because the provider marks the key bytes sensitive. The text above is formatted as `tofu fmt` writes it.

Node file format: exactly 32 raw bytes, no newline (the upstream static seal reads a 32-byte key from a `file://` reference). Directory `/var/lib/catalyst/seal` is mode 0700, owner `100:1000`, the user and group the OpenBao container runs as; SEC-130 confirms the two numbers from the rendered StatefulSet before SEC-150 merges. The OpenBao chart mounts that directory read-only at `/openbao/seal` in the server pod (SEC-162), so a file reference in a stanza is `file:///openbao/seal/<base name of the node file>`.

Shared template arguments (the template names no provider; it only uses these):

| Argument | Type | Meaning |
|---|---|---|
| `k3s_server_token` | string | `K3S_TOKEN` of the server |
| `k3s_agent_token` | string | `--agent-token` of the server; `K3S_TOKEN` of workers |
| `seal_kind` | string | Hook output `kind` |
| `seal_stanza` | string | Hook output `stanza_by_region[<this region>]` |
| `seal_node_files` | list of objects | Hook output `node_files_by_region[<this region>]` |
| `seal_volume_device` | string | Hook output `volume_device` |

What the shared template does with them, in this order. Actions 1 to 3 run before the k3s install line.

1. `write_files`: each node file is written to `/run/catalyst-seal-stage/<base name of path>` with `encoding: b64`, permissions `'0400'`. `/run` is memory-backed, so the key never touches the system disk.
2. `runcmd`, only when `seal_volume_device` is not empty: `[ -b <device> ] || exit 96`; `blkid <device> >/dev/null 2>&1 || mkfs.ext4 -q -L catalyst-seal <device>`; append `LABEL=catalyst-seal /var/lib/catalyst/seal ext4 defaults,nodev,nosuid,noexec 0 2` to `/etc/fstab` unless a line with `catalyst-seal` is present; `mkdir -p /var/lib/catalyst/seal && mount /var/lib/catalyst/seal`.
3. `runcmd`: `install -d -m 0700 -o 100 -g 1000 /var/lib/catalyst/seal`; for each node file `install -m <permissions> -o <owner user> -g <owner group> /run/catalyst-seal-stage/<base name> <path>`; `rm -rf /run/catalyst-seal-stage`.
4. `write_files` writes one ConfigMap manifest, and the template's existing `kubectl apply` line for its bootstrap manifests applies it. That line runs after Flux is installed, so the namespace exists. It is a ConfigMap, not a Secret: it holds no key bytes.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sovereign-seal-profile
  namespace: flux-system
data:
  kind: static-node-file
  stanza: |
    seal "static" {
      current_key_id = "3f9a1c0b7d2e4a61"
      current_key    = "file:///openbao/seal/openbao-seal-3f9a1c0b7d2e4a61.key"
    }
```

Errors: the `runcmd` script exits with code 96 when the seal volume device is not present; the node never starts k3s. The code 96 is reserved for this step: the shared template uses codes from 86 to 95 for its other fatal steps. An OpenBao pod on a node whose seal directory is empty stays sealed and not Ready (fail closed).

Reserved for `cloud-key-service`: three more ConfigMap keys, `pluginStanza` (the `plugin "kms" "<name>" { command = "..." }` block), `pluginImage` (an image that carries the plug-in binary) and `pluginCommand` (its file name). They are added, together with their reader in the chart, only by the package that implements the kind (SEC-155). No other package may add them.

Key rotation (static kind): raising `seal_key_generation` creates a new key. The hook then emits `previous_key_id` and `previous_key` in the stanza and a second node file, per the upstream rule (rotation from key n-1 to key n; the identifier must always match its key). The rotation variant of the hook is written by the first package that performs a rotation, not before.

Residual risk, stated. Kind `static-node-file`: a party with root on a control-plane node, read access to the instance user data, or read access to the decrypted infrastructure state obtains the seal key; with the seal key and a copy of the OpenBao volume or snapshot they can decrypt the store. A copy of the system disk alone no longer carries the key when `volume_device` is set. Kind `cloud-key-service`: a party with root on a control-plane node can use the node's credential to ask the key service to decrypt while the credential is valid; the key itself never leaves the service, the credential can be revoked, and every use is in the cloud's audit trail.

## Part 2: k3s bootstrap contract (shared template)

The server command line MUST contain these four flags, besides its other flags. `--write-kubeconfig-mode` is set once, to `0600`.

```
--secrets-encryption --secrets-encryption-provider=secretbox --agent-token=${k3s_agent_token} --write-kubeconfig-mode=0600
```

The server runs with `K3S_TOKEN=${k3s_server_token}`. Workers MUST use `K3S_TOKEN=${k3s_agent_token}`. Minimum k3s version `v1.31.8+k3s1`.

Where the etcd key rests: `/var/lib/rancher/k3s/server/cred/encryption-config.json` on every server node, and a copy inside etcd's bootstrap record encrypted with a key derived from the server token (k3s token documentation). So the server token is the key-encryption secret, and it rests outside the cluster's data: on server node disks, in instance user data and in the infrastructure state.

Rotation: `k3s secrets-encrypt rotate-keys` on the first server, wait for `Current Rotation Stage: reencrypt_finished` in `k3s secrets-encrypt status`, then restart the servers one at a time (k3s documentation). Yearly, and after any suspected node compromise.

Why not in-cluster transit: the API server must decrypt Secrets to start the pods that would run the key provider. That is a cold-start loop.
