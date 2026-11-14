# ADR-0030 — Model plane: one gateway, one model contract, a simulation back end, agent tokens and metered usage

Status: Accepted
Date: 2026-11-13
Supersedes: ADR-0003

Written by work package AIP-060. It records how a caller inside a Sovereign reaches a model, how the caller proves who it is, how usage reaches the billing engine, how agents run, and what is retired. The interface that callers build against is published beside it: `docs/contracts/model-contract.md`. It replaces ADR-0003 as a whole (section "What happens to ADR-0003").

Terms used here:

- **Model plane**: the gateway, the meter and the back ends, together.
- **Alias**: a logical model name that a caller sends, such as `agent-default`. The gateway maps it to a back end.
- **Back end**: what answers a model request. Today the simulation back end, Axon. Later a self-hosted serving engine on GPUs.
- **Agent token**: a short-lived token that catalyst-api mints for one agent of one Organization.
- **Agent workspace**: Agenity, the per-Organization workspace of Blueprint `bp-agenity` (`products/agenity`).
- **Tool server**: `bp-openova-mcp` (`products/openova-mcp`), the platform's server of tools for agents and people.

## Context

Measured on `main` on 2026-11-13.

- Four components stand between a caller and a model, and none of them carries a model request on a Sovereign: Axon (`products/axon`), `bp-newapi` (`platform/newapi`), `bp-llm-gateway` (`platform/llm-gateway`) and `bp-anthropic-adapter` (`platform/anthropic-adapter`). The chart of `bp-llm-gateway` has no Deployment. The image of `bp-anthropic-adapter` is built by no source and no workflow of this repository.
- `bp-newapi` is bootstrap-kit slot 80 (`clusters/_template/bootstrap-kit/80-newapi.yaml`), so every Sovereign installs it. Its model channels are off by default (`platform/newapi/chart/values.yaml`). The signup funnel also lists it, beside `bp-openclaw` and `bp-agenity`, among the Applications that it installs into a new Organization (`products/catalyst/bootstrap/api/internal/handler/organization_funnel_apps.go`). Its channels, users and keys live in its own database, not in Git. ADR-0003 gives each user a gateway user and a key of its own, delivered as a Kubernetes Secret.
- Axon has no `blueprint.yaml` and no kit slot. Its chart pins the image tag `latest` and deploys a Valkey store and an Ingress (`products/axon/chart/values.yaml`). Its Claude path writes the caller's tool definitions into the prompt text (`products/axon/src/providers/claude.ts`). Its route never returns `tool_calls` and reports token usage as 0 (`products/axon/src/routes/chat-completions.ts`). No agent framework can use it as a model.
- The metering sidecar (`core/services/metering-sidecar`) sits in front of `bp-newapi`. It meters only responses of content type JSON, so a streamed call is not metered. It multiplies tokens by one flat price and publishes money on the subject `catalyst.usage.recorded` (ADR-0028, Context).
- The agent workspace calls the model provider directly, with no gateway and no metering. Its model credential is a Secret that an ExternalSecret fills from the Sovereign's OpenBao (`products/agenity/chart/templates/externalsecret-anthropic.yaml`). Its tool calls run on one bearer per Organization that catalyst-api seeds with tier `org-admin` and a life of one year (`products/catalyst/bootstrap/api/internal/handler/sovereign_mcp_bearer_seed.go`). catalyst-api also seeds the model login (`products/catalyst/bootstrap/api/internal/handler/sovereign_anthropic_seed.go`) and refreshes it (`products/catalyst/bootstrap/api/internal/handler/sovereign_anthropic_refresh.go`).
- The tool server has seven tools: six that read and one that writes, `create_application`, which needs tier admin (`products/openova-mcp/internal/tools/catalogue.go`).
- The signer that mints console sessions puts no key identifier in its tokens (`products/catalyst/bootstrap/api/internal/handoverjwt/signer.go`). Its key set is kept in a Secret under the key `signers.jwks`, and no HTTP route serves it.
- catalyst-api already authenticates an in-cluster caller that has no session: the caller sends its projected ServiceAccount token, a TokenReview resolves it, and an allow-list admits it (`products/catalyst/bootstrap/api/internal/handler/internal_auth.go`).
- No chart, Blueprint or kit slot of an agent runtime exists.

