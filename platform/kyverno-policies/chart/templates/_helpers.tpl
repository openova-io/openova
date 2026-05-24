{{- /*
bp-kyverno-policies — shared template helpers.

Wave 5.90 phase 2 (#2436) introduces a global `bootstrapMode` toggle
that overrides every per-policy `action` (Audit | Enforce) to Audit
while a Sovereign is still mid-Phase-1. The post-handover mechanism
(catalyst-api sets `compliancePolicies.bootstrapMode: false` on the
HelmRelease values, Flux reconciles, Helm upgrades the policies to
their canonical Enforce/Audit defaults) lets governance policies be
authored once with their target-state action without blocking Phase 1
bootstrap.

Why this matters: when a fresh Sovereign installs bp-kyverno-policies
mid-Phase-1, many bp-* workloads have not yet rendered their
annotations / replica counts / probe configs / Harbor image refs.
Policies in Enforce mode then fail-closed on Pod admission, wedging
bp-keycloak / bp-cnpg / etc., cascading to a deadlocked Phase 1
(observed live on d3c6268c 2026-05-24T20:30Z — root cause for
Wave 5.90 #2436). Audit mode is non-blocking; PolicyReport rows still
emit for the Compliance Dashboard, but admission passes.
*/ -}}

{{- /*
bp-kyverno-policies.action — resolves the effective validationFailureAction
for a single ClusterPolicy.

  - When `.Values.compliancePolicies.bootstrapMode` is true (default for
    fresh Sovereigns), returns "Audit" regardless of the per-policy
    canonical action.
  - When false (post-handover Catalyst flips it), returns the per-policy
    canonical action (.policy), defaulting to "Audit" when unset.

Usage in a ClusterPolicy template:

  spec:
    validationFailureAction: {{ include "bp-kyverno-policies.action" (dict "ctx" . "policy" .Values.compliancePolicies.<name>.action) }}
*/ -}}
{{- define "bp-kyverno-policies.action" -}}
{{- if .ctx.Values.compliancePolicies.bootstrapMode -}}
Audit
{{- else -}}
{{- .policy | default "Audit" -}}
{{- end -}}
{{- end -}}
