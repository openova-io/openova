{{/*
Expand the name of the chart.
*/}}
{{- define "bp-stalwart-sovereign.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-stalwart-sovereign.fullname" -}}
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
{{- define "bp-stalwart-sovereign.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-stalwart-sovereign.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-stalwart-sovereign
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-stalwart-sovereign.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-stalwart-sovereign.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-stalwart-sovereign.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-stalwart-sovereign.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (Stalwart bootstrap config.toml).
*/}}
{{- define "bp-stalwart-sovereign.configMapName" -}}
{{- printf "%s-config" (include "bp-stalwart-sovereign.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
DNS-records ConfigMap name (MX/A/SPF/DKIM/DMARC required at the
Sovereign's parent zone). Surfaced by the Sovereign Console UI until
the orchestrator-side auto-registration sub-PR lands.
*/}}
{{- define "bp-stalwart-sovereign.dnsRecordsConfigMapName" -}}
{{- printf "%s-dns-records-required" (include "bp-stalwart-sovereign.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Sovereign FQDN — resolves the Sovereign's primary domain regardless of
which value seam the bootstrap-kit overlay populates. Three accepted
shapes (in priority order):

  1. `sovereignFQDN: <string>`         — chart-block override (highest
                                         priority). Operator sets this
                                         in a per-cluster overlay when
                                         the per-Sovereign FQDN is not
                                         the canonical one in
                                         `global.sovereignFQDN` (rare).
  2. `global.sovereignFQDN: <string>`  — canonical bootstrap-kit
                                         envsubst pattern (matches
                                         clusters/_template/bootstrap-
                                         kit/01-cilium.yaml etc.).
  3. ""                                — empty fallback. Smoke-render-
                                         safe (CI's blueprint-release
                                         smoke gate renders with empty
                                         values; per-template gates
                                         downstream skip-on-empty).

Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) — no fallback to
a literal domain anywhere in this file.
*/}}
{{- define "bp-stalwart-sovereign.sovereignFQDN" -}}
{{- $g := .Values.global | default dict -}}
{{- if .Values.sovereignFQDN -}}
{{- .Values.sovereignFQDN -}}
{{- else if $g.sovereignFQDN -}}
{{- $g.sovereignFQDN -}}
{{- end -}}
{{- end -}}

{{/*
Mail FQDN — composes `mail.<sovereignFQDN>`. Returns empty when the
sovereignFQDN is unset (smoke-render-safe). Per-template render gates
keep Service / Certificate / ConfigMap manifests from emitting unusable
references when the host is empty.
*/}}
{{- define "bp-stalwart-sovereign.mailFQDN" -}}
{{- $fqdn := include "bp-stalwart-sovereign.sovereignFQDN" . -}}
{{- if $fqdn -}}
{{- printf "mail.%s" $fqdn -}}
{{- end -}}
{{- end -}}

{{/*
Sender address — composes `noreply@<sovereignFQDN>` (literal `noreply`
local-part is the chart's `smtp.submissionUser` default; operator may
override via per-cluster overlay).
*/}}
{{- define "bp-stalwart-sovereign.senderAddress" -}}
{{- $fqdn := include "bp-stalwart-sovereign.sovereignFQDN" . -}}
{{- if $fqdn -}}
{{- printf "%s@%s" .Values.smtp.submissionUser $fqdn -}}
{{- end -}}
{{- end -}}

{{/*
DMARC rua address — composed as `postmaster@<sovereignFQDN>` when the
operator's `dns.dmarc.rua` is empty. Per docs/INVIOLABLE-PRINCIPLES.md
#4 the operator may override via per-cluster overlay.
*/}}
{{- define "bp-stalwart-sovereign.dmarcRua" -}}
{{- if .Values.dns.dmarc.rua -}}
{{- .Values.dns.dmarc.rua -}}
{{- else -}}
{{- $fqdn := include "bp-stalwart-sovereign.sovereignFQDN" . -}}
{{- if $fqdn -}}
{{- printf "postmaster@%s" $fqdn -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Effective image reference. Honours `global.imageRegistry` rewrite for
post-handover Sovereign Harbor proxy-cache (ADR-0001 §11.5). Always
SHA-pinned via digest when present (Inviolable Principle #4 / #4a).
*/}}
{{- define "bp-stalwart-sovereign.image" -}}
{{- $g := .Values.global | default dict -}}
{{- $img := .Values.stalwart.image -}}
{{- $reg := default $img.registry $g.imageRegistry -}}
{{- if $img.digest -}}
{{- printf "%s/%s@%s" $reg $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $reg $img.repository $img.tag -}}
{{- end -}}
{{- end -}}

{{/*
Effective setup-Job image reference. Same registry-override semantics
as the main image.
*/}}
{{- define "bp-stalwart-sovereign.setupJobImage" -}}
{{- $g := .Values.global | default dict -}}
{{- $img := .Values.setupJob.image -}}
{{- $reg := default .Values.stalwart.image.registry $g.imageRegistry -}}
{{- if $img.digest -}}
{{- printf "%s/%s@%s" $reg $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $reg $img.repository $img.tag -}}
{{- end -}}
{{- end -}}

{{/*
Render the Stalwart admin password env var reference. Always pulled
from the Secret named `.Values.admin.secretName`, key ADMIN_PASSWORD.
Centralised so deployment.yaml and the setup Job pin to the same key.
*/}}
{{- define "bp-stalwart-sovereign.adminPasswordEnv" -}}
- name: ADMIN_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.admin.secretName | quote }}
      key: ADMIN_PASSWORD
{{- end -}}

{{/*
Render the Stalwart submission password env var reference. Used by the
setup Job to register the submission principal via /api/principal.
*/}}
{{- define "bp-stalwart-sovereign.submissionPasswordEnv" -}}
- name: SUBMISSION_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.smtp.submissionSecretName | quote }}
      key: smtp-pass
{{- end -}}

