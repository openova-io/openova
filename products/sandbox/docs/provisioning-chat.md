# Sandbox — conversational provisioning

## The pitch

The current `catalyst-ui` wizard is a good tool for power users and integrators. It presents region, cluster size, blueprint catalogue, identity mode, DNS choice, and 15+ further fields on first contact. That works for people who already know what those words mean.

It does **not** work for the customer we lose most often: a non-technical founder who wants a Sovereign for their business and would describe their need in two sentences if we let them.

**Conversational provisioning** is the same Sandbox shell — text *and* voice — scoped to a narrower MCP toolbox, available at `console.openova.io/start` *before* the user has a Sovereign. The agent asks what they need, calls catalyst-api with the right body, watches the provisioning SSE stream, and hands them off into their new Sovereign's Sandbox when it is ready.

This is the first OpenOva surface where AI-first replaces forms. It is also the demonstration moment: the prospect's first impression of OpenOva is "this platform is alive."

## Wizard vs chat — when each wins

|  | Wizard (`catalyst-ui`) | Conversational (Sandbox shell) |
|---|---|---|
| Power user who already knows region SKUs | Best | Slower |
| Integrator using `POST /api/v1/deployments` directly | Best (API stays canonical) | Not relevant |
| Repeat ops (provision 5 environments today) | Best | Slower |
| Non-technical founder, first contact | Friction | Best |
| Voice-only / accessibility need | Not available | Best |
| Mobile-first prospect | Fields-on-glass is painful | Native |
| Demonstrating AI capability on the homepage | Static | The whole point |

Both paths terminate at the **same** catalyst-api endpoint. The chat is not a parallel system — it is a different *interaction* against the same backend. Anything the wizard can do, the chat can do, because the chat reads the same OpenAPI surface.

## End-user journey — text-first

The user lands on `console.openova.io/start`. They are not authenticated yet beyond an email gate (Magic link or OIDC social login).

```
┌─ OpenOva — Start your Sovereign ─────────────────────────────┐
│                                                              │
│  Hi. I am the OpenOva concierge. I can stand up a Sovereign  │
│  cloud for you — privately hosted, with your own apps,       │
│  domain, and team. Tell me what you are building, and I'll   │
│  ask anything I need to know.                                │
│                                                              │
│  [ Mic v ]   [ Voice on / off ]                              │
│  > _                                                          │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

The user types:

```
> I run a small OB-GYN clinic in Muscat. I need patient records,
  scheduling, billing, and a way for patients to book online.
  We are 6 staff, growing.
```

The agent reads the catalyst-api OpenAPI spec via MCP, reads the marketplace catalogue (`core/marketplace-api/handlers`), reads compliance hints from a small skill pack:

```
> v catalyst.openapi                     loaded
> v marketplace.catalog.list             4 healthcare apps known
> v skills.healthcare-compliance         loaded

   I can help with that. A few quick things so I size your cloud
   right:

   1. Region — I suggest Frankfurt (Germany) for low latency to
      Oman. We can also do Helsinki or Bahrain. Which?
   2. Patient data residency — Germany keeps you under GDPR; some
      Oman ministries require local. Any preference?
   3. Domain — do you have one (e.g. clinicname.om), or shall I
      give you a free subdomain like clinicname.openova.io to
      start, and we move to your domain later?
   4. Apps — I will install:
        - OpenClaw (patient records, scheduling, billing — HIPAA-grade)
        - Stalwart (secure email for the clinic)
        - WordPress (your public booking page)
      OK to proceed, or strike any?

> _
```

User answers in one message (`Frankfurt fine, GDPR is fine, no domain yet, all three apps`). The agent calls catalyst-api:

```
> v POST /api/v1/deployments
      provider          hetzner
      region            fsn1
      tier              sme
      domain            clinicname.openova.io        (subdomain)
      apps              [openclaw, stalwart, wordpress]
      orgSlug           clinicname
      ownerEmail        <signed-in email>
                                                     queued (id: dep-2841)
```

The agent then subscribes to the existing provisioning SSE feed (`/api/v1/deployments/{id}/events` — `products/catalyst/bootstrap/api/internal/handler/deployments.go:1186`) and renders progress as cards in the chat:

```
> v Provisioning           dep-2841            queued -> running
> v Hetzner project        created             nbg1-rtz                  OK
> v Network                 10.0.0.0/16        created                    OK
> v Control plane           3 x cpx42          booting...                 (180s)
> v k3s installed           apiserver healthy                              OK
> v cilium                  default-deny CCNP applied                      OK
> v cert-manager            Let's Encrypt staging issuer ready             OK
> v Catalyst-Zero handover  HR Ready                                       OK
> v Keycloak realm          sovereign  created                             OK
> v Gitea                   running                                        OK
> v Harbor                  running, proxy-pull configured                 OK
> v SeaweedFS               running, 4 volumes                             OK
> v JetStream               5 streams created                              OK
> v OpenClaw                installed                                      OK
> v Stalwart                installed                                      OK
> v WordPress               installed                                      OK
> v DNS                     clinicname.openova.io   propagated             OK
> v TLS                     LE PROD cert issued for all subdomains         OK

   Your Sovereign is ready.

   Console:   https://console.clinicname.openova.io
   OpenClaw:  https://openclaw.clinicname.openova.io
   Stalwart:  https://mail.clinicname.openova.io
   Website:   https://clinicname.openova.io

   I have created your first user (you) with the password I just
   emailed. Want me to walk you through OpenClaw for ten minutes
   to get the first patient record in, or are you good on your own?
