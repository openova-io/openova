{{/*
Expand the name of the chart.
*/}}
{{- define "bp-newapi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-newapi.fullname" -}}
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
{{- define "bp-newapi.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-newapi
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-newapi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-newapi.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-newapi.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (channel + policy config).
*/}}
{{- define "bp-newapi.configMapName" -}}
{{- printf "%s-config" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Effective channel list — composed default channels plus
`.Values.channels`. Composition order matters because NewAPI's
channel-router resolves `model` lookups in row-insertion order:
  1. defaultChannels.qwenBankDhofar     (issue #915 — channel #1)
  2. .Values.channels                    (operator-supplied)
  3. defaultChannels.vllm                (in-cluster vLLM fallback)
A fresh customer landing on a fresh Sovereign with no
`.Values.channels` set hits qwenBankDhofar first; this is the
documented "channel #1 = Qwen3.6@BankDhofar" contract from epic #915
(per-tenant SME alice → NewAPI → Qwen3.6@BankDhofar end-to-end DoD).
Centralised so configmap.yaml + assertChannelAttestation +
channel-seed-job.yaml operate on the same materialised list.
*/}}
{{- define "bp-newapi.effectiveChannels" -}}
{{- $channels := list -}}
{{- $dc := .Values.defaultChannels | default dict -}}
{{/* ── Channel #1: Qwen3.6 @ BankDhofar (#915) ─────────────────
     Attestation gate (PR #1631 follow-up, 2026-05-18): franchised
     Sovereigns set `MARKETPLACE_ENABLED=true` to flip qbd.enabled true,
     but cloud-init may not yet have `LLM_BANK_DHOFAR_ACCOUNT_ID` /
     `LLM_BANK_DHOFAR_CONTRACT_REF` set (no commercial contract signed
     yet for that Sovereign). The envsubst defaults leave accountId /
     contractRef as empty strings, which makes `assertChannelAttestation`
     fail the install with `commercial-contract attestation requires
     accountId`.

     Fix: SKIP composing the qwenBankDhofar channel when
     attestation.kind=commercial-contract AND accountId/contractRef are
     empty. The Sovereign installs newapi with zero default channels
     (operator-supplied channels still compose). Once the commercial
     contract lands, the operator overlays the attestation values and
     the channel composes on the next reconcile. */}}
{{- $qbd := $dc.qwenBankDhofar | default dict -}}
{{- $qbdAtt := $qbd.attestation | default (dict "kind" "commercial-contract") -}}
{{- $qbdAttReady := true -}}
{{- if and $qbd.enabled (eq (default "" $qbdAtt.kind) "commercial-contract") -}}
  {{- if or (not $qbdAtt.accountId) (not $qbdAtt.contractRef) -}}
    {{- $qbdAttReady = false -}}
  {{- end -}}
{{- end -}}
{{- if and $qbd.enabled $qbdAttReady -}}
  {{- if not $qbd.endpoint -}}
    {{- fail "defaultChannels.qwenBankDhofar.enabled=true but defaultChannels.qwenBankDhofar.endpoint is empty — supply the upstream relay URL in the per-Sovereign bootstrap-kit overlay (canonical: https://llm-api.omtd.bankdhofar.com)" -}}
  {{- end -}}
  {{- $composed := dict
        "name"      (default "qwen" $qbd.name)
        "type"      "openai-compatible"
        "endpoint"  $qbd.endpoint
        "models"    (default (list "qwen3.6" "qwen3-coder") $qbd.models)
        "attestation" $qbdAtt -}}
  {{- if $qbd.existingSecret -}}
    {{- $_ := set $composed "existingSecret" $qbd.existingSecret -}}
  {{- end -}}
  {{- $channels = append $channels $composed -}}
{{- end -}}
{{/* ── Operator-supplied channels (in-order) ──────────────────── */}}
{{- range (default (list) .Values.channels) -}}
  {{- $channels = append $channels . -}}
{{- end -}}
{{/* ── Default vLLM (in-cluster fallback) ─────────────────────── */}}
{{- $vllm := $dc.vllm | default dict -}}
{{- if $vllm.enabled -}}
  {{- if not $vllm.endpoint -}}
    {{- fail "defaultChannels.vllm.enabled=true but defaultChannels.vllm.endpoint is empty — supply the upstream vLLM relay URL in the per-Sovereign bootstrap-kit overlay (e.g. https://llm-api.omtd.bankdhofar.com)" -}}
  {{- end -}}
  {{- $composed := dict
        "name"      (default "qwen" $vllm.name)
        "type"      "vllm"
        "endpoint"  $vllm.endpoint
        "models"    (default (list "qwen3-coder") $vllm.models)
        "attestation" (default (dict "kind" "in-cluster") $vllm.attestation) -}}
  {{- if $vllm.existingSecret -}}
    {{- $_ := set $composed "existingSecret" $vllm.existingSecret -}}
  {{- end -}}
  {{- $channels = append $channels $composed -}}
{{- end -}}
{{- toYaml $channels -}}
{{- end -}}

{{/*
Channel-seed Job name. Reused by the Job + RBAC + ConfigMap manifests
emitted by templates/channel-seed-job.yaml.
*/}}
{{- define "bp-newapi.channelSeedJobName" -}}
{{- printf "%s-channel-seed" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Channel attestation gate — refuses to render if any enabled channel
lacks attestation. Compliance posture defined in
platform/newapi/README.md and blueprint.yaml configSchema. Operates on
the EFFECTIVE channel list (`.Values.channels` + composed defaults).
*/}}
{{- define "bp-newapi.assertChannelAttestation" -}}
{{- $effective := include "bp-newapi.effectiveChannels" . | fromYamlArray -}}
{{- range $idx, $ch := $effective }}
{{- if not $ch.attestation }}
{{- fail (printf "channel[%d] (%s): missing required attestation block — see platform/newapi/README.md compliance posture" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.kind }}
{{- fail (printf "channel[%d] (%s): attestation.kind is required (one of: in-cluster, commercial-contract, byok)" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if eq $ch.attestation.kind "commercial-contract" }}
{{- if not $ch.attestation.accountId }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires accountId" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.contractRef }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires contractRef" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
