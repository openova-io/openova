{{- /*
OpenBao desired configuration — bp-openbao (ADR-0015, Refs #7067).

The ONE definition of every ACL policy, auth role, auth mount and identity
object this chart puts into OpenBao. templates/config-desired.yaml renders it
as the `openbao-config-desired` ConfigMap; the openbao-config reconciler
(templates/config-reconciler.yaml, files/openbao-config-reconcile.sh) applies
it continuously. Before 1.2.72 these writes lived in the one-shot
auth-bootstrap hook, which revokes its root token — so no later chart version
could ever change them on a running Sovereign. Now a chart bump IS the change.

What the store writes itself, once, on its first start (the initialize block
of templates/seal-config.yaml): the kubernetes auth mount and its config (the
reconciler logs in through it), and the openbao-config-admin policy and role
(openbao.configAdminPolicy below; the policy is also the first op, so a later
chart version changes it). The kv mount, the mounts of `engines` (SEC-190)
and the PKI mounts of `pki` (SEC-250) are secrets-mount ops below.

Ops, applied in order (policies before the roles that name them, #5829):
  write <api-path>          POST the payload (idempotent on every path used)
  mount <sys/auth/x>        enable the auth mount unless already enabled
  secrets-mount <sys/mounts/x>  enable the secrets mount unless already enabled
The reconciler replaces AUTH_ACCESSOR_KUBERNETES in every payload by the
accessor of its login mount.
An op may carry a probe URL: when it does not answer (a peer region not yet
reachable over the mesh) the op is skipped this tick and retried next tick.
*/ -}}

{{- /* SEC-080: the legacy cluster-wide stores read sso/, catalyst/ and the
apps prefix only, never the hand-over archive. */ -}}
{{- define "openbao.policy.external-secrets-read" -}}
path "{{ .kv }}/data/sso/*" {
  capabilities = ["read"]
}
path "{{ .kv }}/metadata/sso/*" {
  capabilities = ["read", "list"]
}
path "{{ .kv }}/data/catalyst/*" {
  capabilities = ["read"]
}
path "{{ .kv }}/metadata/catalyst/*" {
  capabilities = ["read", "list"]
}
path "{{ .kv }}/data/{{ .apps }}/*" {
  capabilities = ["read"]
}
path "{{ .kv }}/metadata/{{ .apps }}/*" {
  capabilities = ["read", "list"]
}
path "{{ .kv }}/data/catalyst/tofu-phase0-archive" {
  capabilities = ["deny"]
}
path "{{ .kv }}/metadata/catalyst/tofu-phase0-archive" {
  capabilities = ["deny"]
}
{{ end -}}

{{- /* #5375 newapi admin-token + ADR-0015 #7067 multi-region shared material. No delete. */ -}}
{{- define "openbao.policy.external-secrets-push" -}}
path "{{ .kv }}/data/catalyst/newapi/admin-token" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/metadata/catalyst/newapi/admin-token" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/data/{{ .apps }}/*" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/metadata/{{ .apps }}/*" {
  capabilities = ["create", "update", "read"]
}
{{ end -}}

{{- define "openbao.policy.sso-bridge-write" -}}
path "{{ .kv }}/data/sso/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
path "{{ .kv }}/metadata/sso/*" {
  capabilities = ["read", "list", "delete"]
}
{{ end -}}

{{- /* G91.openbao #2655 + #3150 — the sso-configure reconciler's grant. */ -}}
{{- define "openbao.policy.sso-configure-admin" -}}
path "sys/auth" { capabilities = ["read", "list"] }
path "sys/auth/oidc" { capabilities = ["create", "update", "delete", "sudo"] }
path "sys/auth/oidc/*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }
path "auth/oidc/*" { capabilities = ["create", "read", "update", "delete", "list"] }
path "sys/policies/acl/sso-operator-admin" { capabilities = ["create", "read", "update", "delete"] }
path "sys/policies/acl/sso-operator-read" { capabilities = ["create", "read", "update", "delete"] }
path "identity/group" { capabilities = ["create", "update"] }
path "identity/group/name/*" { capabilities = ["create", "read", "update"] }
path "identity/group-alias" { capabilities = ["create", "update"] }
path "identity/group-alias/id/*" { capabilities = ["create", "read", "update"] }
{{ end -}}

{{- /* #3376 catalyst-api handover-archive writer. */ -}}
{{- define "openbao.policy.catalyst-api-write" -}}
path "{{ .kv }}/data/catalyst/*" {
  capabilities = ["create", "read", "update", "list"]
}
path "{{ .kv }}/metadata/catalyst/*" {
  capabilities = ["read", "list"]
}
{{ end -}}

