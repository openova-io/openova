# Sandbox — user journey

This document is the canonical wireframe storyboard for the two primary roles: the **developer** (Nova user inside an Org) and the **Sovereign admin** (operator). The flows are end-to-end, no wizards-in-prose.

## Native Claude Code is preserved

The developer's muscle memory survives. Inside the Sandbox terminal they keep:

- `/plan`, `/think hard`, `/compact`, `/clear`, `/status`, `/model`
- `Read`, `Edit`, `Write`, `Bash`, `TaskCreate`, `Grep`, `Glob` tool cards
- `--continue` resumption (now across devices, not just sessions)
- `CLAUDE.md` precedence (global, project, local) — Sandbox auto-merges a layer on top with the cluster context
- Plan mode card with Approve / Edit / Cancel

The same applies to Cursor, Qwen Code, Aider, Opencode — their native UX renders in the same xterm. Sandbox does not wrap or skin any agent.

---

## Developer journey — building EventForge in a Sandbox

The developer is an experienced Claude Code user. They are signed into their Org inside Sovereign `t39` and have just been invited to a Sandbox.

### Scene 1 — Enter the Sandbox

```
┌─ Sovereign Console — console.t39.omani.works ─────────────┐
│  Overview   Canvas   Sandbox   Workloads   Settings        │
└────────────────────────────────────────────────────────────┘
```

Click **Sandbox**, then **New session**.

```
┌─ New session ────────────────────────────────────────────┐
│   Agent     ( claude-code   v )                            │
│   Repo      ( + Create new repo... v )                     │
│              Name:    [ eventforge                       ] │
│              Owner:   ( emrah-baysal v )                   │
│              Visibility: ( private v )                     │
│   Branch    main (will be created)                          │
│   Sandbox   workshop-emrah · 50 GB · hel1                  │
│                                                              │
│   [ Start session -> ]                                       │
└──────────────────────────────────────────────────────────────┘
```

One click. Sandbox creates the Gitea repo in the Org's Gitea Org (already auto-provisioned by `organization-controller`), mounts a fresh PVC at `/repo/eventforge`, mints an MCP token scoped to the Sandbox, and drops the user into the Claude Code session.

### Scene 2 — The session opens, and it looks like home

```
╭─────────────────────────────────────────────────────────────╮
│  Welcome to Claude Code on OpenOva Sandbox                   │
│                                                              │
│  cwd       /repo/eventforge                                  │
│  model     claude-opus-4-7 [1m]                              │
│  CLAUDE.md global + sandbox (no project CLAUDE.md yet)       │
│  skills    23 loaded  (openova-sandbox skill pack)           │
│  mcp       openova-sandbox-mcp  *connected*                  │
│                                                              │
│  /help  /status  /clear  /compact  /model                    │
╰─────────────────────────────────────────────────────────────╯
> _
```

Exact same shell as the developer's laptop. Only `skills` and `mcp` lines are new — those announce the cluster-aware moat.

### Scene 3 — Describe the product, request a plan

```
> I want to build an event management webapp. Organizers create events,
  sell tickets via Stripe Connect. Two surfaces:
   - Organizer dashboard (auth, create event, set tiers, see sales)
   - Public event pages (no auth, buy ticket, get email confirmation)
  Next.js 15 App Router, Postgres, Tailwind, Resend for email.  /plan
```

Plan mode dispatches Explore-style sub-tasks against the Sandbox skill library and the RAG corpus:

```
[PLAN MODE — read only]
  v Read .claude/skills/openova-sandbox/sandbox-primitives.md
  v Read .claude/skills/openova-sandbox/stripe-connect.md
  v Read .claude/skills/openova-sandbox/postgres-via-cnpg.md
  v Read .claude/skills/openova-sandbox/auth-via-keycloak.md
  v rag.search  "Next.js 15 + Keycloak OIDC patterns"  8 hits
```

ExitPlanMode returns the familiar plan card:

```
┌─ Plan ──────────────────────────────────────────────────────┐
│  1. Scaffold Next.js 15 App Router + Tailwind in /repo      │
│  2. Provision Postgres via sandbox.db.provision             │
│  3. Drizzle schema: orgs, events, tiers, orders, tickets    │
│  4. Auth: Keycloak realm  eventforge-orgs                   │
│  5. Organizer dashboard:  /dashboard/events                  │
│  6. Public pages:         /e/[slug]                          │
│  7. Stripe Connect onboarding (sandbox.stripe.bindAccount)  │
│  8. Email: Resend (sandbox.secrets.resend)                   │
│  9. PR -> preview at eventforge.sb-t39.omani.homes           │
│                                                              │
│  [ Approve & build ]   [ Edit plan ]   [ Cancel ]            │
└──────────────────────────────────────────────────────────────┘
```

### Scene 4 — Sandbox primitives appear as cards

```
> v sandbox.db.provision  { name: eventforge, version: 16 }
      Provisioned CNPG cluster  eventforge-db
      Endpoint  eventforge-db-rw.workshop-emrah.svc
      Credential bound to secret  eventforge-db-app
      URI written to /repo/eventforge/.env.local              (600)
      Backups: daily to SeaweedFS                              OK

> v sandbox.auth.provisionRealm   { app: eventforge }
      realm     eventforge-orgs
      client    eventforge-web   (confidential, PKCE)
      issuer    https://auth.t39.omani.works/realms/...
      redirect  https://eventforge.sb-t39.omani.homes/...
                http://localhost:3000/...
      secret    bound to  eventforge-auth                      OK

> v sandbox.stripe.bindAccount    { mode: test }
      Connected to acct_1MxxxxxxxxxK
      Webhook endpoint  /api/stripe/webhook
      Webhook secret bound to  eventforge-stripe               OK
```

