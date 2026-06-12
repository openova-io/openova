{{- /*
bp-oidc-gate helpers (#3374).
*/ -}}

{{- define "bp-oidc-gate.labels" -}}
app.kubernetes.io/name: bp-oidc-gate
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
catalyst.openova.io/blueprint: bp-oidc-gate
{{- end -}}

{{- /*
bp-oidc-gate.instanceHostname — dict {root, inst} -> public hostname.
Explicit inst.hostname wins; else <name>.<sovereignFQDN>; else "" (the
instance renders nothing — fail-closed per Inviolable Principle #4).
*/ -}}
{{- define "bp-oidc-gate.instanceHostname" -}}
{{- $root := .root -}}
{{- $inst := .inst -}}
{{- if $inst.hostname -}}
{{- $inst.hostname -}}
{{- else if $root.Values.sovereignFQDN -}}
{{- printf "%s.%s" $inst.name $root.Values.sovereignFQDN -}}
{{- end -}}
{{- end -}}
