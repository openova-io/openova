{{/* chart name */}}
{{- define "agenity.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* fully-qualified name */}}
{{- define "agenity.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "agenity.labels" -}}
app.kubernetes.io/name: {{ include "agenity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-agenity
{{- end -}}

{{/* selector labels */}}
{{- define "agenity.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agenity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* image ref */}}
{{- define "agenity.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- /*
agenity.gateHostname — the public hostname the oidc-gate companion (and the
chart's own HTTPRoute) owns. The org-gitops emitter passes the per-Org host as
httpRoute.hostnames[0] (agenity.<slug>.<pool>); else derive agenity.<fqdn> from
sovereignFqdn; else "" (fail-closed — Inviolable Principle #4, the gate then
renders nothing). Single source of truth shared by the gate + the suppression
guard so both agree on the host.
*/ -}}
{{- define "agenity.gateHostname" -}}
{{- if .Values.httpRoute.hostnames -}}
{{- index .Values.httpRoute.hostnames 0 -}}
{{- else if .Values.sovereignFqdn -}}
{{- printf "agenity.%s" .Values.sovereignFqdn -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.mcpTenantHost — the Organization console host (console.<slug>.<pool>)
the openova MCP forwards as X-Tenant-Host on the org-scoped install path
(#4116). The catalyst-api base URL is the SOVEREIGN host (console.<sov-fqdn>)
which is NOT a registered tenant, so X-Tenant-Host MUST be the Org host or the
install 404s `tenant-not-registered` (#4610). Resolution:
  1. explicit openovaMCP.tenantHost wins;
  2. else derive from the agenity gate host (agenity.<slug>.<pool>): swap the
     leading `agenity` label for `console` → console.<slug>.<pool>;
  3. else empty (the MCP falls back to deriving from the api-URL host).
*/ -}}
{{- define "agenity.mcpTenantHost" -}}
{{- if .Values.openovaMCP.tenantHost -}}
{{- .Values.openovaMCP.tenantHost -}}
{{- else -}}
{{- $host := include "agenity.gateHostname" . -}}
{{- if $host -}}
{{- $rest := "" -}}
{{- if contains "." $host -}}
{{- $rest = (regexReplaceAll "^[^.]+\\." $host "") -}}
{{- printf "console.%s" $rest -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.gateEnabled — "true" only when the SSO gate is requested AND a hostname
resolves (no host → fail-closed, the gate would render nothing useful). Drives
BOTH templates/oidc-gate.yaml (render the gate) AND templates/httproute.yaml
(suppress the chart's own route so the gate owns the host).
*/ -}}
{{- define "agenity.gateEnabled" -}}
{{- if and .Values.oidcGate.enabled (include "agenity.gateHostname" .) -}}
true
{{- end -}}
{{- end -}}

{{- /*
agenity.gateClientId — the KC client id the gate registers. Explicit
oidcGate.clientId wins (the org-gitops emitter pins agenity-<slug>); else
derive agenity-<firstLabel> from the gate hostname (agenity.<slug>.<pool> →
agenity-<slug>); else "agenity-gate".
*/ -}}
{{- define "agenity.gateClientId" -}}
{{- if .Values.oidcGate.clientId -}}
{{- .Values.oidcGate.clientId -}}
{{- else -}}
{{- $host := include "agenity.gateHostname" . -}}
{{- $labels := splitList "." $host -}}
{{- if gt (len $labels) 1 -}}
{{- printf "agenity-%s" (index $labels 1) -}}
{{- else -}}
agenity-gate
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.catalystApiUrl — the catalyst-api base URL the tool server forwards to
and the tool-identity sidecar asks for its bearer at: openovaMCP.catalystApiUrl
when set, else https://console.<sovereignFqdn>. The console host routes /api/
to catalyst-api, both on a Sovereign host and on an Organization host.
*/ -}}
{{- define "agenity.catalystApiUrl" -}}
{{- if .Values.openovaMCP.catalystApiUrl -}}
{{- .Values.openovaMCP.catalystApiUrl -}}
{{- else -}}
{{- printf "https://console.%s" (.Values.sovereignFqdn | default "openova.io") -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.toolsDir — the in-memory folder the tool-identity sidecar writes the
tool bearer (bearer) and catalyst-api's signer key set (signers.jwks) into, by
rename. The workspace container mounts it read-only.
*/ -}}
{{- define "agenity.toolsDir" -}}/var/run/agenity-tools{{- end -}}

{{- /*
agenity.organization — the Organization the tool identity is asked for: the
value toolIdentity.organization, else the namespace of the release. An
Organization's namespace is named after its slug and carries the label
openova.io/organization; catalyst-api admits the workspace's account only for
the Organization of that label.
*/ -}}
{{- define "agenity.organization" -}}
{{- .Values.toolIdentity.organization | default .Release.Namespace -}}
{{- end -}}

{{- /*
agenity.actingPerson — the person the workspace's agent acts for: the value
toolIdentity.actingPerson when set, else the owner of the Organization, read
from spec.owners of the Organization object (orgs.openova.io/v1, named by the
slug), the same object catalyst-api reads the person's role from at every issue.
The one-year bearer this replaces was minted for that owner too. Empty when
neither gives a person: the token then carries no acting person, its tier stays
developer and create_application is not offered.
*/ -}}
{{- define "agenity.actingPerson" -}}
{{- if .Values.toolIdentity.actingPerson -}}
{{- .Values.toolIdentity.actingPerson | lower | trim -}}
{{- else if .Capabilities.APIVersions.Has "orgs.openova.io/v1/Organization" -}}
{{- include "agenity.ownerOf" (lookup "orgs.openova.io/v1" "Organization" "" (include "agenity.organization" .)) -}}
{{- end -}}
{{- end -}}

{{- /*
agenity.ownerOf — given an Organization object, the email of the first entry
of spec.owners with role owner, else the first with role admin, in lower case;
empty for no object or no such entry.
*/ -}}
{{- define "agenity.ownerOf" -}}
{{- $owners := list -}}
{{- if . -}}{{- $owners = dig "spec" "owners" (list) . -}}{{- end -}}
{{- $pick := "" -}}
{{- range $role := list "owner" "admin" -}}
{{- range $owners -}}
{{- if and (not $pick) (kindIs "map" .) (eq (lower (toString (.role | default ""))) $role) -}}
{{- $pick = toString (.email | default "") -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $pick | lower | trim -}}
{{- end -}}

{{- /*
agenity.toolIdentityRequest — the body the sidecar sends to
POST /api/v1/internal/agent-tokens (docs/contracts/model-contract.md section
2.2): the Organization, the agent and tier the default entry of
agentTokens.callers admits for the account agenity, the life, and the acting
person when there is one.
*/ -}}
{{- define "agenity.toolIdentityRequest" -}}
{{- $life := int .Values.toolIdentity.ttlSeconds -}}
{{- if or (lt $life 60) (gt $life 3600) -}}
{{- fail (printf "toolIdentity.ttlSeconds is %d: a tool identity lives 60 to 3600 seconds" $life) -}}
{{- end -}}
{{- $body := dict "organization" (include "agenity.organization" .) "agent" .Values.toolIdentity.agent "tier" .Values.toolIdentity.tier "ttlSeconds" $life -}}
{{- $person := include "agenity.actingPerson" . -}}
{{- if $person -}}{{- $_ := set $body "actingPerson" $person -}}{{- end -}}
{{- $body | toJson -}}
{{- end -}}

{{- /*
agenity.toolIdentityEnv — the environment of both tool-identity containers.
*/ -}}
{{- define "agenity.toolIdentityEnv" -}}
- name: AGENITY_API_URL
  value: {{ include "agenity.catalystApiUrl" . | quote }}
- name: AGENITY_TOOL_REQUEST
  value: {{ include "agenity.toolIdentityRequest" . | quote }}
- name: AGENITY_TOOL_LIFE
  value: {{ int .Values.toolIdentity.ttlSeconds | quote }}
- name: AGENITY_TOOLS_DIR
  value: {{ include "agenity.toolsDir" . | quote }}
- name: AGENITY_ACCOUNT_FILE
  value: /var/run/secrets/agenity/token
{{- end -}}

{{- /*
agenity.toolIdentityPass — the function `pass` of both tool-identity
containers. A pass reads catalyst-api's public signer key set (the tool server
verifies the bearer with it), then asks the token route for a bearer with the
projected token of the account, and replaces each file by rename, so a reader
sees the old file or the new one. A failed pass keeps the files it has and
returns 1. The bearer and the account token are never printed. The init
container runs one pass; the sidecar (statefulset.yaml) runs one every third of
the bearer's life, or 30 seconds after a failed pass.
*/ -}}
{{- define "agenity.toolIdentityPass" -}}
set -u
umask 077
d="$AGENITY_TOOLS_DIR"
renew=$((AGENITY_TOOL_LIFE / 3))
pass() {
  if curl -fsS --connect-timeout 5 --max-time 15 -o "$d/.signers.jwks" "$AGENITY_API_URL/api/v1/auth/signers.jwks"; then
    mv -f "$d/.signers.jwks" "$d/signers.jwks"
  else
    echo "[tool-identity] the signer key set could not be read; the tool server keeps the set it has"
  fi
  code=$(curl -sS --connect-timeout 5 --max-time 15 -o "$d/.answer" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $(cat "$AGENITY_ACCOUNT_FILE")" -H 'Content-Type: application/json' \
    --data "$AGENITY_TOOL_REQUEST" "$AGENITY_API_URL/api/v1/internal/agent-tokens") || code=000
  bearer=""
  if [ "$code" = 200 ]; then
    bearer=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$d/.answer")
  fi
  if [ -n "$bearer" ]; then
    printf '%s' "$bearer" > "$d/.bearer"
    mv -f "$d/.bearer" "$d/bearer"
    echo "[tool-identity] renewed the tool bearer, valid until $(sed -n 's/.*"expiresAt":"\([^"]*\)".*/\1/p' "$d/.answer"); next pass in ${renew}s"
    rm -f "$d/.answer"
    return 0
  fi
  echo "[tool-identity] the token route answered HTTP $code: $(head -c 300 "$d/.answer" 2>/dev/null)"
  rm -f "$d/.answer"
  return 1
}
{{- end -}}

{{- /*
agenity.mcpServer — the openova tool-server entry of CHEPHERD_EXTRA_MCP_JSON
and of the onboarding file, as JSON. The launcher reads the signer key set the
sidecar wrote into OPENOVA_MCP_RS256_PUBKEY_SET when the tool server starts;
the bearer reaches the tool server through the bearer-file setting of the
workspace container's environment, read at every call. Only knobs that are no
credential are written here.
*/ -}}
{{- define "agenity.mcpServer" -}}
{{- $env := dict "OPENOVA_MCP_CATALYST_API_URL" (include "agenity.catalystApiUrl" .) "OPENOVA_MCP_CONTEXT" .Values.openovaMCP.context "OPENOVA_MCP_VERIFY" .Values.openovaMCP.verify -}}
{{- $tenantHost := include "agenity.mcpTenantHost" . -}}
{{- if $tenantHost -}}{{- $_ := set $env "OPENOVA_MCP_TENANT_HOST" $tenantHost -}}{{- end -}}
{{- $launch := printf "f=%s/signers.jwks; if [ -s \"$f\" ]; then OPENOVA_MCP_RS256_PUBKEY_SET=$(cat \"$f\"); export OPENOVA_MCP_RS256_PUBKEY_SET; fi; exec %s" (include "agenity.toolsDir" .) .Values.openovaMCP.binaryPath -}}
{{- dict "command" "/bin/sh" "args" (list "-c" $launch) "env" $env | toJson -}}
{{- end -}}
