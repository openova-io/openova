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

  - While the bootstrap window is open, returns "Audit" regardless of the
    per-policy canonical action. The window is open when
    `.Values.compliancePolicies.bootstrapMode` is true (the default) and
    `.Values.compliancePolicies.sovereignConsume` is not "local" (unset or
    empty counts as "origin", the default).
  - When the window is closed, returns the per-policy canonical action
    (.policy), defaulting to "Audit" when unset.

Usage in a ClusterPolicy template:

  spec:
    validationFailureAction: {{ include "bp-kyverno-policies.action" (dict "ctx" . "policy" .Values.compliancePolicies.<name>.action) }}
*/ -}}
{{- define "bp-kyverno-policies.action" -}}
{{- if and .ctx.Values.compliancePolicies.bootstrapMode (ne (.ctx.Values.compliancePolicies.sovereignConsume | default "origin") "local") -}}
Audit
{{- else -}}
{{- .policy | default "Audit" -}}
{{- end -}}
{{- end -}}

{{- /*
bp-kyverno-policies.actions — the validationActions of a ValidatingPolicy
(policies.kyverno.io). The same rule as bp-kyverno-policies.action above:
[Audit] while the bootstrap window is open, else [Deny] when the per-policy
action is Enforce and [Audit] for any other action or none.

Usage in a ValidatingPolicy template:

  spec:
    validationActions: {{ include "bp-kyverno-policies.actions" (dict "ctx" . "policy" .Values.compliancePolicies.<name>.action) }}
*/ -}}
{{- define "bp-kyverno-policies.actions" -}}
{{- if or (and .ctx.Values.compliancePolicies.bootstrapMode (ne (.ctx.Values.compliancePolicies.sovereignConsume | default "origin") "local")) (ne (.policy | default "Audit") "Enforce") -}}
[Audit]
{{- else -}}
[Deny]
{{- end -}}
{{- end -}}

{{- /*
bp-kyverno-policies.excludeNamespacesExpr — a CEL expression, for a match
condition of a ValidatingPolicy, that is true when the object is in none of
the namespaces of the list it is given. A cluster-scoped object has the
namespace '' and is in none of them. An empty list excludes nothing.

Usage:

  matchConditions:
    - name: exclude-namespaces
      expression: {{ include "bp-kyverno-policies.excludeNamespacesExpr" .Values.compliancePolicies.<name>.excludeNamespaces | quote }}
*/ -}}
{{- define "bp-kyverno-policies.excludeNamespacesExpr" -}}
!(object.metadata.?namespace.orValue('') in [{{ range $i, $ns := . }}{{ if $i }}, {{ end }}'{{ $ns }}'{{ end }}])
{{- end -}}

{{- /*
bp-kyverno-policies.reportsReadChecksum — a checksum of the read that
templates/rbac-reports-matched-read.yaml grants the reports controller, for
an annotation of each ValidatingPolicy that read is for.

Kyverno v1.19 sets RBACPermissionsGranted when it reconciles a policy, and it
reconciles a policy only when the policy is added, when its resourceVersion
changes, or when its webhook turns ready (pkg/controllers/policystatus). It
does not watch RBAC and does not requeue on a timer. A grant added on a
running Sovereign therefore changes nothing by itself: the condition stays
False until the policy changes. With this annotation a change of the grant is
a change of the policy, so the upgrade that brings the grant also patches the
policy, and Kyverno checks the permissions again. Helm applies a ClusterRole
before any ValidatingPolicy (a kind Helm does not know, applied last), so the
role exists, and is normally aggregated, before the policy changes.

Usage, under metadata.annotations:

    catalyst.openova.io/reports-read-checksum: {{ include "bp-kyverno-policies.reportsReadChecksum" . | quote }}
*/ -}}
{{- define "bp-kyverno-policies.reportsReadChecksum" -}}
{{- include (print .Template.BasePath "/rbac-reports-matched-read.yaml") . | sha256sum | trunc 16 -}}
{{- end -}}