## Decision

**M1 One gateway, named.** The model gateway is agentgateway, installed as Blueprint `bp-model-gateway` in kit slot 81 (AIP-140). Spike AIP-010 confirms the choice with eight binary tests. If one test fails, the fallback is that the meter itself verifies the token and maps aliases, until an upstream gateway passes the same tests. `bp-newapi` is retired in both cases (epic AIP-180).

**M2 Placement.** One model plane serves a Sovereign. It runs in the host cluster of the primary region. Other regions reach it through a Cilium global Service. Organization virtual clusters reach it through a replicated host Service (AIP-170). The signup funnel no longer installs a gateway into an Organization (AIP-181).

**M3 One model contract.** Every caller uses one base URL, the aliases `agent-default` and `agent-fast`, and the OpenAI Chat Completions wire format. The alias `conformance-echo` serves gate checks only. The Anthropic Messages route is not offered, because no caller needs it. A change of back end never changes a caller. The contract is `docs/contracts/model-contract.md`.

**M4 The credential at the gateway is an agent token.** A caller authenticates with an agent token: a JWT that lives at most 60 minutes, signed by the Sovereign's own signer and carrying a key identifier (AIP-120). The gateway takes the Organization from the verified token only. No credential of the model plane is a Kubernetes Secret object. The gateway does not accept console sessions. Virtual keys, for an Organization Application that cannot obtain an agent token, are built by AIP-270 for their first consumer.

**M5 Axon is the simulation back end.** Until GPUs arrive, Axon is the back end of the aliases (epic AIP-100). It is rebuilt so that a caller's tools come back as tool calls and never run inside Axon; tool results are accepted in later turns; streaming is real; and usage numbers are real. Each login has exactly one Axon, and only Axon refreshes that credential (AIP-105). It reads the credential from OpenBao through a mount, never from a Secret object, and writes each refreshed version back (AIP-106, AIP-160). It becomes Blueprint `bp-axon` in kit slot 82 (AIP-106).

**M6 An agent is not a model.** The agent workspace is never placed behind a model interface, and it is not placed behind the gateway. It stays the per-Organization workspace beside the agent runtime of M7, on each Organization's own sign-in. Its own model path and its tool path stay. No credential supplied by one party serves another party: each Organization brings its own sign-in or key. AIP-155 replaces the credential seeded from the Sovereign with the Organization's own sign-in, delivered by a mount, and replaces the one-year bearer with a short-lived tool identity, as spike AIP-250 records. AIP-156 then deletes the bearer seed.

**M7 The agent runtime is kagent.** kagent 1.x is the runtime for declared agents, packaged as Blueprint `bp-kagent` with one installation per Organization and pinned to a patch release at least 30 days old (epic AIP-200). It merges only after spike AIP-030 has passed on k3s, Cilium and a virtual cluster.

**M8 An agent is a principal.** The platform service that starts an agent turn asks catalyst-api for an agent token through the internal-route mechanism of the Context, and passes it as the bearer. kagent forwards it to the gateway and to the tool servers. catalyst-api confines an agent token to its Organization with the existing Organization scope guard, and to read methods unless a table entry allows a write (AIP-120). An agent changes desired state only by proposing a pull request (AIP-245). Agents hold no cluster write credential. No token carries a person on whose behalf the agent acts.

**M9 Tool servers.** `bp-openova-mcp` stays the platform tool server. Agents see its six read tools. Its one write tool stays reserved to people: an agent token is capped at tier developer (AIP-150). One tool is added for agents, `propose_change`, with the fixer agent that is its first reader (AIP-245). Every platform tool server accepts the one audience `platform-tools`. Each later plane owns its own tool server under the convention that AIP-150 writes. The tool catalogue of the withdrawn Sandbox is not ported (ADR-0023).

