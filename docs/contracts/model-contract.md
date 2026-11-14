# Model contract

Status: published by work package AIP-060, with decision record 0030 (`docs/adr/0030-model-plane.md`).
Owner: area AIP, the agent plane. Only that area changes this file.

This file is the interface between every caller of a model inside a Sovereign and the model plane. It has four parts: the HTTP interface (section 1), the agent token (section 2), the identity headers between the gateway and the meter (section 3), and the usage event that the meter publishes (section 4). Each part names the work packages that build it. Until such a package has merged, its part is a specification only.

Conventions: in a table of fields, a field not listed is invalid. Times are RFC 3339 in UTC.

## 1. HTTP interface

### 1.1 Endpoint and routes

Base URL inside a Sovereign: `http://model-gateway.model-gateway.svc.cluster.local:8080/v1`. It is a ClusterIP Service only. It resolves in every region through a Cilium global Service, and inside every Organization virtual cluster through a replicated host Service (AIP-170).

| Method and path | Offered from | Purpose |
|---|---|---|
| `POST /v1/chat/completions` | AIP-140 | OpenAI Chat Completions. Every back end serves it. |
| `GET /v1/models` | AIP-140 | Lists the aliases as model objects. The field `owned_by` of each object is the back-end key of the alias. |

The Anthropic Messages route, `POST /v1/messages`, is not offered. No caller needs it. A package that brings its first caller adds it, together with a gate check of its own.

Request header: `Authorization: Bearer <credential>`. The credential is an agent token (section 2), or a virtual key once AIP-270 has built them. The gateway removes every request header whose name starts with `x-openova-`.

Back-end keys: `simulation` (Axon), `selfhosted` (a self-hosted serving engine), `stub` (a scripted back end for gate checks). AIP-320 adds `upstream`, the model plane of a parent Sovereign. An alias whose back end is not `simulation` or `upstream` keeps the request inside the Sovereign.

### 1.2 Aliases

| Alias | Meant for | Simulation phase | GPU phase |
|---|---|---|---|
| `agent-default` | Agents that decide, and tool use | back end `simulation`, upstream model `sim-default` | back end `selfhosted`, the served model name |
| `agent-fast` | Short reads and summaries | back end `simulation`, upstream model `sim-fast` | back end `selfhosted`, the same or a smaller model |
| `conformance-echo` | Gate checks only | back end `stub` | back end `stub` |

A request whose `model` is not an alias gets HTTP 404 with the error code `model_not_found`. A new alias is added by a work package that names its first reader. A change of back end is a change of the alias mapping in Git; it never changes a caller.

### 1.3 Request fields of `POST /v1/chat/completions`

"Native" means that the back end honours the field as the OpenAI specification says.

| Field | Simulation back end | Self-hosted back end |
|---|---|---|
| `model` | an alias, required | the same |
| `messages` with the roles `system`, `user`, `assistant` (with `tool_calls`) and `tool` | native for the roles; `system` messages are joined with a blank line and used as the system prompt | native |
| `messages[].content` as text parts | native | native |
| `messages[].content` as image parts (`image_url` with a `data:` URL) | from AIP-107; before that HTTP 400 `unsupported_content` | native if the model has vision |
| `tools` (type `function`) | native: returned as `tool_calls`, never run by the back end. A tool name longer than 56 characters gets HTTP 400 `tool_name_too_long` | native |
| `tool_choice` `auto` or `none` | native | native |
| `tool_choice` `required` or a named function | emulated by an instruction, not guaranteed; response header `x-openova-emulated: tool_choice` | native |
| `parallel_tool_calls: false` | enforced: only the first call is returned; the others are answered inside the back end as not run | native |
| `stream`, `stream_options.include_usage` | native: real deltas, including deltas of tool-call arguments | native |
| `response_format` `json_schema` | from AIP-107 through structured output; before that emulated by an instruction | native |
| `reasoning_effort` | mapped to an effort level `low`, `medium` or `high` | native if supported |
| `max_tokens`, `max_completion_tokens` | accepted, not enforced | native |
| `stop` | applied by cutting the output | native |
| `temperature`, `top_p`, `seed`, `presence_penalty`, `frequency_penalty`, `logit_bias`, `logprobs`, `top_logprobs` | accepted and ignored | native |
| `n` greater than 1 | HTTP 400 `unsupported_parameter` | native |
| `user`, `metadata`, `store` | ignored | ignored |

The fields `conversation_id`, `thinking`, `effort` and `profile`, which Axon accepts today, are not part of this contract.

### 1.4 Response

The response is the OpenAI `chat.completion` object, or a `chat.completion.chunk` stream. Every back end behaves as follows.