```

End-to-end the user typed ~3 messages and never saw a form field.

## End-user journey — voice

The exact same flow with voice ingestion (browser Web Speech API, supports Arabic, Turkish, English, French, German, Spanish, and 60+ others — already an open Web Platform feature). Each user utterance is transcribed in-tab, sent to the chat as a normal user message, the agent responds with text **and** TTS playback. The user can keep talking or switch to typing at any moment; the modes are not separate sessions.

TTS provider options (any work):

- **Native Web Speech `speechSynthesis`** — zero cost, no API, voice quality varies by OS.
- **OpenAI TTS** — best voices, pay per character.
- **Local model** in the Sovereign once it is up — for the post-provision Sandbox sessions; provisioning runs on mothership so it cannot use the Sovereign's model.

For the launch surface, default to Web Speech for ingestion + OpenAI TTS for response, with a toggle to Web Speech-only for users who want zero external calls.

## Architecture

```
                user (browser, voice or text)
                          │
                          ▼
            ┌──────────────────────────────────┐
            │  console.openova.io/start        │
            │  (Sandbox shell in chat mode,    │
            │   card-protocol view only —      │
            │   no terminal, no agent install) │
            └────────────────┬─────────────────┘
                             │ WebSocket
                             ▼
            ┌──────────────────────────────────┐
            │  mothership Sandbox concierge     │
            │  (single pod, shared across all   │
            │   prospects; per-user session)    │
            │  agent: claude-code or qwen-code  │
            └────────────────┬─────────────────┘
                             │ MCP
                             ▼
            ┌──────────────────────────────────┐
            │  concierge-mcp  (narrow surface)  │
            │                                   │
            │  catalyst.openapi                 │
            │  catalyst.deployments.create      │
            │  catalyst.deployments.events      │
            │  catalyst.deployments.get         │
            │  marketplace.catalog.list         │
            │  marketplace.region.list          │
            │  marketplace.sku.list             │
            │  skills.healthcare-compliance     │
            │  skills.education-compliance      │
            │  skills.finance-compliance        │
            │  ...                              │
            │                                   │
            │  (NO sandbox.*, NO k8s.*,         │
            │   NO sovereign.* — the prospect    │
            │   has no Sovereign yet)            │
            └────────────────┬─────────────────┘
                             │
                             ▼
                    existing catalyst-api
                    /api/v1/deployments
                    /api/v1/deployments/{id}/events  (SSE)
```

The concierge is a shared mothership service (one pod, many simultaneous user sessions). Once provisioning completes, the chat hands the user off to **their own Sovereign's** Sandbox, where the full MCP surface is available, the agent runs in their own vcluster, and identity has fully transferred to their Keycloak realm. The mothership concierge session ends.

## Why this is not a separate product

Architecturally, conversational provisioning is just Sandbox with:

- A narrower MCP toolbox (provisioning instead of operations).
- A shared mothership pod instead of per-user vcluster pods.
- Card-protocol-only surface (no terminal — the prospect has no agent installed and no repo).
- A pre-login state (email magic link instead of Org-scoped OIDC).

Same code path for: WebSocket, card translator, MCP client wiring, session persistence, SSE subscription, voice ingestion. One product, two scopes.

## Implementation requirements (when we revisit)

- **Mothership pod** — `sandbox-concierge`, single Deployment in the OpenOva mothership cluster. Long-lived to amortise model warm-up.
- **`concierge-mcp`** — a MCP server bundling the narrow toolbox above; deployed as a sidecar to the concierge pod.
- **Card-only Sandbox front-end** — reuses the mobile mode of the main Sandbox PWA, rendered at `console.openova.io/start`.
- **Voice in/out toggle** — browser Web Speech for ingestion, OpenAI TTS for response (configurable).
- **Skill packs for verticals** — `healthcare-compliance`, `education-compliance`, `finance-compliance`, `retail-ecommerce` so the concierge knows region/data-residency hints per industry. Each is a versioned markdown file in the same skill-library OCI artifact.
- **Hand-off ritual** — when the deployment reaches `phase: ready`, the concierge issues the handover JWT (already in `catalyst/bootstrap/api/internal/handoverjwt/signer.go`) and redirects the browser to `https://console.<new-sov>/sandbox` with the user signed in to their new Sovereign.

No new backend endpoints required on catalyst-api beyond what `catalyst-ui` already calls.

## Where the wizard still wins

We keep `catalyst-ui`. The user who knows what they want will always be faster filling a form than narrating to an agent. Two paths, same destination, the user picks. The chat is the path that grows the top of the funnel — the wizard is the path that converts the user who already arrived.
