{{/*
Catalyst-curated cert-manager helpers — issuer naming, contact email
resolution. Kept tiny so the wrapper chart stays close to upstream.
*/}}

{{/*
catalyst.certManager.issuerEmail — Sovereign-scoped contact email for
the ACME account. Falls back to a generic Catalyst contact if the
operator did not pass one in. Lets Encrypt uses this for expiry
reminders and policy notifications.
*/}}
{{- define "catalyst.certManager.issuerEmail" -}}
{{- default "catalyst@openova.io" .Values.catalystIssuer.email -}}
{{- end -}}

{{/*
catalyst.certManager.acmeServer — Lets Encrypt directory URL. Defaults
to the production endpoint; tests / staging Sovereigns flip to staging
via Helm values rather than editing this template.
*/}}
{{- define "catalyst.certManager.acmeServer" -}}
{{- default "https://acme-v02.api.letsencrypt.org/directory" .Values.catalystIssuer.acmeServer -}}
{{- end -}}