{{- /* The snapshot SAVE endpoint only: what the backup CronJob may do
(templates/backup-snapshot.yaml, SEC-180). */ -}}
{{- define "openbao.policy.openbao-snapshot" -}}
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}
{{ end -}}

{{- /* The restore drill's canary and nothing else (templates/restore-drill.yaml,
SEC-181). A token of token role sec-g10-drill holds only this policy. */ -}}
{{- define "openbao.policy.sec-g10-drill" -}}
path "{{ .kv }}/data/sec-g10-drill/canary" {
  capabilities = ["create", "update", "read"]
}
{{ end -}}

{{- /* ADR-0015 #7067 — ESO mints its push token for the peers. */ -}}
{{- define "openbao.policy.openbao-peer-token" -}}
path "identity/oidc/token/{{ .aud }}" {
  capabilities = ["read"]
}
{{ end -}}

{{- /* ADR-0015 #7067 — what a PEER region's token may do here. No delete. */ -}}
{{- define "openbao.policy.openbao-peer-push" -}}
path "{{ .kv }}/data/{{ .apps }}/*" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/metadata/{{ .apps }}/*" {
  capabilities = ["create", "update", "read"]
}
{{ end -}}

{{- /* SEC-190 — the policies of the generic roles. NS and SA (.ns, .sa) are
the identity templates of the caller's Kubernetes service account; the
reconciler puts the accessor of its login mount in place of
AUTH_ACCESSOR_KUBERNETES. One policy serves every namespace: never one per
namespace. A pod reads its own service account's subtree and its namespace's
shared subtree of mounted/. */ -}}
{{- define "openbao.policy.workload" -}}
path "mounted/data/{{ .ns }}/sa/{{ .sa }}/*" {
  capabilities = ["read"]
}
path "mounted/data/{{ .ns }}/shared/*" {
  capabilities = ["read"]
}
{{ end -}}

{{- /* The namespace's External Secrets identity: creates values below its own
namespace of mounted/ (never reads one back) and keeps its own namespace of
the apps prefix. */ -}}
{{- define "openbao.policy.eso-ns" -}}
path "mounted/data/{{ .ns }}/*" {
  capabilities = ["create"]
}
path "mounted/metadata/{{ .ns }}/*" {
  capabilities = ["read", "delete"]
}
path "{{ .kv }}/data/{{ .apps }}/{{ .ns }}/*" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/metadata/{{ .apps }}/{{ .ns }}/*" {
  capabilities = ["read", "list", "delete"]
}
{{ end -}}

{{- /* SEC-200 — the birth Job of a namespace (platform/secrets-lib): it
creates values below its own namespace of mounted/ and reads their metadata
(never a value). With peers it also mints the identity token of role
openbao-peer-birth-<its namespace>, which a peer region accepts for that
namespace only. */ -}}
{{- define "openbao.policy.secrets-birth" -}}
path "mounted/data/{{ .ns }}/*" {
  capabilities = ["create"]
}
path "mounted/metadata/{{ .ns }}/*" {
  capabilities = ["read"]
}
{{- if .peers }}
path "identity/oidc/token/openbao-peer-birth-{{ .ns }}" {
  capabilities = ["read"]
}
{{- end }}
{{ end -}}

{{- /* SEC-200 — what the birth Job of a PEER region may do here, one policy
per namespace of peerTrust.birthNamespaces: create below that namespace of
mounted/, nothing else (no read, no update, no list). */ -}}
{{- define "openbao.policy.secrets-birth-peer" -}}
path "mounted/data/{{ .birthNs }}/*" {
  capabilities = ["create"]
}
{{ end -}}

{{- /* The state key: what a minted token, or a Job that logs in itself, may do. */ -}}
{{- define "openbao.policy.tofu-state" -}}
path "transit/datakey/plaintext/{{ .key }}" {
  capabilities = ["update"]
}
path "transit/decrypt/{{ .key }}" {
  capabilities = ["update"]
}
{{ end -}}

{{- /* The one offline backup of the state key (docs/RUNBOOKS.md §2.17). */ -}}
{{- define "openbao.policy.tofu-state-backup" -}}
path "transit/backup/{{ .key }}" {
  capabilities = ["read"]
}
{{ end -}}

{{- /* External Secrets in crossplane-system mints the state token. */ -}}
{{- define "openbao.policy.tofu-state-mint" -}}
path "auth/token/create/tofu-state" {
  capabilities = ["update"]
}
{{ end -}}

