{{/* chart name */}}
{{- define "agenity.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* fully-qualified name */}}
{{- define "agenity.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "agenity.labels" -}}
app.kubernetes.io/name: {{ include "agenity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-agenity
{{- end -}}

{{/* selector labels */}}
{{- define "agenity.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agenity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* image ref */}}
{{- define "agenity.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- /*
agenity.gateHostname — the public hostname the oidc-gate companion (and the
chart's own HTTPRoute) owns. The org-gitops emitter passes the per-Org host as
httpRoute.hostnames[0] (agenity.<slug>.<pool>); else derive agenity.<fqdn> from
sovereignFqdn; else "" (fail-closed — Inviolable Principle #4, the gate then
renders nothing). Single source of truth shared by the gate + the suppression
guard so both agree on the host.
*/ -}}
{{- define "agenity.gateHostname" -}}
{{- if .Values.httpRoute.hostnames -}}
{{- index .Values.httpRoute.hostnames 0 -}}
{{- else if .Values.sovereignFqdn -}}
{{- printf "agenity.%s" .Values.sovereignFqdn -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.mcpTenantHost — the Organization console host (console.<slug>.<pool>)
the openova MCP forwards as X-Tenant-Host on the org-scoped install path
(#4116). The catalyst-api base URL is the SOVEREIGN host (console.<sov-fqdn>)
which is NOT a registered tenant, so X-Tenant-Host MUST be the Org host or the
install 404s `tenant-not-registered` (#4610). Resolution:
  1. explicit openovaMCP.tenantHost wins;
  2. else derive from the agenity gate host (agenity.<slug>.<pool>): swap the
     leading `agenity` label for `console` → console.<slug>.<pool>;
  3. else empty (the MCP falls back to deriving from the api-URL host).
*/ -}}
{{- define "agenity.mcpTenantHost" -}}
{{- if .Values.openovaMCP.tenantHost -}}
{{- .Values.openovaMCP.tenantHost -}}
{{- else -}}
{{- $host := include "agenity.gateHostname" . -}}
{{- if $host -}}
{{- $rest := "" -}}
{{- if contains "." $host -}}
{{- $rest = (regexReplaceAll "^[^.]+\\." $host "") -}}
{{- printf "console.%s" $rest -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.gateEnabled — "true" only when the SSO gate is requested AND a hostname
resolves (no host → fail-closed, the gate would render nothing useful). Drives
BOTH templates/oidc-gate.yaml (render the gate) AND templates/httproute.yaml
(suppress the chart's own route so the gate owns the host).
*/ -}}
{{- define "agenity.gateEnabled" -}}
{{- if and .Values.oidcGate.enabled (include "agenity.gateHostname" .) -}}
true
{{- end -}}
{{- end -}}

{{- /*
agenity.gateClientId — the KC client id the gate registers. Explicit
oidcGate.clientId wins (the org-gitops emitter pins agenity-<slug>); else
derive agenity-<firstLabel> from the gate hostname (agenity.<slug>.<pool> →
agenity-<slug>); else "agenity-gate".
*/ -}}
{{- define "agenity.gateClientId" -}}
{{- if .Values.oidcGate.clientId -}}
{{- .Values.oidcGate.clientId -}}
{{- else -}}
{{- $host := include "agenity.gateHostname" . -}}
{{- $labels := splitList "." $host -}}
{{- if gt (len $labels) 1 -}}
{{- printf "agenity-%s" (index $labels 1) -}}
{{- else -}}
agenity-gate
{{- end -}}
{{- end -}}
{{- end -}}