**M10 Metering.** The metering sidecar becomes `model-meter` (`core/services/model-meter`), placed between the gateway and every back end (epic AIP-110). For every answered request, streamed or not, it publishes the one usage event of ADR-0028, `catalyst.usage.recorded.v1`: quantities per token class, no price. The Organization in it is the one the gateway asserted. The meter refuses a stopped Organization (AIP-275). The security fix AIP-040 comes first; its text is published with its ticket.

**M11 Operational records and bounded judges.** A small Go service with PostgreSQL, `ops-records`, delivered as Blueprint `bp-ops-records`, holds signals, incidents, problems and judgements (epic AIP-220). It drives the service-management flow in code. It calls agents as bounded judges: it offers a closed set of answers, validates the answer, and decides by rule when the agent is late. Its tables are domain objects of its Blueprint, not platform kinds. The caller entry it ships names one Organization, never all (AIP-226). The correlator, the investigator and the scribe ship first (epic AIP-240).

**M12 Proof, guardrails, cost.** Each exemplar agent ships an evaluation set. It is `supported` only while that set passes on the current alias mapping (check AIP-G11). Guardrails are a small library inside `ops-records` and inside the tool server (AIP-224). The cost of an agent is read from the meter's token counters, labelled by principal. The bill comes from the usage event.

**M13 Retirements.** Deleted: `bp-llm-gateway` and `bp-anthropic-adapter` (AIP-070); `bp-newapi` with its bridge sidecar, and `bp-openclaw` (epic AIP-180); the gateway client and the per-user key issuing in catalyst-api (AIP-182); the seeding and refreshing of the model credential in catalyst-api (AIP-190); Axon's vLLM provider, conversation store and Valkey (AIP-101). Axon itself stays until the switch-over of M14.

**M14 Switch-over.** When GPUs arrive, the alias mapping in Git changes from back end `simulation` to back end `selfhosted` (epic AIP-290). The evaluation sets run again. Then Axon is deleted (AIP-293). No caller changes.

## Alternatives rejected

**M1.** (a) Keep `bp-newapi`. Its state (channels, model names, users, keys) lives in its own database behind its own screens, not in Git. It keeps a credit ledger of its own beside the one billing product of ADR-0028. It has no per-request usage event, so a meter must be built in either case. (b) A gateway that keeps its state in a database: the same objection. (c) Write a gateway of our own from the start: one more gateway. The fallback of M1 is smaller: it adds token verification and an alias table to a proxy that the platform already owns, and only if the spike fails.

**M2.** (a) One gateway per Organization, as the funnel installs today: every Organization would hold a route to the model credential, and metering would have as many producers as Organizations. (b) One gateway per region: the simulation back end can exist only where the single login lives, so a second region would hold a gateway with nothing behind it.

**M3.** (a) Pass a provider's own OpenAI-compatible endpoint through: it does not serve a subscription login. (b) Offer only the Anthropic wire format: self-hosted serving engines and the agent runtime's gateway wiring use the OpenAI format.

**M4.** (a) A model key stored in a Secret object in each Organization: an Application credential never becomes a Secret object. (b) A token issuer per runtime installation: the gateway would have to trust one issuer per Organization. (c) One static key per Organization: it lives long, and it cannot tell one agent from another. (d) Accept console sessions at the gateway too: sessions carry no key identifier, the gateway selects its verification key by that identifier, and no caller needs it.

**M5.** (a) Leave Axon as it is: no agent framework can use it. (b) Replay every request as a text transcript only: simple and stateless, but the model never sees a real result of its own tool call. It stays as the fallback path inside Axon. (c) Rewrite Axon in Go: the agent kit that drives the login is a TypeScript library.