{{- /* SEC-196 — the hand-over Job of the Sovereign-lifecycle area (service
account sovereign-ops, role sovereign-ops): writes and reads the hand-over
inputs below catalyst/tofu/ and the cloud credentials below catalyst/cloud/ of
the legacy prefix, reads the hand-over archive and deletes it (its metadata,
so every version). No list, no other value. Not the human group sovereign-ops
of sso-configure, which maps to policy sovereign-admin. */ -}}
{{- define "openbao.policy.sovereign-ops" -}}
path "{{ .kv }}/data/catalyst/tofu/*" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/data/catalyst/cloud/*" {
  capabilities = ["create", "update", "read"]
}
path "{{ .kv }}/data/catalyst/tofu-phase0-archive" {
  capabilities = ["read"]
}
path "{{ .kv }}/metadata/catalyst/tofu-phase0-archive" {
  capabilities = ["read", "delete"]
}
{{ end -}}

{{- /* The human administrators (groups sovereign-admins and sovereign-ops,
templates/sso-configure-deployment.yaml): read-mostly, no value of any
mount, no seal, no policy write. Break-glass is cluster administration. */ -}}
{{- define "openbao.policy.sovereign-admin" -}}
path "{{ .kv }}/metadata/*" {
  capabilities = ["read", "list"]
}
path "mounted/metadata/*" {
  capabilities = ["read", "list"]
}
path "sys/mounts" {
  capabilities = ["read"]
}
path "sys/auth" {
  capabilities = ["read"]
}
path "sys/policies/acl/*" {
  capabilities = ["read", "list"]
}
path "sys/leases/lookup/*" {
  capabilities = ["read", "list", "sudo"]
}
path "sys/leases/revoke/*" {
  capabilities = ["update"]
}
{{ end -}}

{{- /* SEC-192 — a producer identity (ADR-0018 S20), one per entry of the value
`producers`: create and update on each listed pattern below mounted/data/,
read and delete on its metadata, never a read of a value. */ -}}
{{- define "openbao.policy.producer" -}}
{{- range .paths -}}
path "mounted/data/{{ . }}" {
  capabilities = ["create", "update"]
}
path "mounted/metadata/{{ . }}" {
  capabilities = ["read", "delete"]
}
{{ end -}}
{{- end -}}

{{- /* SEC-192 — the named exception of ADR-0018 S20: the simulation back end
(service account axon of namespace axon) writes back and reads its own model
credential, and nothing else. */ -}}
{{- define "openbao.policy.axon-credential" -}}
path "mounted/data/axon/sa/axon/model-credential/*" {
  capabilities = ["create", "update", "read"]
}
path "mounted/metadata/axon/sa/axon/model-credential/*" {
  capabilities = ["read"]
}
{{ end -}}

{{- /* SEC-250 — what cert-manager may do as role cert-manager-pki: sign at the
two roles of this region's pki/, the ClusterIssuers openbao-internal and
sovereign-mesh-ca of bp-cert-manager-issuers. */ -}}
{{- define "openbao.policy.pki-sign-internal" -}}
path "pki/sign/internal-server" {
  capabilities = ["create", "update"]
}
path "pki/sign/mesh" {
  capabilities = ["create", "update"]
}
{{ end -}}

{{- /* SEC-250 — the PKI bootstrap Job (templates/pki-bootstrap-job.yaml), primary
region only: exactly the four paths it writes. It makes the root inside
pki-root/ and this region's key and signing request inside pki/, has the root
sign that request and sets the signed certificate in pki/. No glob, so neither
generate/exported (which returns the private key) nor cross-sign is granted.
The Job also lists issuers/ of both mounts; the PKI engine serves that list
without a token check (OpenBao 2.7.2 builtin/logical/pki/backend.go, the
Unauthenticated paths), so it needs no grant here. */ -}}
{{- define "openbao.policy.pki-bootstrap" -}}
{{- range list "pki-root/root/generate/internal" "pki-root/root/sign-intermediate" "pki/intermediate/generate/internal" "pki/intermediate/set-signed" -}}
path "{{ . }}" {
  capabilities = ["create", "update"]
}
{{ end -}}
{{- end -}}

