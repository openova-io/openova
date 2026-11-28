{{- /*
openova.generatedSecrets (SEC-200) — every value a chart owns, born once.

Input: a dict
  ctx      the chart's root context
  secrets  the chart's generatedSecrets map (below)
  peers    the peer region ids (a list, may be empty)
  primary  whether this region is the primary (a bool; read when peers is
           not empty)
  self     this region's id (default .Values.openova.topology.self); needed
           when a shared value goes to peers
  namespace  the namespace of every value and every object below (SEC-217;
           default, when absent or null: the release's namespace); a
           namespace name (DNS label). It must exist; the birth Job runs
           there because OpenBao lets it create only below its own
           namespace (mounted/data/<ns>/*)

generatedSecrets:            # map; key = <name>, pattern [a-z0-9-]+
  db-password:
    delivery: mounted        # mounted (tier B) | synced (tier A); required
    registerId: ""           # delivery synced: required, the register entry; mounted: forbidden
    epoch: 1                 # integer >= 1; required; raising it rotates
    length: 32               # default 32
    symbols: 0               # default 0; at most length
    scope: sa                # mounted: sa | shared; default sa
    serviceAccount: ""       # mounted + scope sa: required
    sharedAcrossRegions: true   # default true: one value in every region,
                             # born by the primary
    key: password            # mounted only (SEC-201): the key of the document;
                             # default password; [A-Za-z0-9][-._A-Za-z0-9]*
    placeholder: false       # mounted only (SEC-201): true = the document holds
                             # the key with an empty string (no length, no symbols)
    seedFrom: {name: a, key: b}  # mounted only (SEC-226); no default (absent:
                             # no seed; {} is refused): name and key of a Secret
                             # of the value's namespace that holds the value today;
                             # the birth writes its key when the path is absent,
                             # a new random value only when the Secret does not
                             # exist, and nothing when the path holds a value
    mountedCopies: []        # synced only: [{serviceAccount: x}], one copy
                             # of the synced value as a mounted file each
    template: {}             # synced only (SEC-219): the target Secret's
                             # type, labels, annotations and data; each data
                             # entry is an ESO template (engine v2) over the
                             # value, {{ .password }}, and each joined value,
                             # {{ index . "<name>" }}
    copies: []               # synced only (SEC-219): [{name, namespace,
                             # template}], one more Secret of the value each,
                             # in the value's namespace (namespace, if given,
                             # must be it)
    join: []                 # synced only (SEC-219): names of other synced
                             # values whose value this value's Secrets also
                             # carry, each under its name; a joined value
                             # renders no Secret of its own
    source: generated        # synced only (SEC-228): generated (default) |
                             # external, a value given from outside: never
                             # generated or pushed here, only read
    writer: ""               # source external, not joined: required, who
                             # writes the store's path (printable text)

delivery mounted, scope sa:     mounted/<ns>/sa/<serviceAccount>/<name>/e<epoch>
delivery mounted, scope shared: mounted/<ns>/shared/<name>/e<epoch>
  No ExternalSecret, no PushSecret and no Secret object. The birth Job
  (openova.birthScript) writes the value with create-only rights: here, and
  for a shared value of the primary also in every peer region, through the
  peer trust of bp-openbao. A region that is not the primary
  renders nothing for a shared value: the primary writes it there.
  A placeholder is created once, empty, so a CSI mount of the path succeeds
  before the real value exists; a later writer with update rights replaces
  it and the Job never writes it again.
  A seedFrom value: the Job reads Secret <name> through the Kubernetes API as
  its ServiceAccount secrets-birth, bound by RoleBinding secrets-birth-seed to
  Role secrets-birth-seed (get on the named Secrets of the plan, nothing
  else), with a projected token of the API's audience. A Secret without the
  key, an empty value, a value that is not printable ASCII and any answer but
  200 or 404 are refused before anything is written.
delivery synced:   Password generator <name>-e<epoch>, PushSecret
  <name>-e<epoch> (IfNotExists) into SecretStore openbao at
  openova/apps/<ns>/<name>/e<epoch>, plus ClusterSecretStore vault-<peer> per
  peer for a shared value of the primary (none in another region), and the
  ExternalSecret <name> that materialises Secret <name> (key password).
  The PushSecret has deletionPolicy None: no finalizer, and the born value
  stays in the store when the PushSecret goes (an epoch raise, a roll back,
  the namespace deleted). With Delete its finalizer sends a DELETE on the
  data path, which policy eso-ns does not grant: the PushSecret would stay
  Terminating for ever. A later roll forward (IfNotExists) finds the value.
  Each mountedCopies entry: the birth Job copies Secret <name> to
  mounted/<ns>/sa/<serviceAccount>/<name>/e<epoch> in this region.
  A template gives the ExternalSecret target.template; each copies entry an
  ExternalSecret <copy name> of the same value in the same namespace (a copy
  in another namespace is refused: there, the value's producer writes it
  through, SEC-225); join adds each joined value to the data of every
  ExternalSecret of this value, and the joined value renders only its
  Password and PushSecret.
delivery synced, source external (SEC-228): no Password and no PushSecret in
  any region; the ExternalSecret <name> (and each copy) reads
  openova/apps/<ns>/<name>/e<epoch>, which its writer writes, with the
  template and join above, and carries annotation secrets.openova.io/writer.
  It takes no length, symbols, sharedAcrossRegions or mountedCopies, and joins
  only external values (one writer writes every path of its Secret). While
  the path is absent the ExternalSecret is not ready and nothing is written:
  a value given from outside is never generated.

The Job, its ServiceAccount secrets-birth and its plan ConfigMap render only
when this region has a value to write, in the namespace of the values. Their
name carries a hash of the plan, so raising an epoch runs a new Job.

Settings, under .Values.openova.secrets (all optional):
  openbaoAddress   default http://openbao.openbao.svc.cluster.local:8200
  openbaoNamespace default openbao (the peers: openbao-<peer>.<ns>.svc)
  birthImage       default harbor.openova.io/proxy-dockerhub/curlimages/curl:8.10.1
*/ -}}
{{- define "openova.generatedSecrets" -}}
{{- $ctx := .ctx -}}
{{- $ns := include "openova.secretsNamespace" (dict "in" . "ctx" $ctx) -}}
{{- $s := ($ctx.Values.openova | default dict).secrets | default dict -}}
{{- $addr := $s.openbaoAddress | default "http://openbao.openbao.svc.cluster.local:8200" -}}
{{- $baoNs := $s.openbaoNamespace | default "openbao" -}}
{{- $image := $s.birthImage | default "harbor.openova.io/proxy-dockerhub/curlimages/curl:8.10.1" -}}
{{- $peers := .peers | default list -}}
{{- if not (kindIs "slice" $peers) }}{{ fail "openova.generatedSecrets: peers must be a list of region ids" }}{{ end -}}
{{- range $peers }}{{ if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString .)) }}{{ fail (printf "openova.generatedSecrets: peer %q is not a region id" (toString .)) }}{{ end }}{{ end -}}
{{- $primary := .primary -}}
{{- if and $peers (not (kindIs "bool" $primary)) }}{{ fail "openova.generatedSecrets: primary must be true or false when peers is not empty" }}{{ end -}}
{{- $self := .self | default ((($ctx.Values.openova | default dict).topology | default dict).self | default "") -}}
{{- $plan := list -}}
{{- $seeds := list -}}
{{- $copies := list -}}
{{- $all := .secrets | default dict -}}
{{- /* SEC-219: each value that another synced value joins, and that value. */ -}}
{{- $joined := dict -}}
{{- range $n, $sp := $all }}{{ if kindIs "map" $sp }}{{ if and (eq (toString $sp.delivery) "synced") (kindIs "slice" $sp.join) }}{{ range $sp.join }}{{ if ne (toString .) $n }}{{ $_ := set $joined (toString .) $n }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end -}}
{{- $copyNames := dict -}}
{{- range $name, $spec := $all -}}
{{- $spec = $spec | default dict -}}
{{- $where := printf "openova.generatedSecrets: generatedSecrets.%s" $name -}}
{{- if not (regexMatch "^[a-z0-9-]+$" $name) }}{{ fail (printf "%s: the name must match [a-z0-9-]+" $where) }}{{ end -}}
{{- $delivery := $spec.delivery | default "" -}}
{{- if eq $delivery "" }}{{ fail (printf "%s: delivery is required (mounted or synced)" $where) }}{{ end -}}
{{- if not (has $delivery (list "mounted" "synced")) }}{{ fail (printf "%s: delivery %q is neither mounted nor synced" $where $delivery) }}{{ end -}}
{{- if not (hasKey $spec "epoch") }}{{ fail (printf "%s: epoch is required" $where) }}{{ end -}}
{{- $epoch := int $spec.epoch -}}
{{- if lt $epoch 1 }}{{ fail (printf "%s: epoch must be 1 or more" $where) }}{{ end -}}
{{- $len := int ($spec.length | default 32) -}}
{{- $sym := int ($spec.symbols | default 0) -}}
{{- if or (lt $len 1) (lt $sym 0) (gt $sym $len) }}{{ fail (printf "%s: length must be 1 or more and symbols between 0 and length" $where) }}{{ end -}}
{{- $shared := true -}}
{{- if hasKey $spec "sharedAcrossRegions" }}{{ $shared = $spec.sharedAcrossRegions }}{{ end -}}
{{- $waits := and $shared $peers (not $primary) -}}
{{- $toPeers := false -}}
{{- if and $shared $peers $primary }}{{ $toPeers = true }}{{ end -}}
{{- $reg := $spec.registerId | default "" -}}
{{- $mc := $spec.mountedCopies | default list -}}
{{- if eq $delivery "mounted" -}}
{{- if ne $reg "" }}{{ fail (printf "%s: registerId is only for delivery synced" $where) }}{{ end -}}
{{- if $mc }}{{ fail (printf "%s: mountedCopies is only for delivery synced" $where) }}{{ end -}}
{{- range list "template" "copies" "join" "source" "writer" }}{{ if hasKey $spec . }}{{ fail (printf "%s: %s is only for delivery synced" $where .) }}{{ end }}{{ end -}}
{{- $key := "password" -}}
{{- if hasKey $spec "key" -}}
{{- if not (regexMatch "^[A-Za-z0-9][-._A-Za-z0-9]*$" (toString $spec.key)) }}{{ fail (printf "%s: key %q must match [A-Za-z0-9][-._A-Za-z0-9]*" $where (toString $spec.key)) }}{{ end -}}
{{- $key = toString $spec.key -}}
{{- end -}}
{{- $ph := false -}}
{{- if hasKey $spec "placeholder" -}}
{{- if not (kindIs "bool" $spec.placeholder) }}{{ fail (printf "%s: placeholder must be true or false" $where) }}{{ end -}}
{{- $ph = $spec.placeholder -}}
{{- end -}}
{{- if and $ph (or (hasKey $spec "length") (hasKey $spec "symbols")) }}{{ fail (printf "%s: a placeholder is empty: it takes no length or symbols" $where) }}{{ end -}}
{{- $src := "-" -}}
{{- if hasKey $spec "seedFrom" -}}
{{- $sf := $spec.seedFrom -}}
{{- if not (kindIs "map" $sf) }}{{ fail (printf "%s: seedFrom must be a map of name and key" $where) }}{{ end -}}
{{- range $f, $_ := $sf }}{{ if not (has $f (list "name" "key")) }}{{ fail (printf "%s: seedFrom field %q is none of name and key" $where $f) }}{{ end }}{{ end -}}
{{- if or (not (kindIs "string" $sf.name)) (gt (len (toString $sf.name)) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" (toString $sf.name))) }}{{ fail (printf "%s: seedFrom.name must be the name of a Secret of namespace %s" $where $ns) }}{{ end -}}
{{- if or (not (kindIs "string" $sf.key)) (gt (len (toString $sf.key)) 253) (not (regexMatch "^[-._a-zA-Z0-9]+$" (toString $sf.key))) }}{{ fail (printf "%s: seedFrom.key must be a Secret key ([-._a-zA-Z0-9]+)" $where) }}{{ end -}}
{{- if $ph }}{{ fail (printf "%s: a placeholder is empty: it takes no seedFrom" $where) }}{{ end -}}
{{- $src = printf "seed:%s/%s" $sf.name $sf.key -}}
{{- end -}}
{{- $scope := $spec.scope | default "sa" -}}
{{- if not (has $scope (list "sa" "shared")) }}{{ fail (printf "%s: scope %q is neither sa nor shared" $where $scope) }}{{ end -}}
{{- $path := printf "%s/shared/%s/e%d" $ns $name $epoch -}}
{{- if eq $scope "sa" -}}
{{- $sa := $spec.serviceAccount | default "" -}}
{{- if eq $sa "" }}{{ fail (printf "%s: serviceAccount is required for scope sa" $where) }}{{ end -}}
{{- $path = printf "%s/sa/%s/%s/e%d" $ns $sa $name $epoch -}}
{{- end -}}
{{- if not $waits -}}
{{- $line := printf "%s %d %d %s %s" $path $len $sym $src (ternary "all" "self" $toPeers) -}}
{{- if $ph }}{{ $line = printf "%s 0 0 empty %s" $path (ternary "all" "self" $toPeers) }}{{ end -}}
{{- if ne $key "password" }}{{ $line = printf "%s %s" $line $key }}{{ end -}}
{{- $plan = append $plan $line -}}
{{- if ne $src "-" }}{{ $seeds = append $seeds $spec.seedFrom.name }}{{ end -}}
{{- end -}}
{{- else -}}
{{- if or (hasKey $spec "key") (hasKey $spec "placeholder") }}{{ fail (printf "%s: key and placeholder are only for delivery mounted" $where) }}{{ end -}}
{{- if hasKey $spec "seedFrom" }}{{ fail (printf "%s: seedFrom is only for delivery mounted: a synced value is born by its PushSecret, which reads one source and has no fallback" $where) }}{{ end -}}
{{- /* SEC-228: a value given from outside is only read, never generated. */ -}}
{{- $ext := false -}}
{{- if hasKey $spec "source" -}}
{{- if not (has (toString $spec.source) (list "generated" "external")) }}{{ fail (printf "%s: source %q is neither generated nor external" $where (toString $spec.source)) }}{{ end -}}
{{- $ext = eq (toString $spec.source) "external" -}}
{{- end -}}
{{- if $ext -}}
{{- range list "length" "symbols" "sharedAcrossRegions" "mountedCopies" }}{{ if hasKey $spec . }}{{ fail (printf "%s: a value given from outside (source external) is never generated or pushed here, so it takes no %s" $where .) }}{{ end }}{{ end -}}
{{- else if hasKey $spec "writer" }}{{ fail (printf "%s: writer is only for source external: a generated value is written by its PushSecret" $where) }}{{ end -}}
{{- $isJoined := hasKey $joined $name -}}
{{- if $isJoined -}}
{{- range list "registerId" "template" "copies" "join" "mountedCopies" "writer" }}{{ if hasKey $spec . }}{{ fail (printf "%s: generatedSecrets.%s joins this value, so it renders no Secret of its own and takes no %s" $where (index $joined $name) .) }}{{ end }}{{ end -}}
{{- else if eq $reg "" }}{{ fail (printf "%s: registerId is required for delivery synced" $where) }}{{ end -}}
{{- $writer := "" -}}
{{- if and $ext (not $isJoined) -}}
{{- if or (not (kindIs "string" $spec.writer)) (not (regexMatch "^[ -~]+$" (toString $spec.writer))) (eq (trim (toString $spec.writer)) "") }}{{ fail (printf "%s: writer is required for source external: who writes openova/apps/%s/%s/e%d (printable text)" $where $ns $name $epoch) }}{{ end -}}
{{- $writer = $spec.writer -}}
{{- end -}}
{{- $tpl := index $spec "template" -}}
{{- if hasKey $spec "template" }}{{ include "openova.secretsCheckTemplate" (dict "t" $tpl "where" $where) }}{{ end -}}
{{- if and $mc (kindIs "map" $tpl) -}}
{{- if hasKey $tpl "data" -}}
{{- if ne (toString (index $tpl.data "password")) "{{ .password }}" }}{{ fail (printf "%s: mountedCopies reads key password of Secret %s, so template data must hold password: \"{{ .password }}\"" $where $name) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- $cps := index $spec "copies" | default list -}}
{{- if not (kindIs "slice" $cps) }}{{ fail (printf "%s: copies must be a list" $where) }}{{ end -}}
{{- range $i, $c := $cps -}}
{{- $cw := printf "%s: copies[%d]" $where $i -}}
{{- if not (kindIs "map" $c) }}{{ fail (printf "%s must be a map of name, namespace and template" $cw) }}{{ end -}}
{{- range $f, $_ := $c }}{{ if not (has $f (list "name" "namespace" "template")) }}{{ fail (printf "%s: field %q is none of name, namespace and template" $cw $f) }}{{ end }}{{ end -}}
{{- if not (kindIs "string" $c.name) }}{{ fail (printf "%s: name is required, a Secret name" $cw) }}{{ end -}}
{{- if or (gt (len $c.name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" $c.name)) }}{{ fail (printf "%s: name %q is not a Secret name" $cw $c.name) }}{{ end -}}
{{- if hasKey $all $c.name }}{{ fail (printf "%s: name %q is the name of a value of this chart" $cw $c.name) }}{{ end -}}
{{- if hasKey $copyNames $c.name }}{{ fail (printf "%s: name %q is already a copy of generatedSecrets.%s" $cw $c.name (index $copyNames $c.name)) }}{{ end -}}
{{- $_ := set $copyNames $c.name $name -}}
{{- if hasKey $c "namespace" }}{{ if not (kindIs "invalid" $c.namespace) }}{{ if ne (toString $c.namespace) $ns }}{{ fail (printf "%s: namespace %q is not the value's namespace %q. A copy stays in its value's namespace; a value read in another namespace is written through by its producer (SEC-225)" $cw (toString $c.namespace) $ns) }}{{ end }}{{ end }}{{ end -}}
{{- if hasKey $c "template" }}{{ include "openova.secretsCheckTemplate" (dict "t" (index $c "template") "where" $cw) }}{{ end -}}
{{- end -}}
{{- $entries := list (dict "secretKey" "password" "key" (printf "openova/apps/%s/%s/e%d" $ns $name $epoch)) -}}
{{- if hasKey $spec "join" -}}
{{- if not (kindIs "slice" $spec.join) }}{{ fail (printf "%s: join must be a list of value names" $where) }}{{ end -}}
{{- $jseen := dict -}}
{{- range $spec.join -}}
{{- $j := toString . -}}
{{- if eq $j $name }}{{ fail (printf "%s: a value does not join itself" $where) }}{{ end -}}
{{- if eq $j "password" }}{{ fail (printf "%s: join names password, the key of the value itself" $where) }}{{ end -}}
{{- if hasKey $jseen $j }}{{ fail (printf "%s: join names %q twice" $where $j) }}{{ end -}}
{{- $_ := set $jseen $j true -}}
{{- $js := index $all $j -}}
{{- $jd := "" -}}
{{- if kindIs "map" $js }}{{ $jd = toString (index $js "delivery") }}{{ end -}}
{{- if ne $jd "synced" }}{{ fail (printf "%s: join names %q, which is not a synced value of this chart" $where $j) }}{{ end -}}
{{- if ne (eq (toString (index $js "source")) "external") $ext }}{{ fail (printf "%s: join names %q, whose source is not this value's: the values of one Secret have one source and one writer" $where $j) }}{{ end -}}
{{- $entries = append $entries (dict "secretKey" $j "key" (printf "openova/apps/%s/%s/e%d" $ns $j (int (index $js "epoch")))) -}}
{{- end -}}
{{- end -}}
{{- if and (not $waits) (not $ext) }}
---
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: {{ printf "%s-e%d" $name $epoch }}
  namespace: {{ $ns | quote }}
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  length: {{ $len }}
  digits: 6
  symbols: {{ $sym }}
  noUpper: false
  allowRepeat: true
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata:
  name: {{ printf "%s-e%d" $name $epoch }}
  namespace: {{ $ns | quote }}
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  refreshInterval: 1h
  updatePolicy: IfNotExists
  deletionPolicy: None
  secretStoreRefs:
    - kind: SecretStore
      name: openbao
{{- if $toPeers }}
{{- range $peers }}
    - kind: ClusterSecretStore
      name: {{ printf "vault-%s" . }}
{{- end }}
{{- end }}
  selector:
    generatorRef:
      apiVersion: generators.external-secrets.io/v1alpha1
      kind: Password
      name: {{ printf "%s-e%d" $name $epoch }}
  data:
    - match:
        secretKey: password
        remoteRef:
          remoteKey: {{ printf "openova/apps/%s/%s/e%d" $ns $name $epoch }}
          property: password
{{- end }}
{{- if not $isJoined }}
{{- include "openova.secretsExternalSecret" (dict "name" $name "ns" $ns "reg" $reg "writer" $writer "tpl" $tpl "entries" $entries) }}
{{- range $cps }}
{{- include "openova.secretsExternalSecret" (dict "name" (quote .name) "ns" $ns "reg" $reg "writer" $writer "tpl" (index . "template") "entries" $entries) }}
{{- end }}
{{- end }}
{{- range $mc -}}
{{- $csa := (. | default dict).serviceAccount | default "" -}}
{{- if eq $csa "" }}{{ fail (printf "%s: every mountedCopies entry needs a serviceAccount" $where) }}{{ end -}}
{{- $vol := printf "copy-%d" (len $copies) -}}
{{- $copies = append $copies (dict "volume" $vol "secret" $name) -}}
{{- $plan = append $plan (printf "%s/sa/%s/%s/e%d 0 0 /etc/secrets-birth/%s/password self" $ns $csa $name $epoch $vol) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $plan -}}
{{- $seeds = $seeds | uniq | sortAlpha -}}
{{- $readsApi := gt (len $seeds) 0 -}}
{{- $peerList := list -}}
{{- $anyAll := false -}}
{{- range $plan }}{{ if eq (index (splitList " " .) 4) "all" }}{{ $anyAll = true }}{{ end }}{{ end -}}
{{- if $anyAll -}}
{{- if eq (toString $self) "" }}{{ fail "openova.generatedSecrets: self (or openova.topology.self) is required when a shared value goes to the peers" }}{{ end -}}
{{- range $peers }}{{ $peerList = append $peerList (printf "%s=http://openbao-%s.%s.svc.cluster.local:8200" . . $baoNs) }}{{ end -}}
{{- end -}}
{{- $planText := printf "%s\n" (join "\n" $plan) -}}
{{- $script := include "openova.birthScript" (dict "readsApi" $readsApi) -}}
{{- $hash := sha256sum (printf "%s|%s|%s|%s|%s" $planText $script $image (join " " $peerList) $addr) | trunc 10 -}}
{{- $jobName := printf "secrets-birth-%s" $hash }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: secrets-birth
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
automountServiceAccountToken: false
{{- if $seeds }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: secrets-birth-seed
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: {{ toJson $seeds }}
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: secrets-birth-seed
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: secrets-birth-seed
subjects:
  - kind: ServiceAccount
    name: secrets-birth
    namespace: {{ $ns | quote }}
{{- end }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $jobName }}
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
data:
{{- if $seeds }}
  # <path under mounted/> <length> <symbols> <source: - = new random value | empty = placeholder | seed:<Secret>/<key> = that key of that Secret, a new random value only when the Secret does not exist | file> <reach: all | self> [<key>, default password]
{{- else }}
  # <path under mounted/> <length> <symbols> <source: - = new random value | empty = placeholder | file> <reach: all | self> [<key>, default password]
{{- end }}
  plan: |
{{ join "\n" $plan | indent 4 }}
  birth.sh: |
{{ $script | indent 4 }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ $jobName }}
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
spec:
  backoffLimit: 6
  activeDeadlineSeconds: 1800
  # Exit 3: something was written and the run failed after it. A retry could
  # write a second value; the Job ends and the epoch is raised instead.
  podFailurePolicy:
    rules:
      - action: FailJob
        onExitCodes:
          containerName: birth
          operator: In
          values: [3]
  template:
    metadata:
      labels:
        app.kubernetes.io/name: secrets-birth
        secrets.openova.io/library: secrets-lib
    spec:
      restartPolicy: Never
      serviceAccountName: secrets-birth
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 100
        runAsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: birth
          image: {{ $image | quote }}
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh"]
          args: ["/etc/secrets-birth/plan/birth.sh"]
          env:
            - {name: BAO_ADDR, value: {{ $addr | quote }}}
            - {name: BAO_K8S_AUTH_PATH, value: kubernetes}
            - {name: BAO_ROLE, value: secrets-birth}
            - {name: JWT_FILE, value: /var/run/secrets/openova-birth/jwt}
            - {name: SELF_REGION, value: {{ $self | toString | quote }}}
            - {name: PEERS, value: {{ join " " $peerList | quote }}}
            - {name: PEER_ROLE, value: {{ printf "secrets-birth-%s" $ns | quote }}}
            - {name: PEER_TOKEN_ROLE, value: {{ printf "openbao-peer-birth-%s" $ns | quote }}}
            - {name: PLAN_FILE, value: /etc/secrets-birth/plan/plan}
            - {name: WORK_DIR, value: /work}
            - {name: MOUNT, value: mounted}
{{- if $seeds }}
            - {name: KUBE_API, value: "https://kubernetes.default.svc"}
            - {name: KUBE_NAMESPACE, value: {{ $ns | quote }}}
            - {name: KUBE_DIR, value: /var/run/secrets/openova-birth-kube}
{{- end }}
          resources:
            requests:
              cpu: "10m"
              memory: "16Mi"
            limits:
              cpu: "200m"
              memory: "64Mi"
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          volumeMounts:
            - {name: plan, mountPath: /etc/secrets-birth/plan, readOnly: true}
            - {name: jwt, mountPath: /var/run/secrets/openova-birth, readOnly: true}
            - {name: work, mountPath: /work}
{{- if $seeds }}
            - {name: kube, mountPath: /var/run/secrets/openova-birth-kube, readOnly: true}
{{- end }}
{{- range $copies }}
            - {name: {{ .volume }}, mountPath: {{ printf "/etc/secrets-birth/%s" .volume }}, readOnly: true}
{{- end }}
      volumes:
        - name: plan
          configMap:
            name: {{ $jobName }}
        - name: jwt
          projected:
            sources:
              - serviceAccountToken:
                  audience: openbao
                  expirationSeconds: 600
                  path: jwt
        - name: work
          emptyDir:
            medium: Memory
            sizeLimit: 1Mi
{{- if $seeds }}
        - name: kube
          projected:
            sources:
              - serviceAccountToken:
                  expirationSeconds: 600
                  path: token
              - configMap:
                  name: kube-root-ca.crt
                  items:
                    - {key: ca.crt, path: ca.crt}
{{- end }}
{{- range $copies }}
        - name: {{ .volume }}
          secret:
            secretName: {{ .secret }}
            items:
              - {key: password, path: password}
{{- end }}
{{- end -}}
{{- end -}}

{{- /*
openova.secretsNamespace (SEC-217) — the namespace of a template's objects:
the input's namespace when it is given (not absent, not null), else the
release's namespace. Input: dict "in" <the template's input> "ctx" <the
chart's root context>. A given namespace must be a namespace name (a DNS
label: [a-z0-9-], at most 63 characters, a letter or digit at each end).
*/ -}}
{{- define "openova.secretsNamespace" -}}
{{- $ns := .ctx.Release.Namespace -}}
{{- if and (hasKey .in "namespace") (not (kindIs "invalid" .in.namespace)) -}}
{{- $ns = toString .in.namespace -}}
{{- if or (gt (len $ns) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $ns)) }}{{ fail (printf "openova.secrets: namespace %q is not a namespace name (a DNS label)" $ns) }}{{ end -}}
{{- end -}}
{{- $ns -}}
{{- end -}}

{{- /*
openova.secretsExternalSecret (SEC-219) — one ExternalSecret of a synced
value: the value's own target <name>, or a copy. Input: dict "name" (as it
is written), "ns", "reg" (the register id), "writer" (SEC-228: the writer of
a value given from outside; empty for a generated one), "tpl" (the template,
or absent) and "entries" (secretKey and remote key per value the Secret
carries: the value itself as password, then each joined value under its
name). Without a template and a writer the object is the one of SEC-200,
byte for byte.
*/ -}}
{{- define "openova.secretsExternalSecret" }}
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: {{ .name }}
  namespace: {{ .ns | quote }}
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: {{ .reg | quote }}
{{- with .writer }}
    secrets.openova.io/writer: {{ . | quote }}
{{- end }}
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: {{ .name }}
    creationPolicy: Owner
{{- with .tpl }}
    template:
      engineVersion: v2
{{- if hasKey . "type" }}
      type: {{ .type | quote }}
{{- end }}
{{- if or .labels .annotations }}
      metadata:
{{- $t := . }}
{{- range $f := list "labels" "annotations" }}
{{- with index $t $f }}
        {{ $f }}:
{{- range $k, $v := . }}
          {{ $k | quote }}: {{ $v | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- with .data }}
      data:
{{- range $k, $v := . }}
        {{ $k | quote }}: {{ $v | quote }}
{{- end }}
{{- end }}
{{- end }}
  data:
{{- range .entries }}
    - secretKey: {{ .secretKey }}
      remoteRef:
        key: {{ .key }}
        property: password
{{- end }}
{{- end -}}

{{- /*
openova.secretsCheckTemplate (SEC-219) — fails the render on a template that
is not a map of type (a Secret type), labels, annotations and data (maps of
strings; a data key is a Secret key). Input: dict "t" "where". Renders nothing.
*/ -}}
{{- define "openova.secretsCheckTemplate" -}}
{{- $w := .where -}}
{{- if not (kindIs "map" .t) }}{{ fail (printf "%s: template must be a map of type, labels, annotations and data" $w) }}{{ end -}}
{{- range $f, $_ := .t }}{{ if not (has $f (list "type" "labels" "annotations" "data")) }}{{ fail (printf "%s: template field %q is none of type, labels, annotations and data" $w $f) }}{{ end }}{{ end -}}
{{- if hasKey .t "type" }}{{ if not (kindIs "string" .t.type) }}{{ fail (printf "%s: template type must be a Secret type (a string)" $w) }}{{ end }}{{ if not (regexMatch "^[A-Za-z0-9][-._/A-Za-z0-9]*$" .t.type) }}{{ fail (printf "%s: template type %q is not a Secret type" $w .t.type) }}{{ end }}{{ end -}}
{{- range $f := list "labels" "annotations" "data" -}}
{{- if hasKey $.t $f -}}
{{- $m := index $.t $f -}}
{{- if not (kindIs "map" $m) }}{{ fail (printf "%s: template %s must be a map of strings" $w $f) }}{{ end -}}
{{- range $k, $v := $m -}}
{{- if not (kindIs "string" $v) }}{{ fail (printf "%s: template %s %q must be a string (quote it)" $w $f $k) }}{{ end -}}
{{- if and (eq $f "data") (not (regexMatch "^[-._a-zA-Z0-9]+$" $k)) }}{{ fail (printf "%s: template data key %q must match [-._a-zA-Z0-9]+" $w $k) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
