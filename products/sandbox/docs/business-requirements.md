# Sandbox — business requirements

## The problem we are solving

Developers using modern coding agents (Claude Code, Cursor, Qwen Code, Aider, Opencode) hit three structural ceilings:

1. **The agent dies when the laptop closes.** All current agents are local processes bound to a TTY and a workstation. Long-running work (multi-hour refactors, end-to-end provisioning, watching a deploy roll) can not survive a closed lid, a flaky Wi-Fi, or moving devices.
2. **The agent has zero awareness of the cloud the developer is shipping to.** It can edit code competently and run shell commands, but it does not know what Sovereign you operate, what cluster state exists right now, what your DNS topology is, what your secrets vault holds. Every cross-cluster question is a `kubectl` round-trip the developer has to wire by hand.
3. **The user has no way to use the agent from a phone, a tablet, a Chromebook, or a borrowed machine.** SSH + tmux + Termius is the workaround and it is brittle, ugly on mobile, and breaks down on every device boundary.

Beyond developers, there is a fourth problem at the on-ramp: **non-technical users cannot provision a Sovereign through a wizard.** The current `catalyst-ui` wizard is excellent for power users and integrators but presents 20+ fields at first contact. We are losing potential customers who would rather describe their need in two sentences than fill a form.

## Who Sandbox is for

| Audience | What they get | Pricing fit |
|---|---|---|
| **Sovereign super-admin** (the operator of the Sovereign) | Fleet view of every Org's Sandbox usage, agent catalogue control, org-level quotas, audit, cost attribution. Plus their own developer Sandbox in the same UI. | Bundled with Sovereign subscription. |
| **Org admin** (e.g., CTO of a customer Org inside a corporate Sovereign) | Org-scoped admin view: invite developers, set per-developer quotas, bind Org-level secrets (Stripe live, Resend prod), publish Org skill packs. Plus their own developer Sandbox. | Per-seat add-on on the Org subscription. |
| **Nova user — developer** (an engineer working inside an Org) | One personal Sandbox: persistent sessions, all approved agent brands, PVC-mounted repos, Org-shared build cache, auto preview URLs per PR, native-TUI in browser. | Per-seat. Pre-paid agent-token budget on top. |
| **Prospective Sovereign customer** (no Sovereign yet) | A conversational entry point that talks them through provisioning — text, or voice. The same Sandbox shell, scoped to provisioning MCP tools only. | Free conversion path; paid once the Sovereign is up. |

## Value proposition

Sandbox is not "another IDE." It is the **agent host platform** that is missing between "raw agent on a laptop" and "managed AI tooling SaaS." The differentiators that matter:

- **The Sovereign is the sandbox.** The agent runs inside the customer's own tenant cloud (vcluster per Org), under their own RBAC, against their own data, signing as them. Nothing leaves the tenant.
- **Live cluster awareness is native, not retrofitted.** OpenovaFlow's existing watcher fabric and JetStream subjects already publish the events. Sandbox makes the agent a first-class subscriber via MCP `resources/subscribe`. No `kubectl get` loops.
- **The same session is on every device.** Native Claude Code TUI in the browser via xterm.js + persistent PTY. Same session re-attaches from PC, iPad, or phone. The pty-server fans out multi-client like tmux but with no tmux in the stack.
- **Preview-per-PR is one click.** Every PR the agent opens auto-resolves to a live URL under the Org's marketplace subdomain (which already exists today). Click on phone, ship from laptop.
- **Conversational provisioning.** New customers describe what they need; the agent calls catalyst-api directly. This is the first surface where AI-first replaces forms.

## The moat

OpenOva already runs a **per-tenant vcluster cloud** with Keycloak identity, Gitea, Harbor, SeaweedFS, NATS JetStream, CNPG, marketplace DNS + BYOD, Crossplane reconcilers, Flux GitOps, and a live event fabric. No competitor in the AI-coding tools category has any of that. They have an editor with chat. We have a cloud with an agent host.

Every other "AI coding" tool tries to bolt cluster-awareness onto an editor. Sandbox does the inverse: it bolts an editor experience onto a cluster the user already owns.

## Success criteria

We will know Sandbox is the right product when:

- A developer on an iPad in a cab can ship a PR to production from a session that started on their desktop that morning, without re-attaching by hand.
- A new customer goes from `console.openova.io` to a working Sovereign by speaking three sentences. No wizard fields touched.
- The Sovereign admin can see "Org X spent $42 on agent tokens this week, 8 sessions, 3 production deploys" without writing a query.
- A Cursor user and a Claude Code user inside the same Org open each other's PRs and the cards in each session reference the other's diffs because both subscribed to the same JetStream subjects.
- The marketing pitch for Sovereign no longer leads with "secure tenant cloud" — it leads with "the only place where your agents can ship for you."

## Non-goals

- Sandbox is **not** an editor. It does not replace VS Code / Cursor / IntelliJ. It hosts agents; developers keep their editors.
- Sandbox is **not** a Codespaces clone. Codespaces hosts a dev environment; Sandbox hosts an *agent*. The dev environment is incidental.
- Sandbox does **not** ship its own model. Users bring their model subscription (Anthropic, OpenAI, Qwen, etc.) — the Sovereign holds the API key in its secret store.
- Sandbox does **not** replace `catalyst-ui` wizard for provisioning. The conversational entry is an alternative path for new users. Power users keep the wizard.

## Cost surface (for billing design)

Sandbox usage rolls up into the existing JetStream usage stream (`catalyst.usage.recorded`), tagged with `org_id` in the payload (the existing convention — `core/services/shared/events/nats.go`). The cost dimensions:

- **Compute** — vcluster pod CPU/memory hours per Sandbox session.
- **Storage** — PVC GB-hours for repo workdirs and build caches.
- **Agent tokens** — model API spend attributed to the session's owner.
- **Preview hosting** — pod-hours for live preview deployments under the Org's marketplace subdomain.

The super-admin dashboard shows per-Org rollup; the org admin dashboard shows per-developer rollup; the developer sees their own only.