{{/*
Resolve the submission password — single source of truth.

Source-of-truth precedence (the chart's submission-secret.yaml is
canonical; the mirror in catalyst-system is hydrated by the post-
install setup Job from the source):

  1. Existing in-namespace submission Secret (steady state — the only
     Secret the chart's `lookup` reads from).
  2. Existing catalyst-system mirror Secret (Phase-1 cutover — when
     catalyst-api's sovereign_smtp_seed.go pre-seeded mothership
     creds; the chart picks up those bytes so a session does not
     break across the cutover).
  3. Fresh randAlphaNum 32 (first install, neither Secret exists).

The mirror Secret in catalyst-system is materialised by the post-
install setup Job (templates/setup-job.yaml) which reads the source
Secret via the K8s API and writes the mirror with identical bytes.
This avoids the helm-render-pass race that would otherwise produce
mismatched bytes if BOTH Secrets were rendered as templates calling
this helper (each `randAlphaNum` invocation returns fresh bytes).

Per docs/INVIOLABLE-PRINCIPLES.md #10 this helper is the ONLY call
site that touches the plaintext value; the consumer template re-
encodes immediately into b64.
*/}}
{{- define "bp-stalwart-sovereign.submissionPassword" -}}
{{- $srcName := .Values.smtp.submissionSecretName -}}
{{- $srcNs := .Release.Namespace -}}
{{- $src := lookup "v1" "Secret" $srcNs $srcName -}}
{{- $pwd := "" -}}
{{- if and $src $src.data (index $src.data "smtp-pass") -}}
  {{- $pwd = index $src.data "smtp-pass" | b64dec -}}
{{- else -}}
  {{- $cfg := .Values.sovereignSMTPCredentialsMirror | default dict -}}
  {{- $mirrorNs := default "catalyst-system" $cfg.namespace -}}
  {{- $mirrorName := default "sovereign-smtp-credentials" $cfg.secretName -}}
  {{- $mirror := lookup "v1" "Secret" $mirrorNs $mirrorName -}}
  {{- if and $mirror $mirror.data (index $mirror.data "smtp-pass") -}}
    {{- $pwd = index $mirror.data "smtp-pass" | b64dec -}}
  {{- else -}}
    {{- $pwd = randAlphaNum 32 -}}
  {{- end -}}
{{- end -}}
{{- $pwd -}}
{{- end -}}
