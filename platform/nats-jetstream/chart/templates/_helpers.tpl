{{/*
Common labels for every Catalyst-overlay resource in this chart.
The upstream nats subchart owns its own labels; we only stamp these
on Catalyst-authored resources (Streams, KVs, NetworkPolicies, etc.).
*/}}
{{- define "catalyst-nats.labels" -}}
app.kubernetes.io/name: nats-jetstream
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: messaging
app.kubernetes.io/part-of: catalyst
catalyst.openova.io/blueprint: bp-nats-jetstream
catalyst.openova.io/blueprint-version: {{ .Chart.Version | quote }}
{{- end -}}

{{/*
The K8s Service that fronts the upstream nats subchart's StatefulSet.
Default `<release>-nats` matches the upstream chart's templating; the
operator can override per-Sovereign via .Values.catalystStreams.servers.
*/}}
{{- define "catalyst-nats.servers" -}}
{{- $servers := .Values.catalystStreams.servers -}}
{{- if $servers -}}
{{- $servers -}}
{{- else -}}
nats://{{ .Release.Name }}-nats.{{ .Release.Namespace }}.svc.cluster.local:4222
{{- end -}}
{{- end -}}
