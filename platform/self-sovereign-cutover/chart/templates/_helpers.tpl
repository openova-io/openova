{{/*
Expand the name of the chart.
*/}}
{{- define "bp-self-sovereign-cutover.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels — applied to every resource emitted by this chart.

Why these label keys are load-bearing
─────────────────────────────────────
The catalyst-api cutover endpoint (issue #792, products/catalyst/bootstrap/api/
internal/handler/cutover.go) discovers step ConfigMaps via:

    app.kubernetes.io/part-of=self-sovereign-cutover
    app.kubernetes.io/component=cutover-step

…and resolves order/mode/daemonsetRef via:

    bp.openova.io/cutover-order        integer 1..N
    bp.openova.io/cutover-mode         "job" | "daemonset-wait"
    bp.openova.io/cutover-daemonset    DaemonSet name (mode=daemonset-wait only)

Those labels are EMITTED PER-STEP-TEMPLATE because each step's order /
mode / daemonsetRef differ. The helpers below provide ONLY the common
ones; per-step templates inline their own.
*/}}
{{- define "bp-self-sovereign-cutover.labels" -}}
app.kubernetes.io/name: {{ include "bp-self-sovereign-cutover.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: self-sovereign-cutover
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
catalyst.openova.io/blueprint: bp-self-sovereign-cutover
catalyst.openova.io/sovereign-fqdn: {{ .Values.sovereign.fqdn | quote }}
{{- end -}}

{{/*
Image reference helper. Routes through .Values.global.imageRegistry IF
set AND the caller passes `cutoverPhase: post`. The first three steps
(01/02/03) and the registry-pivot DaemonSet MUST use upstream-public
images (cutoverPhase: pre) because they run BEFORE registries.yaml v2
takes effect on the node containerd. Post-pivot steps (05/06/07/08)
declare cutoverPhase: post so they pull from local Harbor when the
operator overlay sets global.imageRegistry.

Inputs (dict): .repository .tag .cutoverPhase (pre|post) .Values
*/}}
{{- define "bp-self-sovereign-cutover.image" -}}
{{- $repo := .repository -}}
{{- $tag := .tag -}}
{{- $registry := "" -}}
{{- if and (eq .cutoverPhase "post") .Values.global.imageRegistry -}}
{{- $registry = printf "%s/" .Values.global.imageRegistry -}}
{{- end -}}
{{- printf "%s%s:%s" $registry $repo $tag -}}
{{- end -}}

{{/*
ServiceAccount name — every step Job (stamped by catalyst-api from the
PodSpec ConfigMaps in this chart) AND the registry-pivot DaemonSet use
the same SA so RBAC is single-source.
*/}}
{{- define "bp-self-sovereign-cutover.serviceAccountName" -}}
{{- printf "%s-runner" (include "bp-self-sovereign-cutover.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
── #4975 offline-mirror coverage map (single source of truth) ───────────────
The bare host of the LOCAL Sovereign Harbor (registry.<fqdn>), derived from
sovereign.harborPublicURL. Steps 03/04/08 all resolve the local registry from
THIS one helper so "which host is local" is never hardcoded per-step (the
"harbor." vs "registry." drift that made step-08 exclude the mothership tether
and needlessly roll local refs).
*/}}
{{- define "bp-self-sovereign-cutover.localRegistryHost" -}}
{{- .Values.sovereign.harborPublicURL | trimPrefix "https://" | trimPrefix "http://" | trimSuffix "/" -}}
{{- end -}}

{{/*
The bare host of the MOTHERSHIP Harbor (harbor.openova.io), derived from
upstream.mothershipHarborURL. Post-cutover this host is a tether: its
`harbor.openova.io/proxy-<x>/PATH` refs host-swap to
`registry.<fqdn>/proxy-<x>/PATH` (path preserved).
*/}}
{{- define "bp-self-sovereign-cutover.mothershipHost" -}}
{{- .Values.upstream.mothershipHarborURL | trimPrefix "https://" | trimPrefix "http://" | trimSuffix "/" -}}
{{- end -}}

{{/*
The host→local-Harbor-project coverage map, rendered as a single-line,
env-safe, space-separated list of `host:project` tokens (empty project =
host-swap/path-preserve, used for the mothership host whose path already
carries the proxy-<x> project segment). Consumed VERBATIM by step-03
(skopeo push dest), step-04 (containerd certs.d rewrite target) and step-08
(pre-hold completeness HEAD) so all three derive identical local paths from
one declaration — this is the retirement of the three hand-maintained lists.
*/}}
{{- define "bp-self-sovereign-cutover.hostProjectMapInline" -}}
{{- $pairs := list -}}
{{- $hosts := list -}}
{{- range .Values.offlineMirror.hostProjects -}}
{{- $pairs = append $pairs (printf "%s:%s" .host (.project | default "")) -}}
{{- $hosts = append $hosts .host -}}
{{- end -}}
{{- /*
   #5026 (Refs #5010 #4977) — REGRESSION-PROOF mothership coverage. The
   mothership Harbor host (harbor.openova.io) is a sovereignty TETHER: its
   `harbor.openova.io/proxy-<x>/PATH` pod-spec refs MUST host-swap to
   `registry.<fqdn>/proxy-<x>/PATH` (path preserved) in the step-07 sweep +
   step-08 completeness gate, or velero-hcs/cnpg/etc. fail the pre-hold
   completeness gate (proven live hw243). offlineMirror.hostProjects already
   carries it, but an operator overlay that rewrites hostProjects could drop
   it — so GUARANTEE it here (empty project = host-swap) whenever it isn't
   already present. The map is the single source both step-03/04/07/08 read,
   so this one guard fixes the whole class at the source. */ -}}
{{- $mo := (include "bp-self-sovereign-cutover.mothershipHost" .) -}}
{{- if and $mo (not (has $mo $hosts)) -}}
{{- $pairs = append $pairs (printf "%s:" $mo) -}}
{{- end -}}
{{- join " " $pairs -}}
{{- end -}}

{{/*
Hosts EXCLUDED from the offline mirror (handled elsewhere / un-mirrorable):
xpkg.upbound.io is pivoted by step-11 (crossplaneProviderPivot) and bypasses
containerd. Space-separated, env-safe.
*/}}
{{- define "bp-self-sovereign-cutover.excludedHostsInline" -}}
{{- .Values.offlineMirror.excludedHosts | default (list) | join " " -}}
{{- end -}}

{{/*
Image-path substrings EXCLUDED from the offline mirror + the step-08 roll-set
(rancher/k3s = per-Org vcluster distro, un-mirrorable on the host plane;
loft-sh/vcluster = handled by step-10 vcluster-registry-pivot). Space-separated.
*/}}
{{- define "bp-self-sovereign-cutover.excludedSubstringsInline" -}}
{{- .Values.offlineMirror.excludedImageSubstrings | default (list) | join " " -}}
{{- end -}}