{{- /* SEC-250 — the PKI of this region, from the value `pki`, as JSON:
enabled (pki.enabled and kubernetesAuth.enabled), primary (pki.regionRole is
primary: this region holds the root in pki-root/ and signs its own
intermediate), and the lifetimes of the root and the intermediate, which are
also the largest lifetimes of their mounts. With pki.enabled, a regionRole that
is neither primary nor secondary fails the render, the empty one included: an
unset role never becomes primary, so a secondary region never makes a root of
its own. */ -}}
{{- define "openbao.pki" -}}
{{- $p := .Values.pki | default dict -}}
{{- $role := $p.regionRole | default "" | toString -}}
{{- if and $p.enabled (not (has $role (list "primary" "secondary"))) }}{{ fail (printf "pki.regionRole: %q is neither primary nor secondary" $role) }}{{ end -}}
{{- $on := false -}}
{{- if and $p.enabled (.Values.kubernetesAuth | default dict).enabled }}{{ $on = true }}{{ end -}}
{{- dict "enabled" $on "primary" (eq $role "primary") "rootTTL" "87600h" "intermediateTTL" "8760h" | toJson -}}
{{- end -}}

{{- /*
openbao.configAdminPolicy — the reconciler's own grant, written by the
self-initialisation (the only privileged writer). Configuration surfaces
only: policies, auth mounts + their roles/config, identity OIDC. No secret
data, no sys/raw, no seal, no token creation, no delete. It may rewrite its
own policy (the first op of the desired state) but not its own role (the
exact-path read-only entry wins over the wildcard), and it may enable only the
kv mount and the mounts of `engines` (SEC-190) and, with `pki` (SEC-250), pki/
and in the primary region pki-root/; it writes the roles of pki/, never a key,
an issuer or a certificate (the PKI bootstrap Job does that with its own role).
It creates and configures
transit keys and writes token roles (the state key and token role tofu-state,
SEC-190), but cannot use a key or back one up, and it creates a token only
through token role sec-g10-drill, for the restore drill (SEC-181). It CAN write
any policy and bind any role, so it is a privileged
writer by composition — that is what a declarative config owner is; the
narrowing keeps it off data and seal, and its only input is this chart.
*/ -}}
{{- define "openbao.configAdminPolicy" -}}
{{- $kv := (.Values.kubernetesAuth | default dict).kvMountPath | default "secret" -}}
path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policy/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/auth" { capabilities = ["read"] }
path "sys/auth/*" { capabilities = ["create", "read", "update", "sudo"] }
path "sys/mounts" { capabilities = ["read"] }
path "sys/mounts/{{ $kv }}" { capabilities = ["create", "update"] }
{{- range $name, $_ := (.Values.engines | default dict) }}
path "sys/mounts/{{ $name }}" { capabilities = ["create", "update"] }
{{- end }}
{{- $pki := include "openbao.pki" . | fromJson }}
{{- if $pki.enabled }}
path "sys/mounts/pki" { capabilities = ["create", "update"] }
{{- if $pki.primary }}
path "sys/mounts/pki-root" { capabilities = ["create", "update"] }
{{- end }}
path "pki/roles/*" { capabilities = ["create", "update", "read"] }
{{- end }}
path "transit/keys/*" { capabilities = ["create", "update", "read"] }
path "transit/keys/+/config" { capabilities = ["create", "update"] }
path "auth/token/roles/*" { capabilities = ["create", "update", "read"] }
{{- if (.Values.restoreDrill | default dict).enabled }}
path "auth/token/create/sec-g10-drill" { capabilities = ["update"] }
{{- end }}
path "auth/+/role/*" { capabilities = ["create", "read", "update", "list"] }
path "auth/+/role/openbao-config-admin" { capabilities = ["read"] }
path "auth/+/config" { capabilities = ["create", "read", "update"] }
path "identity/oidc/*" { capabilities = ["create", "read", "update", "list"] }
{{ end -}}

{{- /* The k8s-auth mount the in-namespace reconcilers log in through. */ -}}
{{- define "openbao.configLoginMount" -}}
{{- $kAuth := .Values.kubernetesAuth | default dict -}}
{{- $kAuth.mountPath | default "kubernetes" -}}
{{- end -}}

