{{- /*
objectBucket.contexts: the checked Contexts of .Values.buckets as a JSON list. Each element:
key (<consumer namespace>/<name>/<alias>), name, alias, namespace, path, bucket, user, versioning,
retentionDays. The render fails on an entry that is not a Context of object-storage v1, on a
binding path that is not the consumer's own, on a Context listed twice and on a retentionDays above
255, which the in-cluster implementation does not offer. The bucket name is the
entry name and a digest of the key, so two consumers of one name never share a bucket; the
SeaweedFS identity of the key has the bucket's name.
*/ -}}
{{- define "objectBucket.contexts" -}}
{{- $out := list }}
{{- $seen := dict }}
{{- $dns := "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" }}
{{- range $i, $e := (.Values.buckets | default list) }}
{{- $at := printf "bp-object-bucket: buckets[%d]" $i }}
{{- if not (kindIs "map" $e) }}{{ fail (printf "%s is not a map" $at) }}{{ end }}
{{- $name := toString ($e.name | default "") }}
{{- if not (regexMatch $dns $name) }}{{ fail (printf "%s: name %q is not an Application name" $at $name) }}{{ end }}
{{- if ne (toString ($e.capability | default "")) "object-storage" }}{{ fail (printf "%s: capability %q, want object-storage" $at (toString ($e.capability | default ""))) }}{{ end }}
{{- if ne (toString ($e.contract | default "")) "v1" }}{{ fail (printf "%s: contract %q, want v1" $at (toString ($e.contract | default ""))) }}{{ end }}
{{- $alias := toString ($e.alias | default "") }}
{{- if not (regexMatch "^[a-z][a-z0-9-]{0,30}$" $alias) }}{{ fail (printf "%s: alias %q is not an alias" $at $alias) }}{{ end }}
{{- $consumer := $e.consumer | default dict }}
{{- if not (kindIs "map" $consumer) }}{{ fail (printf "%s: consumer is not a map" $at) }}{{ end }}
{{- $ns := toString ($consumer.namespace | default "") }}
{{- if not (regexMatch $dns $ns) }}{{ fail (printf "%s: consumer.namespace %q is not a namespace" $at $ns) }}{{ end }}
{{- $binding := $e.binding | default dict }}
{{- if not (kindIs "map" $binding) }}{{ fail (printf "%s: binding is not a map" $at) }}{{ end }}
{{- $path := toString ($binding.path | default "") }}
{{- if not (regexMatch (printf "^mounted/data/%s/shared/binding-%s/e[1-9][0-9]*$" $ns $alias) $path) }}
{{- fail (printf "%s: binding.path %q is not mounted/data/%s/shared/binding-%s/e<N>" $at $path $ns $alias) }}
{{- end }}
{{- $versioning := false }}
{{- if hasKey $e "versioning" }}
{{- if not (kindIs "bool" $e.versioning) }}{{ fail (printf "%s: versioning %v is not true or false" $at $e.versioning) }}{{ end }}
{{- $versioning = $e.versioning }}
{{- end }}
{{- $days := 0 }}
{{- if hasKey $e "retentionDays" }}
{{- $r := $e.retentionDays }}
{{- if not (or (kindIs "float64" $r) (kindIs "int" $r) (kindIs "int64" $r)) }}{{ fail (printf "%s: retentionDays %v is not a number" $at $r) }}{{ end }}
{{- if or (lt (float64 $r) 0.0) (ne (float64 $r) (float64 (int64 $r))) }}{{ fail (printf "%s: retentionDays %v is not a whole number of days, 0 or more" $at $r) }}{{ end }}
{{- /* fs.configure of SeaweedFS 4.22 takes a time to live of 1 to 255 of one unit. */}}
{{- if gt (float64 $r) 255.0 }}{{ fail (printf "%s: retentionDays %v is more than 255, the most the in-cluster implementation offers" $at $r) }}{{ end }}
{{- $days = int64 $r }}
{{- end }}
{{- $key := printf "%s/%s/%s" $ns $name $alias }}
{{- if hasKey $seen $key }}{{ fail (printf "%s: the Context %s is listed twice" $at $key) }}{{ end }}
{{- $_ := set $seen $key true }}
{{- $bucket := printf "%s-%s" (trunc 54 $name | trimSuffix "-") (sha256sum $key | trunc 8) }}
{{- $out = append $out (dict "key" $key "name" $name "alias" $alias "namespace" $ns "path" $path "bucket" $bucket "user" $bucket "versioning" $versioning "retentionDays" $days) }}
{{- end }}
{{- if and $out (ne .Release.Namespace .Values.seaweedfs.namespace) }}
{{- fail (printf "bp-object-bucket: a release with Contexts runs in namespace %s (seaweedfs.namespace), not %s" .Values.seaweedfs.namespace .Release.Namespace) }}
{{- end }}
{{- toJson $out }}
{{- end }}

