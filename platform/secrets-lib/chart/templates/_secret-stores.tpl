{{- /*
openova.secretStores (SEC-200) — the External Secrets reader of a namespace.
Input: the chart's root context (the release's namespace), or (SEC-217)
dict "ctx" <the root context> "namespace" <name>: the stores of the namespace
that openova.generatedSecrets is given, where its synced values read.

  ServiceAccount eso-reader    the identity of the namespace's stores; OpenBao
                               role eso-ns binds it in every namespace
                               (platform/openbao/chart/templates/_config.tpl)
  SecretStore openbao          tier A: mount secret/, keys under
                               openova/apps/<namespace>/ only (policy eso-ns)

There is no store for mount mounted/: a tier B value is born by the birth Job
of openova.generatedSecrets, never pushed by External Secrets (SEC-140 step 2,
SEC-200).

Settings, under .Values.openova.secrets (all optional):
  openbaoAddress   default http://openbao.openbao.svc.cluster.local:8200
*/ -}}
{{- define "openova.secretStores" -}}
{{- $ctx := . -}}
{{- $in := dict -}}
{{- if hasKey . "ctx" }}{{ $ctx = .ctx }}{{ $in = . }}{{ end -}}
{{- $ns := include "openova.secretsNamespace" (dict "in" $in "ctx" $ctx) -}}
{{- $s := ($ctx.Values.openova | default dict).secrets | default dict -}}
{{- $addr := $s.openbaoAddress | default "http://openbao.openbao.svc.cluster.local:8200" }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eso-reader
  namespace: {{ $ns | quote }}
  labels:
    app.kubernetes.io/component: eso-reader
    secrets.openova.io/library: secrets-lib
automountServiceAccountToken: false
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: openbao
  namespace: {{ $ns | quote }}
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  provider:
    vault:
      server: {{ $addr | quote }}
      path: secret
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: eso-ns
          serviceAccountRef:
            name: eso-reader
{{- end -}}
