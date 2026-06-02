{{- /*
bp-cilium-policies — shared template helpers.

G117.1-followup (Refs #2749, 2026-06-02): initial scaffold.
*/ -}}

{{- /*
Standard Catalyst label set stamped on every CiliumNetworkPolicy /
CiliumClusterwideNetworkPolicy CR rendered by this chart. Mirrors the
label-emission helper from bp-kyverno-policies + bp-network-policies.
*/ -}}
{{- define "bp-cilium-policies.labels" -}}
app.kubernetes.io/name: bp-cilium-policies
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
catalyst.openova.io/blueprint: bp-cilium-policies
{{- end -}}
