{{- /*
OpenBao desired configuration — bp-openbao (ADR-0015, Refs #7067).

The ONE definition of every ACL policy, auth role, auth mount and identity
object this chart puts into OpenBao. templates/config-desired.yaml renders it
as the `openbao-config-desired` ConfigMap; the openbao-config reconciler
(templates/config-reconciler.yaml, files/openbao-config-reconcile.sh) applies
it continuously. Before 1.2.72 these writes lived in the one-shot
auth-bootstrap hook, which revokes its root token — so no later chart version
could ever change them on a running Sovereign. Now a chart bump IS the change.

What stays in auth-bootstrap (needs root, happens once, or cannot be done by
the reconciler without locking itself out): the kubernetes auth mount + its
TokenReview config (the reconciler logs in through it), the kv mount
(`sys/mounts` is read-only for the reconciler), the prov-time KV seed (data,
never config), and the `openbao-config-admin` policy + role themselves
(openbao.configAdminPolicy below).

Ops, applied in order (policies before the roles that name them, #5829):
  write <api-path>          POST the payload (idempotent on every path used)
  mount <sys/auth/x>        enable the auth mount unless already enabled
An op may carry a probe URL: when it does not answer (a peer region not yet
reachable over the mesh) the op is skipped this tick and retried next tick.
*/ -}}

{{- define "openbao.policy.external-secrets-read" -}}
path "{{ .kv }}/data/*" {
  capabilities = ["read", "list"]
}
path "{{ .kv }}/metadata/*" {
  capabilities = ["read", "list"]
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

{{- /* #3375 DoD-8 snapshot SAVE endpoint only. */ -}}
{{- define "openbao.policy.openbao-snapshot" -}}
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
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

{{- /*
openbao.configAdminPolicy — the reconciler's own grant, written by the
auth-bootstrap hook (the only privileged writer left). Configuration surfaces
only: policies, auth mounts + their roles/config, identity OIDC. No secret
data, no sys/raw, no seal, no token creation, no delete. It may not rewrite
its own policy or role (the exact-path read-only entries win over the
wildcards). It CAN write any policy and bind any role, so it is a privileged
writer by composition — that is what a declarative config owner is; the
narrowing keeps it off data and seal, and its only input is this chart.
*/ -}}
{{- define "openbao.configAdminPolicy" -}}
path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policy/*" { capabilities = ["create", "read", "update", "list"] }
path "sys/policies/acl/openbao-config-admin" { capabilities = ["read"] }
path "sys/policy/openbao-config-admin" { capabilities = ["read"] }
path "sys/auth" { capabilities = ["read"] }
path "sys/auth/*" { capabilities = ["create", "read", "update", "sudo"] }
path "sys/mounts" { capabilities = ["read"] }
path "auth/+/role/*" { capabilities = ["create", "read", "update", "list"] }
path "auth/+/role/openbao-config-admin" { capabilities = ["read"] }
path "auth/+/config" { capabilities = ["create", "read", "update"] }
path "identity/oidc/*" { capabilities = ["create", "read", "update", "list"] }
{{ end -}}

{{- /* The k8s-auth mount the in-namespace reconcilers log in through. */ -}}
{{- define "openbao.configLoginMount" -}}
{{- $kAuth := (.Values.autoUnseal | default dict).kubernetesAuth | default dict -}}
{{- if (($kAuth.crossClusterTokenReview | default dict).enabled | default false) -}}kubernetes-vcluster{{- else -}}{{ $kAuth.mountPath | default "kubernetes" }}{{- end -}}
{{- end -}}

{{- /*
openbao.configOps — the ordered desired state as JSON
[{kind, path, probe, payload}].
*/ -}}
{{- define "openbao.configOps" -}}
{{- $au := .Values.autoUnseal | default dict -}}
{{- $kAuth := $au.kubernetesAuth | default dict -}}
{{- $mount := $kAuth.mountPath | default "kubernetes" -}}
{{- $kv := $kAuth.kvMountPath | default "secret" -}}
{{- $ttl := $kAuth.tokenTTL | default "1h" -}}
{{- $maxTTL := $kAuth.tokenMaxTTL | default "24h" -}}
{{- $esoRole := $kAuth.role | default "external-secrets" -}}
{{- $esoSA := $kAuth.serviceAccountName | default "external-secrets" -}}
{{- $esoNs := $kAuth.serviceAccountNamespace | default "external-secrets-system" -}}
{{- $xcr := (($kAuth.crossClusterTokenReview | default dict).enabled | default false) -}}
{{- $capi := $kAuth.catalystApi | default dict -}}
{{- $sr := .Values.snapshotReplication | default dict -}}
{{- $srRole := (($sr.kubernetesAuth | default dict).role) | default "openbao-snapshot" -}}
{{- $cfg := .Values.peerTrust | default dict -}}
{{- $aud := $cfg.audience | default "openbao-peer-push" -}}
{{- $apps := $cfg.kvPathPrefix | default "openova/apps" | trimSuffix "/" -}}
{{- $pt := include "openbao.peerTrust" . | fromJson -}}
{{- $ctx := dict "kv" $kv "apps" $apps "aud" $aud -}}
{{- $ns := .Release.Namespace -}}

