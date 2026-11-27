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
    mountedCopies: []        # synced only: [{serviceAccount: x}], one copy
                             # of the synced value as a mounted file each

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
delivery synced:   Password generator <name>-e<epoch>, PushSecret
  <name>-e<epoch> (IfNotExists) into SecretStore openbao at
  openova/apps/<ns>/<name>/e<epoch>, plus ClusterSecretStore vault-<peer> per
  peer for a shared value of the primary (none in another region), and the
  ExternalSecret <name> that materialises Secret <name> (key password).
  Each mountedCopies entry: the birth Job copies Secret <name> to
  mounted/<ns>/sa/<serviceAccount>/<name>/e<epoch> in this region.

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
{{- $copies := list -}}
{{- range $name, $spec := (.secrets | default dict) -}}
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
{{- $scope := $spec.scope | default "sa" -}}
{{- if not (has $scope (list "sa" "shared")) }}{{ fail (printf "%s: scope %q is neither sa nor shared" $where $scope) }}{{ end -}}
{{- $path := printf "%s/shared/%s/e%d" $ns $name $epoch -}}
{{- if eq $scope "sa" -}}
{{- $sa := $spec.serviceAccount | default "" -}}
{{- if eq $sa "" }}{{ fail (printf "%s: serviceAccount is required for scope sa" $where) }}{{ end -}}
{{- $path = printf "%s/sa/%s/%s/e%d" $ns $sa $name $epoch -}}
{{- end -}}
{{- if not $waits -}}
{{- $line := printf "%s %d %d - %s" $path $len $sym (ternary "all" "self" $toPeers) -}}
{{- if $ph }}{{ $line = printf "%s 0 0 empty %s" $path (ternary "all" "self" $toPeers) }}{{ end -}}
{{- if ne $key "password" }}{{ $line = printf "%s %s" $line $key }}{{ end -}}
{{- $plan = append $plan $line -}}
{{- end -}}
{{- else -}}
{{- if or (hasKey $spec "key") (hasKey $spec "placeholder") }}{{ fail (printf "%s: key and placeholder are only for delivery mounted" $where) }}{{ end -}}
{{- if eq $reg "" }}{{ fail (printf "%s: registerId is required for delivery synced" $where) }}{{ end -}}
{{- if not $waits }}
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
  deletionPolicy: Delete
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
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: {{ $name }}
  namespace: {{ $ns | quote }}
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: {{ $reg | quote }}
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: {{ $name }}
    creationPolicy: Owner
  data:
    - secretKey: password
      remoteRef:
        key: {{ printf "openova/apps/%s/%s/e%d" $ns $name $epoch }}
        property: password
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
{{- $peerList := list -}}
{{- $anyAll := false -}}
{{- range $plan }}{{ if eq (index (splitList " " .) 4) "all" }}{{ $anyAll = true }}{{ end }}{{ end -}}
{{- if $anyAll -}}
{{- if eq (toString $self) "" }}{{ fail "openova.generatedSecrets: self (or openova.topology.self) is required when a shared value goes to the peers" }}{{ end -}}
{{- range $peers }}{{ $peerList = append $peerList (printf "%s=http://openbao-%s.%s.svc.cluster.local:8200" . . $baoNs) }}{{ end -}}
{{- end -}}
{{- $planText := printf "%s\n" (join "\n" $plan) -}}
{{- $script := include "openova.birthScript" $ctx -}}
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
  # <path under mounted/> <length> <symbols> <source: - = new random value | empty = placeholder | file> <reach: all | self> [<key>, default password]
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
