{{- /*
capabilityProbe.contracts: the contract versions this chart can mount and probe. Each holds the
probe name of spec.contracts[].probe and its keys, in the order of the contract file
(platform/_capabilities/<capability>.yaml); chart/tests/render.sh compares them with that file.
*/ -}}
{{- define "capabilityProbe.contracts" -}}
{{- toJson (dict
  "object-storage/v1" (dict "probe" "object-storage-v1" "keys" (list "type" "provider" "endpoint" "region" "bucket" "path-style" "access-key-id" "secret-access-key" "ca.crt"))
) }}
{{- end }}

{{- /*
capabilityProbe.bindings: the checked bound aliases of .Values.bindings as a JSON list sorted by
alias; each element: alias, capability, contract, path, mountPath, probe, keys. The render fails
on a binding this chart has no probe for, on a path outside this namespace's shared tier, and on a
mount path that is not /bindings/<alias>.
*/ -}}
{{- define "capabilityProbe.bindings" -}}
{{- $contracts := include "capabilityProbe.contracts" . | fromJson }}
{{- $bindings := .Values.bindings | default dict }}
{{- $out := list }}
{{- range $alias := keys $bindings | sortAlpha }}
{{- $b := get $bindings $alias }}
{{- $at := printf "bp-capability-probe: bindings.%s" $alias }}
{{- if not (regexMatch "^[a-z][a-z0-9-]{0,30}$" $alias) }}{{ fail (printf "%s: the alias is not an alias" $at) }}{{ end }}
{{- if not (kindIs "map" $b) }}{{ fail (printf "%s is not a map" $at) }}{{ end }}
{{- $id := printf "%s/%s" (toString ($b.capability | default "")) (toString ($b.contract | default "")) }}
{{- if not (hasKey $contracts $id) }}{{ fail (printf "%s: no probe for capability %q contract %q" $at (toString ($b.capability | default "")) (toString ($b.contract | default ""))) }}{{ end }}
{{- $path := toString ($b.path | default "") }}
{{- if not (regexMatch (printf "^mounted/data/%s/shared/binding-%s/e[1-9][0-9]*$" $.Release.Namespace $alias) $path) }}
{{- fail (printf "%s: path %q is not mounted/data/%s/shared/binding-%s/e<N>" $at $path $.Release.Namespace $alias) }}
{{- end }}
{{- $mount := toString ($b.mountPath | default "") }}
{{- if ne $mount (printf "/bindings/%s" $alias) }}{{ fail (printf "%s: mountPath %q, want /bindings/%s" $at $mount $alias) }}{{ end }}
{{- $c := get $contracts $id }}
{{- $out = append $out (dict "alias" $alias "capability" $b.capability "contract" $b.contract "path" $path "mountPath" $mount "probe" $c.probe "keys" $c.keys) }}
{{- end }}
{{- if and $out (not .Values.image.tag) }}
{{- fail "bp-capability-probe: image.tag is empty; build-capability-probe.yaml pins it" }}
{{- end }}
{{- toJson $out }}
{{- end }}

{{- define "capabilityProbe.labels" -}}
app.kubernetes.io/name: bp-capability-probe
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-capability-probe
{{- end }}

{{- define "capabilityProbe.image" -}}
{{ printf "%s:%s" .Values.image.repository .Values.image.tag }}
{{- end }}

{{- /* capabilityProbe.volumes: one read-only CSI volume per bound alias. Argument: root, bindings. */ -}}
{{- define "capabilityProbe.volumes" -}}
{{- range $b := .bindings }}
- name: binding-{{ $b.alias }}
  csi:
    driver: secrets-store.csi.k8s.io
    readOnly: true
    volumeAttributes:
      secretProviderClass: {{ $.root.Release.Name }}-{{ $b.alias }}
{{- end }}
{{- end }}

{{- define "capabilityProbe.volumeMounts" -}}
{{- range $b := .bindings }}
- name: binding-{{ $b.alias }}
  mountPath: {{ $b.mountPath }}
  readOnly: true
{{- end }}
{{- end }}

{{- define "capabilityProbe.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
runAsUser: 65534
runAsGroup: 65534
capabilities:
  drop: [ALL]
seccompProfile:
  type: RuntimeDefault
{{- end }}
