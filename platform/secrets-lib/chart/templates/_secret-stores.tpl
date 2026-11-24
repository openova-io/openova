{{- /*
openova.secretStores (SEC-200) — the External Secrets reader of the release
namespace. Input: the chart's root context.

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
{{- $s := (.Values.openova | default dict).secrets | default dict -}}
{{- $addr := $s.openbaoAddress | default "http://openbao.openbao.svc.cluster.local:8200" }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eso-reader
  namespace: {{ .Release.Namespace | quote }}
  labels:
    app.kubernetes.io/component: eso-reader
    secrets.openova.io/library: secrets-lib
automountServiceAccountToken: false
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: openbao
  namespace: {{ .Release.Namespace | quote }}
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
