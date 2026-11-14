{{/*
Labels of every object this chart renders.
*/}}
{{- define "bp-machine-operations.labels" -}}
app.kubernetes.io/name: bp-machine-operations
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
catalyst.openova.io/blueprint: bp-machine-operations
{{- end }}

{{/*
The image of the machine controller. The render fails when it is empty: a
Deployment without an image is never rendered.
*/}}
{{- define "bp-machine-operations.image" -}}
{{- if not .Values.image -}}
{{- fail "bp-machine-operations: .Values.image is empty. Set it to the catalyst-api image that holds /usr/local/bin/machine-controller." -}}
{{- end -}}
{{- .Values.image -}}
{{- end }}