- Each entry of `choices[0].message.tool_calls[]` has a unique `id`, `type: function`, a `function.name` equal to the caller's tool name, and `function.arguments`, a JSON object encoded as a string. `finish_reason` is `tool_calls` when that list is not empty, and otherwise `stop`, `length` or `content_filter`.
- `usage.prompt_tokens`, `usage.completion_tokens` and `usage.total_tokens` are not zero for an answered request. `usage.prompt_tokens_details.cached_tokens` is present, and 0 when it is not known. The simulation back end also sets `usage.prompt_tokens_details.cache_write_tokens`.
- A stream sends, in this order: a first chunk with `delta.role`; content deltas as they are generated, and tool-call deltas with `index`; a chunk with `finish_reason`; a usage chunk with empty `choices`; then `data: [DONE]`. The meter removes the usage chunk when the caller did not ask for it.
- The response header `x-request-id` equals the identifier of the request in the usage event (section 4). The gateway sets it when the caller did not.

### 1.5 Errors

The body of an error is `{"error": {"message": "<text>", "type": "<type>", "code": "<code>"}}`.

| Status | `code` | When | What the caller does |
|---|---|---|---|
| 400 | `invalid_request`, `unsupported_parameter`, `unsupported_content`, `tool_name_too_long`, `missing_tool_results` | The request is wrong. The last code: a follow-up request has no result for a tool call that was returned. | Fix the request. |
| 401 | `invalid_token` | No token, a bad signature, an unknown key identifier, a wrong issuer, or the token expired. | Obtain a new token. |
| 403 | `organization_stopped` | The Organization is suspended or stopped for overage. The meter answers this, from AIP-275. | Stop. |
| 404 | `model_not_found` | `model` is not an alias. | Fix the request. |
| 429 | `rate_limit_exceeded` | The back end is at capacity, or the upstream limit was hit. Header `Retry-After: <seconds>`. | Wait, then retry. |
| 502 | `backend_error` | The back end failed. | Retry once. |
| 503 | `simulation_credential_invalid` | The simulation credential is absent, expired beyond refresh, or rejected. | Do not retry. A sovereign-admin must act. |
| 504 | `backend_timeout` | No first byte came within the first-byte time-out of the alias, a value of the gateway chart (AIP-140). | Retry once. |

## 2. Agent token

An agent token is a JWT signed with RS256 by the catalyst-api signer of the Sovereign, the signer that already mints console sessions (`products/catalyst/bootstrap/api/internal/handoverjwt/signer.go`). It differs from a session in three ways: its header carries a key identifier, it carries an audience claim, and it carries `typ: agent`. Sessions do not change. AIP-120 builds the issue route, the key set route and the verification at catalyst-api.

### 2.1 Format

Header: `{"alg": "RS256", "typ": "JWT", "kid": "<key identifier>"}`. The key identifier is the RFC 7638 thumbprint of the signer's public key: SHA-256, base64url without padding.

| Claim | Type | Value |
|---|---|---|
| `iss` | string | The session issuer of the Sovereign: the value that `pinIssuer()` in `products/catalyst/bootstrap/api/internal/handler/auth.go` returns. |
| `aud` | list of strings | `["model-gateway", "platform-tools", "catalyst-api"]`. `platform-tools` is the one audience of every platform tool server. |
| `sub` | string | `agent:<organization>/<agent>` |
| `typ` | string | `agent` |
| `org`, `org_id` | string | The slug of the Organization. |
| `agent` | string | The agent name, pattern `^[a-z][a-z0-9-]{1,62}$`. |
| `tier` | string | `viewer` or `developer`. Never higher. |
| `realm_access` | object | `{"roles": ["catalyst-<tier>"]}` |
| `role` | string | `openova-user`, the value that sessions carry. |
| `email` | string | `<agent>@<organization>.agents.invalid`. It is synthetic and not routable. It exists because handlers and the tool server read `email` as the actor. |
| `email_verified` | boolean | `false` |
| `caller` | string | The TokenReview user name of the workload that asked for the token: `system:serviceaccount:<namespace>:<name>`. |
| `iat`, `exp` | number | The life of a token is at most 3,600 seconds; the default is 1,800. |
| `jti` | string | A UUID. |

Every claim is required. There is no claim for a person on whose behalf an agent acts: no caller acts for a signed-in person, so none is built.

### 2.2 Issue route

`POST /api/v1/internal/agent-tokens` on catalyst-api, outside the session group, beside the other internal routes. Its entry in the mutating-route table (contract C9) has the write class `none`.