No secret ever prints to the terminal. Each binding lands in `.env.local` (mode 600, never staged).

### Scene 5 — Preview URL appears as a card

After the first push, JetStream pushes a `sandbox.preview.ready` event into the session. The card renders:

```
  v sandbox.preview.ready
      eventforge.sb-t39.omani.homes        open ->
      build sha     a8f3c12
      cold start    1.4 s
      health        /api/health  200
```

The URL works on phone, tablet, laptop — anywhere the user has a browser pointed at their Sovereign domain.

### Scene 6 — Mobile handoff

The developer leaves the office, opens `console.<orgslug>.omani.homes/sandbox/<session-id>` on their phone, and re-attaches to the same session. Today this opens the same xterm surface as desktop — the pty-server replays its ring buffer (`SANDBOX_RING_BUFFER_BYTES`, 1 MiB default, 16 MiB ceiling — TBD-V22 #1986 F1) so the scrollback is intact, then streams live. Multi-device handoff via the persistent PTY is real and shipped; a phone-friendly card-stream render of the same session is **deferred — see TBD-V30 [#2057](https://github.com/openova-io/openova/issues/2057)**. They type (cramped on a 5" screen, but functional):

```
> the buy page is missing tier selection. tiers should render as cards
  with name/price/remaining. selecting one carries through to Stripe
  Checkout as the line item.
```

Agent reads, edits, typechecks, commits, opens a PR, preview rebuilds. They tap the URL, complete a test purchase. Switch the laptop back on later — terminal view, same session, scrollback intact.

### Scene 7 — Ship to production

```
> resume
> the product feels right. let's go to production.
   - domain: eventforge.io  (bring-your-own via marketplace BYOD)
   - prod database is a separate CNPG cluster, NOT the sandbox one
   - prod Stripe is live mode, switch the secret binding
   - turn on Sentry for error tracking
  /think hard then /plan
```

The agent calls `marketplace.domain.byod` (which is `POST /domain/byod` — `core/services/domain/handlers/handlers.go:206`) to register `eventforge.io`, returns the CNAME target as a card, validates after DNS propagates, binds the cert, ships the production HelmRelease, watches the Flux roll. Final card:

```
  v Live
      eventforge.io                served by  sha a8f3c12
      staging.eventforge.io        served by  sha a8f3c12
      sandbox preview              eventforge.sb-t39.omani.homes
```

The user closes the laptop. The agent stays alive in the Sandbox.

---

## Sovereign admin journey

The admin sees an extra tab in the Sandbox surface — the **Admin** view — gated by their `org` and `role` claims in the token. Their own personal Sandbox sessions live in the **Sessions** tab exactly the same as a developer.

```
┌─ Sandbox · Admin · console.t39.omani.works ─────────────────┐
│                                                              │
│  [ Sandboxes ]  [ Quotas ]  [ Agents ]  [ Skills ]           │
│  [ Secrets ]    [ Domains ] [ Audit ]   [ Costs ]            │
│                                                              │
│  Sandboxes (4 active across 2 Orgs)                          │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ Org    User         Sessions  Storage  Spend(30d)    │  │
│  │ acme   emrah         3        8.2 GB   $42           │  │
│  │ acme   ali           1        2.1 GB   $11           │  │
│  │ blue   pelin         2        4.7 GB   $19           │  │
│  │ blue   contractor-1  0        0.4 GB   $0   paused   │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  Agent catalogue (Sovereign-wide)                            │
│  [x] claude-code  [x] cursor-agent  [x] qwen-code            │
│  [ ] aider        [ ] opencode                               │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

Per-Org admins see the same UI, scoped to one Org. The RBAC is the same model already in `Organization.spec.owners[].role` and propagated via `UserAccess` CRs by `organization-controller`. No new RBAC model is required — Sandbox is one more consumer of the existing identity fan-out.

---

## Multi-device coherence

| Action | Surface |
|---|---|
| Same session open in two tabs | Both tabs receive the same PTY byte stream; either tab can type; that is by design. |
| Close laptop, open phone | New WebSocket to the same `session_id`; the pty-server replays its ring buffer (default 1 MiB, operator-configurable via `SANDBOX_RING_BUFFER_BYTES` up to a 16 MiB ceiling — TBD-V22 #1986 F1) on connect, then streams live. |
| Pod restart (rare) | PTY dies. Agent restarts via `<agent> --continue` (each agent has an equivalent flag). Conversation history in `~/.claude/projects/...` or equivalent is preserved; in-flight tool call may be lost. |
| Same session on watch-style device | **Deferred — TBD-V30 [#2057](https://github.com/openova-io/openova/issues/2057).** The pty-server has a stub `WS /sessions/{id}/cards` route that currently returns the same raw byte stream framed as `{"type":"raw","bytes":...}` (no parsing); a card-translator that emits typed cards (`text` / `tool-call` / `diff` / `bash` / `preview-link`) and a watch-form-factor render are post-MVP. Today the only mobile surface is the same xterm via `/attach`. |

The pty-server (described in [`architecture.md`](architecture.md)) is the only piece that makes this work. **No tmux.**

---

## Conversational provisioning (pre-Sandbox)

A prospective customer who does not have a Sovereign yet lands on `console.t39.omani.works/start` and meets the same Sandbox-style shell — text-only or text+voice — scoped to a much smaller MCP toolbox (catalyst-api provisioning surface). See [`provisioning-chat.md`](provisioning-chat.md) for the full journey.
