{{/*
network-policies.allow: the allow object of one namespace.

An allow file templates/allow/<namespace>.yaml defines the template
"allow.<namespace>.rules", a YAML map with the lists `ingress` and `egress`,
and then calls this helper with the root context. _README.tpl shows a whole
file.

The helper takes the namespace from the name of the file, so the file, the
object `allow-<namespace>` and the namespace the object selects cannot
differ. The file name must be <namespace>.yaml, with a namespace name. It
renders nothing unless .Values.enabled is true and the namespace is in
.Values.enforcedNamespaces. It adds what every allow object holds: the
selector (the pods of the namespace that are not session pods, the label
access.openova.io/session) and same-namespace traffic in both directions.

It stops the render when the rules break the rule template (_README.tpl):
  - a key other than ingress and egress;
  - in a rule, a key other than toPorts and the peer keys of its direction
    (ingress: fromEndpoints, fromEntities, fromCIDR, fromCIDRSet; egress:
    toEndpoints, toEntities, toCIDR, toCIDRSet, toFQDNs);
  - a rule that names no peer, or no port, or the port 0 (every port);
  - a port that is not a number from 1 to 65535 in decimal without a leading
    zero (Cilium reads "00" and "0x0" as the port 0), or a range from the
    port 1 to an endPort of 65535 (every port);
  - an endpoint selector that names no namespace by the label
    io.kubernetes.pod.namespace (in matchLabels, or a matchExpressions term
    with the operator In);
  - an entity other than ingress, kube-apiserver, host and remote-node;
  - a range wider than /8 (IPv4) or /16 (IPv6);
  - a DNS name with a wildcard other than one leading "*." or "**." before a
    name that holds a dot.
*/}}
{{- define "network-policies.allow" -}}
{{- $file := base .Template.Name -}}
{{- $ns := trimSuffix ".yaml" $file -}}
{{- if or (not (hasSuffix ".yaml" $file)) (not (regexMatch "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" $ns)) -}}
{{- fail (printf "templates/allow/%s: an allow file is named <namespace>.yaml, with a namespace name" $file) -}}
{{- end -}}
{{- if and .Values.enabled (has $ns (.Values.enforcedNamespaces | default list)) -}}
{{- $where := printf "templates/allow/%s.yaml" $ns -}}
{{- $rules := include (printf "allow.%s.rules" $ns) . | fromYaml -}}
{{- if hasKey $rules "Error" -}}
{{- fail (printf "%s: the rules are not YAML: %s" $where (get $rules "Error")) -}}
{{- end -}}
{{- range $key, $_ := $rules -}}
{{- if not (has $key (list "ingress" "egress")) -}}
{{- fail (printf "%s: the rules hold the key %s; only ingress and egress are allowed" $where $key) -}}
{{- end -}}
{{- end -}}
{{- $peers := dict "ingress" (list "fromEndpoints" "fromEntities" "fromCIDR" "fromCIDRSet") "egress" (list "toEndpoints" "toEntities" "toCIDR" "toCIDRSet" "toFQDNs") -}}
{{- range $dir := list "ingress" "egress" -}}
{{- range $i, $rule := (get $rules $dir | default list) -}}
{{- $at := printf "%s: %s rule %d" $where $dir $i -}}
{{- $named := 0 -}}
{{- range $key, $value := $rule -}}
{{- if has $key (get $peers $dir) -}}
{{- if $value -}}{{- $named = add1 $named -}}{{- end -}}
{{- else if ne $key "toPorts" -}}
{{- fail (printf "%s holds the key %s; a rule holds toPorts and the peer keys %s" $at $key (join ", " (get $peers $dir))) -}}
{{- end -}}
{{- end -}}
{{- if not $named -}}
{{- fail (printf "%s names no peer" $at) -}}
{{- end -}}
{{- if not $rule.toPorts -}}
{{- fail (printf "%s names no port" $at) -}}
{{- end -}}
{{- range $rule.toPorts -}}
{{- if not .ports -}}
{{- fail (printf "%s names no port" $at) -}}
{{- end -}}
{{- range .ports -}}
{{- if not .port -}}
{{- fail (printf "%s names the port 0 or no port" $at) -}}
{{- end -}}
{{- $port := toString .port -}}
{{- if or (not (regexMatch "^[1-9][0-9]{0,4}$" $port)) (gt (atoi $port) 65535) -}}
{{- fail (printf "%s names the port %s; a port is a number from 1 to 65535 in decimal without a leading zero" $at $port) -}}
{{- end -}}
{{- if and .endPort (eq (atoi $port) 1) (ge (atoi (toString .endPort)) 65535) -}}
{{- fail (printf "%s names the port range %s to %s, every port" $at $port (toString .endPort)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range concat ($rule.fromEndpoints | default list) ($rule.toEndpoints | default list) -}}
{{- $sel := . | default dict -}}
{{- $byLabel := or (hasKey ($sel.matchLabels | default dict) "io.kubernetes.pod.namespace") (hasKey ($sel.matchLabels | default dict) "k8s:io.kubernetes.pod.namespace") -}}
{{- $byTerm := false -}}
{{- range ($sel.matchExpressions | default list) -}}
{{- if and (has .key (list "io.kubernetes.pod.namespace" "k8s:io.kubernetes.pod.namespace")) (eq .operator "In") -}}
{{- $byTerm = true -}}
{{- end -}}
{{- end -}}
{{- if not (or $byLabel $byTerm) -}}
{{- fail (printf "%s names an endpoint selector without a namespace (the label io.kubernetes.pod.namespace)" $at) -}}
{{- end -}}
{{- end -}}
{{- range concat ($rule.fromEntities | default list) ($rule.toEntities | default list) -}}
{{- if not (has . (list "ingress" "kube-apiserver" "host" "remote-node")) -}}
{{- fail (printf "%s names the entity %s; the entities of an allow rule are ingress, kube-apiserver, host and remote-node" $at .) -}}
{{- end -}}
{{- end -}}
{{- $cidrs := concat ($rule.fromCIDR | default list) ($rule.toCIDR | default list) -}}
{{- range concat ($rule.fromCIDRSet | default list) ($rule.toCIDRSet | default list) -}}
{{- $cidrs = append $cidrs (.cidr | default "") -}}
{{- end -}}
{{- range $cidrs -}}
{{- $cidr := toString . -}}
{{- $bits := 128 -}}
{{- $min := 16 -}}
{{- if not (contains ":" $cidr) -}}{{- $bits = 32 -}}{{- $min = 8 -}}{{- end -}}
{{- $len := $bits -}}
{{- if contains "/" $cidr -}}{{- $len = atoi (last (splitList "/" $cidr)) -}}{{- end -}}
{{- if or (eq $cidr "") (lt $len $min) -}}
{{- fail (printf "%s names the range %s, wider than /%d" $at $cidr $min) -}}
{{- end -}}
{{- end -}}
{{- range ($rule.toFQDNs | default list) -}}
{{- $name := .matchName | default "" -}}
{{- if .matchPattern -}}
{{- $name = .matchPattern | trimPrefix "**." | trimPrefix "*." -}}
{{- end -}}
{{- if or (contains "*" $name) (not (contains "." $name)) -}}
{{- fail (printf "%s names the DNS name %s; a wildcard is one leading *. or **. before a name that holds a dot" $at (.matchPattern | default .matchName)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end }}
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: allow-{{ $ns }}
spec:
  endpointSelector:
    matchLabels:
      io.kubernetes.pod.namespace: {{ $ns }}
    matchExpressions:
      - key: access.openova.io/session
        operator: DoesNotExist
  ingress:
    - fromEndpoints:
        - matchLabels:
            io.kubernetes.pod.namespace: {{ $ns }}
    {{- with $rules.ingress }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  egress:
    - toEndpoints:
        - matchLabels:
            io.kubernetes.pod.namespace: {{ $ns }}
    {{- with $rules.egress }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
{{- end -}}
{{- end -}}
