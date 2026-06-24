{{- /*
1.0.11 (#4220, 2026-06-24): single source of truth for "should the webhook
readiness-gate render at all?".

The G53 (#2604) webhook-gate Job + its RBAC, and the G21 (#2570) webhook-cert
bootstrap, ALL exist solely to cover the cluster-singleton webhook
(cnpg-mutating-webhook-configuration / cnpg-validating-webhook-configuration +
Service cnpg-webhook-service) coming up cleanly. A per-Org bp-cnpg install runs
WEBHOOK-LESS (#4143 / #4201: organization_gitops.go sets
cloudnative-pg.webhook.{mutating,validating}.create=false so the per-Org
operator never fights the platform cnpg-system operator for the cluster
singletons). In that mode:

  - There is NO webhook config for the gate to poll → the Job would spin its
    full 300s budget and FAIL the post-install hook every time.
  - WORSE: the gate Job is created directly by Helm (no Flux ownerReference /
    no `app.kubernetes.io/managed-by: flux` label), so the `flux-managed`
    Kyverno Enforce policy (`workload-must-be-flux-managed`) DENIES it at
    admission → the bp-cnpg HelmRelease InstallFailed → the demo Org's
    WordPress (+ newapi/openclaw/stalwart) wedged Ready=False on
    `dependency bp-cnpg is not ready` (live on omantel.biz dep
    4635277cae4ffed9, #4220).

So the gate (and the cert-bootstrap) MUST NOT render when the webhook configs
aren't being created. This helper centralises that decision:

  webhookGateEnabled = (webhookGate.enabled, default true)
                       AND (the subchart is actually creating the webhook
                            configs — i.e. NOT webhook-less)

Webhook-less is detected when BOTH cloudnative-pg.webhook.mutating.create AND
cloudnative-pg.webhook.validating.create are explicitly false. If an operator
genuinely wants the gate in some bespoke topology they can still force it on
via webhookGate.enabled: true paired with the webhook configs enabled; setting
webhookGate.enabled: false always skips. This is a render-skip, not a policy
exemption — the flux-managed guardrail stays fully in force.
*/}}
{{- define "bp-cnpg.webhookConfigsCreated" -}}
{{- $cnpg := (index .Values "cloudnative-pg") | default dict -}}
{{- $wh := (index $cnpg "webhook") | default dict -}}
{{- $mut := (index $wh "mutating") | default dict -}}
{{- $val := (index $wh "validating") | default dict -}}
{{- /* Upstream default for both is `create: true`; treat an unset value as
       true so existing single-operator installs keep the gate. Only an
       EXPLICIT false on both flips us into webhook-less mode. */ -}}
{{- $mutCreate := true -}}
{{- if hasKey $mut "create" -}}{{- $mutCreate = $mut.create -}}{{- end -}}
{{- $valCreate := true -}}
{{- if hasKey $val "create" -}}{{- $valCreate = $val.create -}}{{- end -}}
{{- if and (not $mutCreate) (not $valCreate) -}}false{{- else -}}true{{- end -}}
{{- end -}}

{{- define "bp-cnpg.webhookGateEnabled" -}}
{{- $gate := .Values.webhookGate | default dict -}}
{{- $gateEnabled := true -}}
{{- if hasKey $gate "enabled" -}}{{- $gateEnabled = $gate.enabled -}}{{- end -}}
{{- $configsCreated := eq (include "bp-cnpg.webhookConfigsCreated" .) "true" -}}
{{- if and $gateEnabled $configsCreated -}}true{{- else -}}false{{- end -}}
{{- end -}}