{{- /*
openbao.configOps — the ordered desired state as JSON
[{kind, path, probe, payload}].
*/ -}}
{{- define "openbao.configOps" -}}
{{- $kAuth := .Values.kubernetesAuth | default dict -}}
{{- $mount := $kAuth.mountPath | default "kubernetes" -}}
{{- $kv := $kAuth.kvMountPath | default "secret" -}}
{{- $ttl := $kAuth.tokenTTL | default "1h" -}}
{{- $maxTTL := $kAuth.tokenMaxTTL | default "24h" -}}
{{- $esoRole := $kAuth.role | default "external-secrets" -}}
{{- $esoSA := $kAuth.serviceAccountName | default "external-secrets" -}}
{{- $esoNs := $kAuth.serviceAccountNamespace | default "external-secrets-system" -}}
{{- $capi := $kAuth.catalystApi | default dict -}}
{{- $cfg := .Values.peerTrust | default dict -}}
{{- $aud := $cfg.audience | default "openbao-peer-push" -}}
{{- $apps := $cfg.kvPathPrefix | default "openova/apps" | trimSuffix "/" -}}
{{- $pt := include "openbao.peerTrust" . | fromJson -}}
{{- $engines := .Values.engines | default dict -}}
{{- $pki := include "openbao.pki" . | fromJson -}}
{{- $sk := .Values.stateKey | default dict -}}
{{- $key := $sk.name | default "tofu-state" -}}
{{- /* Helm string literals: Helm never reads them as actions (SEC-190). */ -}}
{{- $idNs := "{{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES.metadata.service_account_namespace}}" -}}
{{- $idSa := "{{identity.entity.aliases.AUTH_ACCESSOR_KUBERNETES.metadata.service_account_name}}" -}}
{{- $ctx := dict "kv" $kv "apps" $apps "aud" $aud "ns" $idNs "sa" $idSa "key" $key "peers" $pt.peers -}}
{{- /* SEC-200: the namespaces whose birth Job writes shared values into the
peers; each gets one role on every jwt-peer-<peer> mount. */ -}}
{{- $birthNs := list -}}
{{- range ($cfg.birthNamespaces | default list) -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString .)) }}{{ fail (printf "peerTrust.birthNamespaces: %q is not a namespace name" (toString .)) }}{{ end -}}
{{- $birthNs = append $birthNs (toString .) -}}
{{- end -}}
{{- $birthNs = $birthNs | uniq | sortAlpha -}}
{{- $ns := .Release.Namespace -}}
{{- /* SEC-192: the producer identities of the value `producers`, each pattern
in the path grammar that values.yaml states; anything else fails the render. */ -}}
{{- $dns := `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` -}}
{{- $grammar := `^(\+|[a-z0-9]([-a-z0-9]*[a-z0-9])?)/(sa/(\+|[a-z0-9]([-a-z0-9]*[a-z0-9])?)|shared)/[a-z0-9-]+(\*|/\*|/e[1-9][0-9]*)$` -}}
{{- $producers := list -}}
{{- $seen := dict -}}
{{- range .Values.producers -}}
{{- $n := toString .name -}}
{{- if not (regexMatch "^[a-z0-9-]+$" $n) }}{{ fail (printf "producers: name %q is not [a-z0-9-]+" $n) }}{{ end -}}
{{- if hasKey $seen $n }}{{ fail (printf "producers: %q is listed twice" $n) }}{{ end -}}
{{- $_ := set $seen $n true -}}
{{- if not (and (regexMatch $dns (toString .serviceAccount)) (regexMatch $dns (toString .namespace))) }}{{ fail (printf "producers: %q needs a serviceAccount and a namespace" $n) }}{{ end -}}
{{- if not .paths }}{{ fail (printf "producers: %q has no paths" $n) }}{{ end -}}
{{- range .paths -}}
{{- if not (regexMatch $grammar (toString .)) }}{{ fail (printf "producers: %q: pattern %q is not in the path grammar of mounted/" $n (toString .)) }}{{ end -}}
{{- end -}}
{{- $producers = append $producers (dict "name" $n "sa" (toString .serviceAccount) "ns" (toString .namespace) "paths" .paths) -}}
{{- end -}}

{{- $policies := list "external-secrets-read" "external-secrets-push" "sso-bridge-write" "sso-configure-admin" -}}
{{- if $capi.enabled }}{{ $policies = append $policies "catalyst-api-write" }}{{ end -}}
{{- if $pt.peers }}{{ $policies = concat $policies (list "openbao-peer-token" $aud) }}{{ end -}}
{{- $policies = concat $policies (list "workload" "eso-ns" "tofu-state" "tofu-state-backup" "tofu-state-mint" "sovereign-ops" "sovereign-admin" "secrets-birth") -}}
{{- $bk := .Values.backup | default dict -}}
{{- if $bk.enabled }}{{ $policies = append $policies "openbao-snapshot" }}{{ end -}}
{{- $drill := (.Values.restoreDrill | default dict).enabled -}}
{{- if $drill }}{{ $policies = append $policies "sec-g10-drill" }}{{ end -}}
{{- if $pki.enabled }}{{ $policies = append $policies "pki-sign-internal" }}{{ end -}}
{{- if and $pki.enabled $pki.primary }}{{ $policies = append $policies "pki-bootstrap" }}{{ end -}}