**M6.** Put the agent workspace behind an OpenAI-compatible wrapper as the model of the agent runtime. An agent runs its own tool loop with its own tools and system prompt. A caller that sends tool schemas and waits for `tool_calls` would get prose back, and two loops would compete. That is the defect Axon has today, with more moving parts.

**M7.** (a) One kagent per Sovereign with a namespace per Organization: its open-source authorizer allows every call, so namespaces would not separate callers. (b) The agent workspace as the only runtime: no declared agents, no approval gate per tool, one process per Organization.

**M8.** (a) Keep the shared one-year bearer: every tool call would run as Organization administrator in the owner's name. (b) Exchange a token at Keycloak for every turn: correct in general, but the tool server and catalyst-api already verify the signer's tokens, so a second issuer would add a system without removing one. (c) A new authentication mechanism for the issue route: catalyst-api already authenticates in-cluster callers by TokenReview. (d) Approval of each tool call inside the runtime as the human gate for a fix: it would add a second approval beside the merge of the pull request, with no screen to give it on. The merge is the gate.

**M10.** (a) Bill from gateway metrics or access logs: they are scraped or best-effort, and a gateway's own cost figure is an estimate. (b) Put the meter in front of the gateway: it would have to authenticate callers a second time and parse two wire formats. (c) A model-specific usage subject with its own payload: the billing engine already consumes one usage event for every meter; a second subject would be a second ingestion contract.

**M11.** (a) Agents drive the flow by calling each other: then no code holds the deadline or the state of an incident, and a late answer cannot be told from a repeated one. (b) A general workflow engine: a new dependency before the foundation is proven. (c) Incidents as custom resources in Git: they are many and operational, and business data belongs in a database. (d) A new platform kind for incidents: the entity model is fixed; these are domain objects of a service that arrives as a Blueprint.

**M13.** Rename the bridge sidecar of `bp-newapi`: it has no caller left once that chart goes, so it is deleted with the chart.

## What happens to ADR-0003

ADR-0003 is the contract of a hook that, for each new user, creates a user and a key in `bp-newapi` and delivers the key as the Secret `newapi-key-{user-uuid}`, which the per-user pod of `bp-openclaw` reads. ADR-0030 withdraws every part that the hook exists for: `bp-newapi` and `bp-openclaw` are retired (M1, M13), no per-user key is issued (M13), and no credential of the model plane is a Secret object (M4). The hook therefore has no work left, and ADR-0003 is superseded as a whole. Its body is not edited; its header names ADR-0030.

## Consequences

- One address, one wire format and one kind of credential serve every caller of a model. A change of back end is one commit to the alias mapping.
- Every answered model request yields one usage event with the verified Organization, and no price. The billing engine rates it (ADR-0028).
- Agents read through the tool server and catalyst-api within their Organization. They change desired state only through a pull request that a person merges.
- The agent workspace keeps working as it does today until AIP-155 replaces its two credentials. Its pillar-4 role does not change.
- Simulated usage is visible and not charged: no price item exists for a model alias in a shipped offer (ADR-0028, decision B9).
- Four components are retired, and the code in catalyst-api that issues per-user keys and seeds and refreshes the model login is deleted.

## Gate checks

| Check | What it proves | Delivered by |
|---|---|---|
| AIP-G02 | A tool call makes a round trip through the gateway. | AIP-140 |
| AIP-G03 | A streamed call is metered. | AIP-140 |
| AIP-G04 | The usage event carries the verified Organization. | AIP-140 |
| AIP-G06 | The simulation back end is installed, truthful and fenced. | AIP-106 |
| AIP-G07 | Agent tokens are minted, confined and verified. | AIP-120 |
| AIP-G08 | An agent calls a platform tool. | AIP-203 |
| AIP-G09 | A usage record lands in the billing engine with its Organization. | AIP-285 |
| AIP-G13 | The simulation credential route works. | AIP-160 |