{{- define "objectBucket.labels" -}}
app.kubernetes.io/name: bp-object-bucket
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-object-bucket
{{- end }}

{{- /*
objectBucket.job: the Job of one Context. Arguments: root (the chart context), ctx (an element of
objectBucket.contexts), mode (create or remove), name, hook (a Helm hook or empty).
*/ -}}
{{- define "objectBucket.job" -}}
{{- $root := .root }}
{{- $ctx := .ctx }}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "objectBucket.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .mode }}
    catalyst.openova.io/context-bucket: {{ $ctx.bucket }}
  {{- with .hook }}
  annotations:
    helm.sh/hook: {{ . }}
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
  {{- end }}
spec:
  backoffLimit: 3
  activeDeadlineSeconds: 600
  template:
    metadata:
      labels:
        {{- include "objectBucket.labels" $root | nindent 8 }}
        app.kubernetes.io/component: {{ .mode }}
        catalyst.openova.io/context-bucket: {{ $ctx.bucket }}
    spec:
      restartPolicy: Never
      serviceAccountName: {{ $root.Values.serviceAccount }}
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: context
          image: {{ $root.Values.seaweedfs.image | quote }}
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-c"]
          args:
            - {{ $root.Files.Get "files/context.sh" | quote }}
          env:
            - {name: MODE, value: {{ .mode | quote }}}
            - {name: CONTEXT, value: {{ $ctx.key | quote }}}
            - {name: BUCKET, value: {{ $ctx.bucket | quote }}}
            - {name: USER_NAME, value: {{ $ctx.user | quote }}}
            - {name: BINDING_PATH, value: {{ $ctx.path | quote }}}
            - {name: VERSIONING, value: {{ $ctx.versioning | toString | quote }}}
            - {name: RETENTION_DAYS, value: {{ $ctx.retentionDays | int64 | toString | quote }}}
            - {name: SEAWEEDFS_MASTER, value: {{ $root.Values.seaweedfs.master | quote }}}
            - {name: SEAWEEDFS_FILER, value: {{ $root.Values.seaweedfs.filer | quote }}}
            - {name: OPENBAO_ADDR, value: {{ $root.Values.openbao.addr | trimSuffix "/" | quote }}}
            - {name: OPENBAO_AUTH_PATH, value: {{ $root.Values.openbao.kubernetesAuthPath | quote }}}
            - {name: OPENBAO_ROLE, value: {{ $root.Values.openbao.role | quote }}}
            - {name: TOKEN_FILE, value: /var/run/secrets/openbao/token}
            - {name: PROVIDER_JSON, value: {{ toJson "bp-object-bucket" | quote }}}
            - {name: ENDPOINT_JSON, value: {{ toJson $root.Values.seaweedfs.s3Endpoint | quote }}}
            - {name: REGION_JSON, value: {{ toJson $root.Values.seaweedfs.region | quote }}}
            - {name: CA_JSON, value: {{ toJson ($root.Values.seaweedfs.caBundle | default "") | quote }}}
            - {name: HOME, value: /tmp}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          resources:
            {{- toYaml $root.Values.resources | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
            - name: openbao-token
              mountPath: /var/run/secrets/openbao
              readOnly: true
      volumes:
        - name: tmp
          emptyDir:
            medium: Memory
            sizeLimit: 16Mi
        - name: openbao-token
          projected:
            sources:
              - serviceAccountToken:
                  audience: openbao
                  expirationSeconds: 600
                  path: token
{{- end }}