{{- /* role: name, sa (one name or a list), ns (one or a list), policies, ttl,
maxTTL, aud (the audience a service-account token must carry; none = any). */ -}}
{{- $roles := list
  (dict "name" $esoRole "sa" $esoSA "ns" $esoNs "policies" (list "external-secrets-read" "external-secrets-push") "ttl" $ttl "max" $maxTTL)
  (dict "name" "sso-bridge" "sa" "sso-bridge-reconciler" "ns" "sso-bridge" "policies" (list "sso-bridge-write") "ttl" $ttl "max" $maxTTL)
  (dict "name" "sso-configure" "sa" "openbao-sso-configure" "ns" $ns "policies" (list "sso-configure-admin") "ttl" $ttl "max" $maxTTL) -}}
{{- if $capi.enabled -}}
{{- $roles = append $roles (dict "name" ($capi.role | default "catalyst-api") "sa" ($capi.serviceAccountName | default "catalyst-api-cutover-driver") "ns" ($capi.serviceAccountNamespace | default "catalyst-system") "policies" (list "catalyst-api-write") "ttl" $ttl "max" $maxTTL) -}}
{{- end -}}
{{- if $pt.peers -}}
{{- $roles = append $roles (dict "name" "openbao-peer-token" "sa" $esoSA "ns" $esoNs "policies" (list "openbao-peer-token") "ttl" "15m" "max" "1h") -}}
{{- end -}}
{{- /* SEC-190 generic roles. workload: any pod, with a token for audience openbao. */ -}}
{{- $roles = concat $roles (list
  (dict "name" "workload" "sa" "*" "ns" "*" "policies" (list "workload") "ttl" "15m" "max" "1h" "aud" "openbao")
  (dict "name" "eso-ns" "sa" "eso-reader" "ns" "*" "policies" (list "eso-ns") "ttl" "15m" "max" "1h")
  (dict "name" "tofu-state-mint" "sa" "eso-reader" "ns" "crossplane-system" "policies" (list "eso-ns" "tofu-state-mint") "ttl" "15m" "max" "1h")) -}}
{{- /* SEC-200: the birth Job of any namespace (service account secrets-birth). */ -}}
{{- $roles = append $roles (dict "name" "secrets-birth" "sa" "secrets-birth" "ns" "*" "policies" (list "secrets-birth") "ttl" "15m" "max" "1h" "aud" "openbao") -}}
{{- /* SEC-180: the backup CronJob (templates/backup-snapshot.yaml), with a token
for audience openbao; rendered with it, when backup.enabled. */ -}}
{{- if $bk.enabled -}}
{{- $roles = append $roles (dict "name" "openbao-snapshot" "sa" "openbao-backup" "ns" $ns "policies" (list "openbao-snapshot") "ttl" "15m" "max" "15m" "aud" "openbao") -}}
{{- end -}}
{{- /* tofu-state and tofu-state-backup bind the service accounts of
stateKey.jobIdentities and .backupIdentities; an empty list renders no role. */ -}}
{{- range $role, $ids := dict "tofu-state" ($sk.jobIdentities | default list) "tofu-state-backup" ($sk.backupIdentities | default list) -}}
{{- if $ids -}}
{{- $sas := list -}}{{- $nss := list -}}
{{- range $ids }}{{ $sas = append $sas .serviceAccount }}{{ $nss = append $nss .namespace }}{{ end -}}
{{- $roles = append $roles (dict "name" $role "sa" (uniq $sas) "ns" (uniq $nss) "policies" (list $role) "ttl" "15m" "max" (ternary "15m" "1h" (eq $role "tofu-state-backup")) "aud" "openbao") -}}
{{- end -}}
{{- end -}}
{{- /* SEC-196: the hand-over Job, service account sovereign-ops in the
namespace of the first job identity, with its own policy and the state key's.
As for tofu-state, an empty jobIdentities renders no role; a first entry
without a namespace fails the render. */ -}}
{{- with ($sk.jobIdentities | default list) -}}
{{- $hns := toString ((index . 0).namespace) -}}
{{- if not (regexMatch $dns $hns) }}{{ fail (printf "stateKey.jobIdentities: the first entry has no namespace for role sovereign-ops (got %q)" $hns) }}{{ end -}}
{{- $roles = append $roles (dict "name" "sovereign-ops" "sa" "sovereign-ops" "ns" $hns "policies" (list "sovereign-ops" "tofu-state") "ttl" "15m" "max" "1h" "aud" "openbao") -}}
{{- end -}}
{{- /* SEC-192: one role per producer identity, and the simulation back end's
own role axon-credential (15 minutes, at most 15 minutes). */ -}}
{{- range $producers -}}
{{- $roles = append $roles (dict "name" (printf "producer-%s" .name) "sa" .sa "ns" .ns "policies" (list (printf "producer-%s" .name)) "ttl" "15m" "max" "1h" "aud" "openbao") -}}
{{- end -}}
{{- $roles = append $roles (dict "name" "axon-credential" "sa" "axon" "ns" "axon" "policies" (list "axon-credential") "ttl" "15m" "max" "15m" "aud" "openbao") -}}
{{- /* SEC-250: cert-manager's own service account, with the token it asks for
each ClusterIssuer of bp-cert-manager-issuers: audiences vault://<issuer> and
openbao (serviceAccountRef.audiences). The role requires openbao, so a token
cert-manager asked for another audience is refused. In the primary region also
the PKI bootstrap Job. */ -}}
{{- if $pki.enabled -}}
{{- $roles = append $roles (dict "name" "cert-manager-pki" "sa" "cert-manager" "ns" "cert-manager" "policies" (list "pki-sign-internal") "ttl" "15m" "max" "1h" "aud" "openbao") -}}
{{- if $pki.primary -}}
{{- $roles = append $roles (dict "name" "pki-bootstrap" "sa" "openbao-pki-bootstrap" "ns" $ns "policies" (list "pki-bootstrap") "ttl" "15m" "max" "1h" "aud" "openbao") -}}
{{- end -}}
{{- end -}}

