{{/*
catalyst.orgSmtpChecksum — deterministic hash of the Sovereign SMTP source
Secret (catalyst-system/sovereign-smtp-credentials) so the org-service Pods
that consume SMTP_* from org-services-secrets can stamp it as a pod-template
annotation and AUTO-ROLL the moment the seed flips empty→populated (#4919).

Why this exists (root cause of the hw233 funnel row-84 ❌):
  org-services/{auth,notification} read SMTP_HOST/PORT/FROM/USER/PASS via
  secretKeyRef, which the kubelet resolves ONCE, at Pod start.
  products/catalyst/bootstrap/api/.../sovereign_smtp_seed.go seeds the relay
  (mail.openova.io:587) ASYNCHRONOUSLY from the mothership at provision time;
  on a fresh Sovereign it can land AFTER these Pods have already started.
  org-services-secrets.yaml's #934 source-wins lookup then bakes the correct
  bytes on the NEXT reconcile, but the already-running Pod keeps its stale
  EMPTY SMTP_* env — so POST /auth/magic-link (the anonymous signup PIN) and
  every notification email silently no-send until an unrelated reconcile
  happens to roll the Pod. That accidental roll is exactly what masked the bug
  on hw233 region-a (org-services-secrets correct since 15:54, auth Pod
  incidentally rolled 17:17), leaving the funnel dead for the whole window in
  between.

  Stamping this hash on the consumer Pod templates makes the roll DETERMINISTIC:
  when the seed appears the hash changes → Flux rolls auth + notification → the
  new Pods resolve the now-populated env → the PIN sends.

Mirrors the source key read in org-services-secrets.yaml (#934): canonical
smtp-host/-port/-from/-user/-pass with legacy host/port/from/user/password
fallback. During `helm template` (no cluster lookup) or pre-seed it hashes the
empty sentinel; a change away from that sentinel IS the empty→populated
transition we want to roll on. Uses only the seed bytes (not org-services-
secrets' persistent-random JWT/ADMIN fields) so the hash never flaps on the
first-install randAlphaNum path.
*/}}
{{- define "catalyst.orgSmtpChecksum" -}}
{{- $ns := .Values.orgSecrets.smtp.sovereignNamespace | default "catalyst-system" -}}
{{- $name := .Values.orgSecrets.smtp.sovereignSecretName | default "sovereign-smtp-credentials" -}}
{{- $sec := lookup "v1" "Secret" $ns $name -}}
{{- $d := dict -}}
{{- if and $sec $sec.data -}}{{- $d = $sec.data -}}{{- end -}}
{{- $host := (index $d "smtp-host" | default (index $d "host" | default "")) -}}
{{- $port := (index $d "smtp-port" | default (index $d "port" | default "")) -}}
{{- $from := (index $d "smtp-from" | default (index $d "from" | default "")) -}}
{{- $user := (index $d "smtp-user" | default (index $d "user" | default "")) -}}
{{- $pass := (index $d "smtp-pass" | default (index $d "password" | default "")) -}}
{{- printf "%s|%s|%s|%s|%s" $host $port $from $user $pass | sha256sum -}}
{{- end -}}