{{- $policies := list "external-secrets-read" "external-secrets-push" "sso-bridge-write" "sso-configure-admin" -}}
{{- if $capi.enabled }}{{ $policies = append $policies "catalyst-api-write" }}{{ end -}}
{{- if $sr.enabled }}{{ $policies = append $policies "openbao-snapshot" }}{{ end -}}
{{- if $pt.peers }}{{ $policies = concat $policies (list "openbao-peer-token" $aud) }}{{ end -}}

{{- /* role: name, sa, saNs, policies, ttl, maxTTL, alsoVcluster */ -}}
{{- $roles := list
  (dict "name" $esoRole "sa" $esoSA "ns" $esoNs "policies" (list "external-secrets-read" "external-secrets-push") "ttl" $ttl "max" $maxTTL "vc" false)
  (dict "name" "sso-bridge" "sa" "sso-bridge-reconciler" "ns" "sso-bridge" "policies" (list "sso-bridge-write" "external-secrets-read") "ttl" $ttl "max" $maxTTL "vc" true)
  (dict "name" "sso-configure" "sa" "openbao-sso-configure" "ns" $ns "policies" (list "sso-configure-admin") "ttl" $ttl "max" $maxTTL "vc" true) -}}
{{- if $capi.enabled -}}
{{- $roles = append $roles (dict "name" ($capi.role | default "catalyst-api") "sa" ($capi.serviceAccountName | default "catalyst-api-cutover-driver") "ns" ($capi.serviceAccountNamespace | default "catalyst-system") "policies" (list "catalyst-api-write") "ttl" $ttl "max" $maxTTL "vc" false) -}}
{{- end -}}
{{- if $sr.enabled -}}
{{- $roles = append $roles (dict "name" $srRole "sa" "openbao-snapshot" "ns" $ns "policies" (list "openbao-snapshot") "ttl" $ttl "max" $maxTTL "vc" true) -}}
{{- end -}}
{{- if $pt.peers -}}
{{- $roles = append $roles (dict "name" "openbao-peer-token" "sa" $esoSA "ns" $esoNs "policies" (list "openbao-peer-token") "ttl" "15m" "max" "1h" "vc" false) -}}
{{- end -}}

{{- $ops := list -}}
{{- range $policies -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "sys/policies/acl/%s" .) "payload" (dict "policy" (include (printf "openbao.policy.%s" (ternary "openbao-peer-push" . (eq . $aud))) $ctx))) -}}
{{- end -}}
{{- range $roles -}}
{{- $body := dict "bound_service_account_names" (list .sa) "bound_service_account_namespaces" (list .ns) "policies" .policies "ttl" .ttl "max_ttl" .max -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/role/%s" $mount .name) "payload" $body) -}}
{{- if and $xcr .vc -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/kubernetes-vcluster/role/%s" .name) "payload" $body) -}}
{{- end -}}
{{- end -}}
{{- if $pt.peers -}}
{{- $self := $pt.self -}}
{{- $ops = append $ops (dict "kind" "write" "path" "identity/oidc/config" "payload" (dict "issuer" (printf "http://%s:8200" (index $pt.hosts $self)))) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "identity/oidc/key/%s" $aud) "payload" (dict "algorithm" "RS256" "allowed_client_ids" (list $aud) "rotation_period" "24h" "verification_ttl" "24h")) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "identity/oidc/role/%s" $aud) "payload" (dict "key" $aud "client_id" $aud "ttl" ($cfg.tokenTTL | default "1h") "template" (dict "openova_region" $self | toJson))) -}}
{{- range $pt.peers -}}
{{- $issuer := printf "http://%s:8200/v1/identity/oidc" (index $pt.hosts .) -}}
{{- $probe := printf "%s/.well-known/openid-configuration" $issuer -}}
{{- $m := printf "jwt-peer-%s" . -}}
{{- $ops = append $ops (dict "kind" "mount" "path" (printf "sys/auth/%s" $m) "probe" $probe "payload" (dict "type" "jwt" "description" "ADR-0015 peer-region ESO push (bp-openbao)")) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/config" $m) "probe" $probe "payload" (dict "oidc_discovery_url" $issuer)) -}}
{{- $ops = append $ops (dict "kind" "write" "path" (printf "auth/%s/role/%s" $m $aud) "probe" $probe "payload" (dict "role_type" "jwt" "bound_audiences" (list $aud) "user_claim" "sub" "bound_claims" (dict "openova_region" .) "token_policies" (list $aud) "token_ttl" "15m" "token_max_ttl" "1h")) -}}
{{- end -}}
{{- end -}}
{{- $ops | toJson -}}
{{- end -}}