{{- $ops := list
  (dict "kind" "write" "path" "sys/policies/acl/openbao-config-admin" "payload" (dict "policy" (include "openbao.configAdminPolicy" $)))
  (dict "kind" "secrets-mount" "path" (printf "sys/mounts/%s" $kv) "payload" (dict "type" "kv" "options" (dict "version" "2"))) -}}
{{- range $name, $spec := $engines -}}
{{- $ops = append $ops (dict "kind" "secrets-mount" "path" (printf "sys/mounts/%s" $name) "payload" $spec) -}}
{{- end -}}
{{- /* SEC-250: pki/ in every region, pki-root/ in the primary region only. */ -}}
{{- if $pki.enabled -}}
{{- $ops = append $ops (dict "kind" "secrets-mount" "path" "sys/mounts/pki" "payload" (dict "type" "pki" "config" (dict "max_lease_ttl" $pki.intermediateTTL))) -}}
{{- if $pki.primary -}}
{{- $ops = append $ops (dict "kind" "secrets-mount" "path" "sys/mounts/pki-root" "payload" (dict "type" "pki" "config" (dict "max_lease_ttl" $pki.rootTTL))) -}}
{{- end -}}
{{- end -}}
{{- range $policies -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "sys/policies/acl/%s" .) "payload" (dict "policy" (include (printf "openbao.policy.%s" (ternary "openbao-peer-push" . (eq . $aud))) $ctx))) -}}
{{- end -}}
{{- if $pt.peers -}}
{{- range $birthNs -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "sys/policies/acl/secrets-birth-peer-%s" .) "payload" (dict "policy" (include "openbao.policy.secrets-birth-peer" (dict "birthNs" .)))) -}}
{{- end -}}
{{- end -}}
{{- range $producers -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "sys/policies/acl/producer-%s" .name) "payload" (dict "policy" (include "openbao.policy.producer" .))) -}}
{{- end -}}
{{- $ops = append $ops (dict "kind" "write" "path" "sys/policies/acl/axon-credential" "payload" (dict "policy" (include "openbao.policy.axon-credential" $ctx))) -}}
{{- /* The state key: exportable and with a plaintext backup allowed (both
one-way), so that the one offline backup can be taken; never deletable. */ -}}
{{- if hasKey $engines "transit" -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "transit/keys/%s" $key) "payload" (dict "type" "aes256-gcm96" "exportable" true "allow_plaintext_backup" true)) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "transit/keys/%s/config" $key) "payload" (dict "deletion_allowed" false "exportable" true "allow_plaintext_backup" true)) -}}
{{- end -}}
{{- /* SEC-250: the two roles of pki/. internal-server signs names below
svc.cluster.local and cluster.local for at most 30 days; mesh signs any name
for at most 90 days. Both sign server and client certificates and take a
request without a common name (a cert-manager Certificate that names only
dnsNames). key_type any: both sign every key cert-manager makes, the RSA 2048
key of a Certificate that names no key (cert-manager's default) as well as
ECDSA P-256, the size to ask for; OpenBao still refuses RSA below 2048 bits and
EC below P-224. A ClusterIssuer has no field for the key, so the roles are
the side that agrees. */ -}}
{{- if $pki.enabled -}}
{{- $ops = append $ops (dict "kind" "write" "path" "pki/roles/internal-server" "payload" (dict "allowed_domains" (list "svc.cluster.local" "cluster.local") "allow_subdomains" true "server_flag" true "client_flag" true "max_ttl" "720h" "key_type" "any" "require_cn" false)) -}}
{{- $ops = append $ops (dict "kind" "write" "path" "pki/roles/mesh" "payload" (dict "allow_any_name" true "server_flag" true "client_flag" true "max_ttl" "2160h" "key_type" "any" "require_cn" false)) -}}
{{- end -}}
{{- range $roles -}}
{{- $sas := .sa }}{{ if kindIs "string" .sa }}{{ $sas = list .sa }}{{ end -}}
{{- $nss := .ns }}{{ if kindIs "string" .ns }}{{ $nss = list .ns }}{{ end -}}
{{- $body := dict "bound_service_account_names" $sas "bound_service_account_namespaces" $nss "policies" .policies "ttl" .ttl "max_ttl" .max -}}
{{- if .aud }}{{ $_ := set $body "audience" .aud }}{{ end -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/role/%s" $mount .name) "payload" $body) -}}
{{- end -}}
{{- /* Token role tofu-state: what External Secrets mints every 15 minutes for
the provider pod (an orphan, not renewable, at most 1h, policy tofu-state). */ -}}
{{- $ops = append $ops (dict "kind" "write" "path" "auth/token/roles/tofu-state" "payload" (dict "allowed_policies" (list "tofu-state") "orphan" true "renewable" false "token_explicit_max_ttl" "1h")) -}}
{{- /* Token role sec-g10-drill: the drill's token, minted by this identity; a
child of the drill's login, so revoking that login revokes it; no default
policy, not renewable, at most 15 minutes. */ -}}
{{- if $drill -}}
{{- $ops = append $ops (dict "kind" "write" "path" "auth/token/roles/sec-g10-drill" "payload" (dict "allowed_policies" (list "sec-g10-drill") "orphan" false "renewable" false "token_explicit_max_ttl" "15m" "token_no_default_policy" true)) -}}
{{- end -}}
{{- if $pt.peers -}}
{{- $self := $pt.self -}}
{{- $clients := list $aud -}}
{{- range $birthNs }}{{ $clients = append $clients (printf "openbao-peer-birth-%s" .) }}{{ end -}}
{{- $ops = append $ops (dict "kind" "write" "path" "identity/oidc/config" "payload" (dict "issuer" (printf "http://%s:8200" (index $pt.hosts $self)))) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "identity/oidc/key/%s" $aud) "payload" (dict "algorithm" "RS256" "allowed_client_ids" $clients "rotation_period" "24h" "verification_ttl" "24h")) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "identity/oidc/role/%s" $aud) "payload" (dict "key" $aud "client_id" $aud "ttl" ($cfg.tokenTTL | default "1h") "template" (dict "openova_region" $self | toJson))) -}}
{{- /* SEC-200: one identity role per namespace; the token names the
namespace and this region, and only that namespace's Job may mint it. */ -}}
{{- range $birthNs -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "identity/oidc/role/openbao-peer-birth-%s" .) "payload" (dict "key" $aud "client_id" (printf "openbao-peer-birth-%s" .) "ttl" "15m" "template" (dict "openova_namespace" . "openova_region" $self | toJson))) -}}
{{- end -}}
{{- range $pt.peers -}}
{{- $issuer := printf "http://%s:8200/v1/identity/oidc" (index $pt.hosts .) -}}
{{- $probe := printf "%s/.well-known/openid-configuration" $issuer -}}
{{- $m := printf "jwt-peer-%s" . -}}
{{- $ops = append $ops (dict "kind" "mount" "path" (printf "sys/auth/%s" $m) "probe" $probe "payload" (dict "type" "jwt" "description" "ADR-0015 peer-region ESO push (bp-openbao)")) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/config" $m) "probe" $probe "payload" (dict "oidc_discovery_url" $issuer)) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/role/%s" $m $aud) "probe" $probe "payload" (dict "role_type" "jwt" "bound_audiences" (list $aud) "user_claim" "sub" "bound_claims" (dict "openova_region" .) "token_policies" (list $aud) "token_ttl" "15m" "token_max_ttl" "1h")) -}}
{{- $peer := . -}}
{{- range $birthNs -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/role/secrets-birth-%s" $m .) "probe" $probe "payload" (dict "role_type" "jwt" "bound_audiences" (list (printf "openbao-peer-birth-%s" .)) "user_claim" "sub" "bound_claims" (dict "openova_region" $peer "openova_namespace" .) "token_policies" (list (printf "secrets-birth-peer-%s" .)) "token_ttl" "15m" "token_max_ttl" "1h")) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $ops | toJson -}}
{{- end -}}