- Caller authentication: the mechanism that the internal routes use today, unchanged. The caller sends `Authorization: Bearer <projected ServiceAccount token>`, and a TokenReview resolves it (`products/catalyst/bootstrap/api/internal/handler/internal_auth.go`).
- Caller authorisation: the environment variable `CATALYST_AGENT_TOKEN_CALLERS`, a JSON list. Each entry is `{"caller": "<TokenReview user name>", "organizations": ["*"] or a list of slugs, "maxTier": "viewer" or "developer", "agents": ["*"] or a list of names}`. All four keys are required. The chart value is `agentTokens.callers`. Its default is exactly one entry, `{"caller": "system:serviceaccount:catalyst-system:catalyst-conformance", "organizations": ["conformance"], "maxTier": "viewer", "agents": ["conformance-probe"]}`, so that gate checks run on a fresh Sovereign. No other workload may mint a token until a commit of values adds its entry. A value that cannot be parsed makes the route answer 503 `agent-tokens-misconfigured`. It never falls back to allowing.
- Request body: `{"organization": "<slug>", "agent": "<name>", "tier": "viewer" or "developer", "ttlSeconds": <integer 60 to 3600>}`. `ttlSeconds` is optional; its default is 1,800. An unknown field is refused.
- Response 200: `{"token": "<jwt>", "expiresAt": "<RFC 3339 time>"}`.
- Errors, with the body `{"error": "<code>", "detail": "<text>"}` that the internal routes use today: 401 `missing-bearer`; 502 `token-review-failed`; 403 `unauthorized-sa` (the caller is in no entry); 403 `caller-not-allowed` (the Organization, the agent or the tier is outside the caller's entry); 422 `invalid-request`; 503 `signer-unavailable`; 503 `agent-tokens-misconfigured`.
- Every issue writes one audit event through the audit package (`products/catalyst/bootstrap/api/internal/audit/audit.go`) with the action `agent-token.issue` and the fields `caller`, `organization`, `agent`, `tier` and `jti`. The token itself is never logged.

Example:

```
POST /api/v1/internal/agent-tokens
Authorization: Bearer <ServiceAccount token of ops-records/ops-records>
{"organization": "acme", "agent": "correlator", "tier": "viewer", "ttlSeconds": 1800}

200 {"token": "eyJhbGciOiJSUzI1NiIsImtpZCI6Ik5Vd0…", "expiresAt": "2026-12-01T09:30:00Z"}
```

### 2.3 Key set route

`GET /api/v1/auth/signers.jwks` on catalyst-api, outside the session group, with no authentication. Response 200, `application/json`, `Cache-Control: max-age=300`:

```json
{"keys": [{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "<key identifier>", "n": "<modulus>", "e": "AQAB"}]}
```

It has one entry: the public key of the signer of this pod. The route returns public material only, so it may be reachable through the console address. The Secret key `signers.jwks` and its readers do not change.

### 2.4 Verification

At catalyst-api (AIP-120):

1. The claims type `Claims` in `products/catalyst/bootstrap/api/internal/auth/session.go` gains three fields: `Typ string` (claim `typ`), `Agent string` (claim `agent`) and `Audience []string` (claim `aud`, which may be a string or a list).
2. The validator uses the local signer key when the token has no key identifier (the rule today) or when its key identifier equals the local one. No other branch changes.
3. `claimsAreOrgScoped` in `products/catalyst/bootstrap/api/internal/handler/org_scope.go` also returns true when `Typ` is `agent`. From then on, the Organization scope guard and `orgScopeForRequest` confine an agent token to the Organization of its `org_id` claim, exactly as they confine an Organization session: denied by default outside the list of Organization-safe routes.
4. A new middleware, `agenttoken.Restrict`, runs after the Organization scope guard. For a token with `Typ` `agent`: if `Audience` does not contain `catalyst-api`, the answer is 401 `agent-token-wrong-audience`. `GET`, `HEAD` and `OPTIONS` pass. Any other method passes only when an entry of the table `agenttoken.MutationsAllowed` matches the method, the path pattern and the minimum tier; otherwise the answer is 403 `agent-token-forbidden`. The table is empty when AIP-120 merges. AIP-245 adds its one entry. For any other token the middleware does nothing.

At the tool server (AIP-150): the resolver accepts `typ: agent` only when `aud` contains `platform-tools`. The tier is the `tier` claim, capped at developer. The identity gains the field `Agent string`. `create_application` needs tier admin, so no agent token can call it.

At the gateway (AIP-140): the signature against the key set route, `iss` equal to the issuer of the Sovereign, the expiry, and `aud` containing `model-gateway`.

When an Organization is stopped, the meter refuses its requests (AIP-275). The issue route does not check the state of the Organization; a token is refused at the model plane within one request.

## 3. Identity headers between the gateway and the meter

The gateway sets these headers from the verified credential, after it has removed any header of the same name that the caller sent. The meter trusts them because only gateway pods may open connections to the meter (a network policy of the gateway chart, AIP-140).

| Header | Value for an agent token | Value for a virtual key (AIP-270) |
|---|---|---|
| `x-openova-organization` | the claim `org_id` | the Organization the key was registered for |
| `x-openova-principal-kind` | `agent` | `application` |
| `x-openova-principal` | the claim `agent` | the name of the key |
| `x-openova-alias` | the `model` that the caller sent | the same |
| `x-request-id` | the caller's value if it matches `^[A-Za-z0-9._-]{8,64}$`, otherwise a new ULID | the same |

Every header is required. Readers: the usage event reads the Organization and the alias; the counters and the log line of the meter read the principal kind and the principal; gate checks read `x-request-id`. A header with no reader is not added.

## 4. Usage event

The billing engine consumes exactly one usage event inside a Sovereign, `catalyst.usage.recorded.v1` (decision record 0028, decisions B8 and B9). The agent plane produces it and defines its fields. This section is the producer's side. The meter, `model-meter` (epic AIP-110), publishes it.

- Subject `catalyst.usage.recorded.v1` on the stream `CATALYST_ORG`.
- Envelope: CloudEvents 1.0 in structured JSON mode, with exactly eight attributes: `specversion` (`1.0`), `id`, `type`, `source`, `subject`, `time`, `datacontenttype` (`application/json`) and `data`. `type` is `io.openova.catalyst.usage.recorded.v1`. `source` is `model-gateway/<sovereign-fqdn>/<region-id>`. `subject` is the Organization slug. `id` is a ULID made once per request and kept across retries; it is sent as the JetStream message identifier (`Nats-Msg-Id`), and it is also the request identifier when the gateway made one.
- Consumer: the billing engine, durable `chargeback-usage` (BSS-131). It removes duplicates by `source` and `id`. Delivery is at least once.
- Registry: the subject has one entry in `docs/contracts/C3-event-subjects.yaml`, owned by area BSS, envelope `cloudevents`, delivery `at-least-once`, payload the Go type of the event in `core/services/model-meter`. AIP-111 adds the entry when it is absent, with the producer `model-meter`; the registry names a service by its folder under `core/services`. When the entry exists, AIP-111 adds `model-meter` to its producers if it is missing.

The `data` block. Every field is required.

```yaml
organization: acme               # string, pattern ^[a-z][a-z0-9-]{2,31}$. From x-openova-organization. Equals the envelope subject.
resource:
  kind: llm-model                # constant for this producer
  id: agent-default              # string. The alias that the caller asked for (x-openova-alias).
occurredAt: 2026-12-01T09:00:06Z # the time the response ended
measures:                        # list of 1 to 5 entries, each {meter, quantity}; quantity is an integer of 0 or more
  - {meter: llm.tokens.input, quantity: 304}
  - {meter: llm.tokens.cache_read, quantity: 1536}
  - {meter: llm.tokens.output, quantity: 212}
  - {meter: llm.requests, quantity: 1}
```

How the measures are computed from the usage block of the back end:

| Measure | Value |
|---|---|
| `llm.tokens.input` | `prompt_tokens` minus `prompt_tokens_details.cached_tokens` minus `prompt_tokens_details.cache_write_tokens`; never below 0 |
| `llm.tokens.cache_read` | `prompt_tokens_details.cached_tokens`, 0 when absent |
| `llm.tokens.cache_write` | `prompt_tokens_details.cache_write_tokens`, 0 when absent |
| `llm.tokens.output` | `completion_tokens`; reasoning tokens are part of it |
| `llm.requests` | 1 |

A token measure whose quantity is 0 is left out. `llm.requests` is always present. The event carries no price, no amount, no user and no agent name.

One whole message body:

```json
{"specversion": "1.0", "id": "01JCF0R1K2M3N4P5Q6R7S8T9V0",
 "type": "io.openova.catalyst.usage.recorded.v1",
 "source": "model-gateway/sovereign.example.org/region-a", "subject": "acme",
 "time": "2026-12-01T09:00:06Z", "datacontenttype": "application/json",
 "data": {"organization": "acme", "resource": {"kind": "llm-model", "id": "agent-default"},
          "occurredAt": "2026-12-01T09:00:06Z",
          "measures": [{"meter": "llm.tokens.input", "quantity": 304},
                       {"meter": "llm.tokens.cache_read", "quantity": 1536},
                       {"meter": "llm.tokens.output", "quantity": 212},
                       {"meter": "llm.requests", "quantity": 1}]}}
```

The meter publishes at most one event per request: exactly one when the back end answered with a 2xx status and a usage block was read, streamed or not; none for an error, a refusal, or a stream that the caller abandoned before its usage chunk.

The subject `catalyst.usage.recorded`, without a version, is a different subject. It loses its only producer in AIP-111. Its consumer and its registry entry belong to area BSS.

Simulated usage is not flagged in the event. It is shown and not charged because no price item exists for a model alias in a shipped offer, price book or seed (decision record 0028, decision B9).
