{{/*
Expand the name of the chart.
*/}}
{{- define "bp-openclaw.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-openclaw.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels — required by docs/BLUEPRINT-AUTHORING.md §14 and by the
Catalyst projector to track resources back to the Blueprint.
*/}}
{{- define "bp-openclaw.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-openclaw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openclaw
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-openclaw.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-openclaw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-openclaw.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-openclaw.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (per-user pod template).
*/}}
{{- define "bp-openclaw.podTemplateConfigMapName" -}}
{{- printf "%s-pod-template" (include "bp-openclaw.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Tenant namespace — falls back to .Release.Namespace when tenant.namespace
is empty (the #4952 default), but an overlay may set it explicitly so the
controller's RBAC scope matches the operator's intent. NEVER default this
to a non-empty placeholder: `default` returns the SECOND arg when it is
truthy, so a placeholder would win over .Release.Namespace and pin the
Role/RoleBinding at a namespace that does not exist on a real per-Org
install (the #4952 `namespaces "org-example" not found` failure).
*/}}
{{- define "bp-openclaw.tenantNamespace" -}}
{{- default .Release.Namespace .Values.tenant.namespace }}
{{- end }}

{{/*
─── OIDC + LLM resolution helpers (umbrella #915) ─────────────────────

The chart's canonical config blocks are `oidc.*` and `llm.*`; legacy
overlays may still set `keycloak.*` / `newapi.*`. Helpers prefer the
canonical value and fall back to the legacy alias when unset.
*/}}
{{/*
Resolution rule: when an overlay sets a legacy key (`keycloak.*` /
`newapi.*`) AND leaves the canonical block at its placeholder default,
the legacy key wins (back-compat). When the canonical block is
explicitly set to a non-placeholder value, it always wins.
*/}}
{{- define "bp-openclaw.oidc.issuerURL" -}}
{{- $oidc := .Values.oidc.issuerURL | default "" -}}
{{- $legacy := .Values.keycloak.realmURL | default "" -}}
{{- if and $legacy (or (eq $oidc "") (eq $oidc "https://keycloak.example.local/realms/example")) -}}
{{- $legacy -}}
{{- else if $oidc -}}
{{- $oidc -}}
{{- else -}}
{{- "https://keycloak.example.local/realms/example" -}}
{{- end -}}
{{- end }}

{{- define "bp-openclaw.oidc.clientId" -}}
{{- $oidc := .Values.oidc.clientId | default "" -}}
{{- $legacy := .Values.keycloak.clientID | default "" -}}
{{- if and $legacy (or (eq $oidc "") (eq $oidc "openclaw")) -}}
{{- $legacy -}}
{{- else if $oidc -}}
{{- $oidc -}}
{{- else -}}
{{- "openclaw" -}}
{{- end -}}
{{- end }}

{{- define "bp-openclaw.oidc.clientSecretName" -}}
{{- $oidc := "" -}}
{{- if .Values.oidc.clientSecret -}}
{{- $oidc = .Values.oidc.clientSecret.name | default "" -}}
{{- end -}}
{{- $legacy := .Values.keycloak.clientSecretName | default "" -}}
{{- if and $legacy (or (eq $oidc "") (eq $oidc "openclaw-oidc")) -}}
{{- $legacy -}}
{{- else if $oidc -}}
{{- $oidc -}}
{{- else -}}
{{- "openclaw-oidc" -}}
{{- end -}}
{{- end }}

{{- define "bp-openclaw.oidc.clientSecretKey" -}}
{{- if and .Values.oidc.clientSecret .Values.oidc.clientSecret.key -}}
{{- .Values.oidc.clientSecret.key -}}
{{- else -}}
{{- "OIDC_CLIENT_SECRET" -}}
{{- end -}}
{{- end }}

{{- define "bp-openclaw.llm.baseURL" -}}
{{- $llm := .Values.llm.baseURL | default "" -}}
{{- $legacy := .Values.newapi.baseURL | default "" -}}
{{- if and $legacy (or (eq $llm "") (eq $llm "https://newapi.example.local/v1")) -}}
{{- $legacy -}}
{{- else if $llm -}}
{{- $llm -}}
{{- else -}}
{{- "https://newapi.example.local/v1" -}}
{{- end -}}
{{- end }}

{{- /*
#6114 — `bp-openclaw.llm.apiKeySecretName` / `.apiKeySecretKey` are GONE.
They resolved the name of a controller-side NewAPI service-token Secret that
no template in this chart (or anywhere in the repo) has ever created, so the
`secretKeyRef` they fed could only ever resolve to nothing. The chart default
was `openclaw-llm-apikey` and both HelmRelease generators overrode it to
`openclaw-newapi-controller-token`; neither name had a producer. The controller
binary reads no api-key env, so nothing regressed by removing them. Do not
reintroduce a secret-name helper without adding the Secret template alongside
it — `TestOpenClawChartSecretRefsHaveProducers` fails the build if you do.
*/ -}}
{{- define "bp-openclaw.llm.defaultModel" -}}
{{- default "qwen3.6" .Values.llm.defaultModel -}}
{{- end }}

{{/*
Placeholder-rejection assertions. The chart ships placeholder defaults
(see values.yaml) so `helm template` smoke-renders cleanly in CI
(mirroring bp-self-sovereign-cutover's pattern); these assertions fail
loudly only when the placeholder is left in *and* the chart is being
installed for real (detected by the `bp-openclaw.assertNoPlaceholders`
flag in values, which Flux overlays set to `true` after they've
provided real values). The smoke-render path leaves the flag at its
default `false` so CI passes.

Operators MUST set every value in the "Required at install time" block
of values.yaml; the deploy-time validation hook in
catalyst-platform's reconciler also checks these against the rendered
HelmRelease.
*/}}
{{- define "bp-openclaw.assertNoPlaceholders" -}}
{{- if .Values.assertNoPlaceholders }}
{{- if eq .Values.controller.image.tag "0.1.0-placeholder" }}
{{- fail "controller.image.tag is still the placeholder — overlay must supply a SHA-pinned tag (Inviolable Principle 4)" }}
{{- end }}
{{- if eq .Values.perUserPod.image.tag "0.1.0-placeholder" }}
{{- fail "perUserPod.image.tag is still the placeholder — overlay must supply a SHA-pinned tag (Inviolable Principle 4)" }}
{{- end }}
{{- if eq (include "bp-openclaw.oidc.issuerURL" .) "https://keycloak.example.local/realms/example" }}
{{- fail "oidc.issuerURL is still the placeholder — overlay must supply the per-tenant Keycloak realm URL" }}
{{- end }}
{{- if eq (include "bp-openclaw.llm.baseURL" .) "https://newapi.example.local/v1" }}
{{- fail "llm.baseURL is still the placeholder — overlay must supply the per-tenant NewAPI OpenAI-compatible endpoint" }}
{{- end }}
{{- /* #4952: tenant.namespace has NO placeholder guard. It defaults to ""
       and `bp-openclaw.tenantNamespace` falls back to .Release.Namespace — the
       real per-Org namespace of a per-Org install — so an unset value is a
       CORRECT install, not a placeholder. (The old guard checked a stale
       "sme-example" literal that never matched the real "org-example" default,
       which is exactly how the org-example namespace leaked into the RBAC.) */}}
{{- /* #4272: ingress.host only matters when the traefik Ingress is rendered.
       On a Sovereign the public exposure is httpRoute.hostnames (Cilium Gateway)
       and ingress.enabled is false, so the placeholder host is harmless. */}}
{{- if and .Values.ingress.enabled (eq .Values.ingress.host "openclaw.example.local") }}
{{- fail "ingress.host is still the placeholder — overlay must supply the controller public hostname (or use httpRoute.hostnames on a Cilium-Gateway Sovereign)" }}
{{- end }}
{{- end }}
{{- end }}
