{{/*
Per-tier action expansion helper. Walks the .Values.tierActions[name].inherit
chain and concatenates each ancestor's `extras[]` rule slices, returning
a flat list of RBAC rules in inherit-order (base → leaf).

Usage:
  {{ $rules := list }}
  {{ $rules = include "catalyst.tierRules" (dict "name" "developer" "values" .Values) | fromYamlArray }}

Returns YAML-encoded array of rule objects suitable for round-trip via
fromYamlArray.
*/}}
{{- define "catalyst.tierRules" -}}
{{- $name := .name -}}
{{- $values := .values -}}
{{- $rules := list -}}
{{- $chain := list $name -}}
{{- $cur := index $values.tierActions $name -}}
{{- range $i := until 10 -}}
  {{- if and $cur (hasKey $cur "inherit") (ne (default "" $cur.inherit) "") -}}
    {{- $chain = prepend $chain $cur.inherit -}}
    {{- $cur = index $values.tierActions $cur.inherit -}}
  {{- else -}}
    {{- $cur = dict "inherit" "" -}}
  {{- end -}}
{{- end -}}
{{- range $tname := $chain -}}
  {{- $t := index $values.tierActions $tname -}}
  {{- if and $t (hasKey $t "extras") -}}
    {{- range $rule := $t.extras -}}
      {{- $rules = append $rules $rule -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{ toYaml $rules }}
{{- end -}}
