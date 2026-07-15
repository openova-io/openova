{{/* chart name */}}
{{- define "openova-mcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* fully-qualified name */}}
{{- define "openova-mcp.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "openova-mcp.labels" -}}
app.kubernetes.io/name: {{ include "openova-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openova-mcp
{{- end -}}

{{/* selector labels */}}
{{- define "openova-mcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "openova-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /*
openova-mcp.image — repository:tag honoring the cutover step-07
global.imageRegistry pivot seam (#4885/#4892 — the bp-openova-flow-server
0.2.5 pattern): when the post-handover cutover sets global.imageRegistry,
strip the leading registry host (first path segment) off the repository and
re-prefix with the pivot registry so the image pulls from the local Harbor
under the deny-egress hold.
*/ -}}
{{- define "openova-mcp.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- $repo := .Values.image.repository -}}
{{- $registry := "" -}}
{{- if .Values.global -}}
{{- $registry = .Values.global.imageRegistry | default "" -}}
{{- end -}}
{{- if $registry -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := $repo -}}
{{- if gt (len $parts) 1 -}}
{{- $rest = rest $parts | join "/" -}}
{{- end -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $registry) $rest $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{/* ServiceAccount name */}}
{{- define "openova-mcp.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "openova-mcp.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- /*
openova-mcp.catalystApiUrl — the facade's upstream. Explicit value wins;
else derived per mode: sovereign → the real in-cluster catalyst-api Service
(products/catalyst/chart/templates/api-service.yaml: `catalyst-api`,
catalyst-system, port 8080 — wired off the rendered Service per RUNBOOKS
§4.4, not an assumed shape); organization → the public Sovereign console
URL (an Org namespace cannot resolve the host Service name, #4111).
Fail-closed: organization mode with no sovereignFqdn renders "" and the
binary's data tools refuse (whoami still works), rather than inventing a
wrong default (Inviolable #4).
*/ -}}
{{- define "openova-mcp.catalystApiUrl" -}}
{{- if .Values.catalystApi.url -}}
{{- .Values.catalystApi.url -}}
{{- else if eq .Values.mode "sovereign" -}}
{{- "http://catalyst-api.catalyst-system.svc.cluster.local:8080" -}}
{{- else if .Values.sovereignFqdn -}}
{{- printf "https://console.%s" .Values.sovereignFqdn -}}
{{- end -}}
{{- end -}}

{{- /*
openova-mcp.hostname — the public HTTPRoute host. Explicit hostnames[0]
wins; else mcp.<sovereignFqdn>; else "" (fail-closed — the route renders
nothing, Inviolable #4).
*/ -}}
{{- define "openova-mcp.hostname" -}}
{{- if .Values.httpRoute.hostnames -}}
{{- index .Values.httpRoute.hostnames 0 -}}
{{- else if .Values.sovereignFqdn -}}
{{- printf "mcp.%s" .Values.sovereignFqdn -}}
{{- end -}}
{{- end -}}

{{- /*
openova-mcp.mcpUrl — the in-cluster MCP endpoint advertised to chepherd
(the optional attach ConfigMap). Explicit chepherd.attach.url wins.
*/ -}}
{{- define "openova-mcp.mcpUrl" -}}
{{- if .Values.chepherd.attach.url -}}
{{- .Values.chepherd.attach.url -}}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%v/mcp" (include "openova-mcp.fullname" .) .Release.Namespace .Values.service.port -}}
{{- end -}}
{{- end -}}
