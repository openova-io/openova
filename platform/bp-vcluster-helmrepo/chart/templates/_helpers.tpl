{{/*
Common labels for every resource the chart renders. Per
docs/EPICS-1-6.md §1.1 every Catalyst-managed resource carries the
canonical label set.
*/}}
{{- define "bp-vcluster-helmrepo.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
catalyst.openova.io/blueprint: {{ .Chart.Name }}
{{- end }}
