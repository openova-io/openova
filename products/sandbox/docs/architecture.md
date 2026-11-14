# Sandbox — architecture

This is the technical contract. It documents:

1. The shipped surface (native agent TUI in xterm.js, same on desktop and mobile) and the persistent process it attaches to. A second card-protocol surface for phones is **deferred — see TBD-V30 [#2057](https://github.com/openova-io/openova/issues/2057)**.
2. The `pty-server` shim — what it does, what tmux would have done wrong.
3. The MCP server tool catalogue (`openova-sandbox-mcp`).
4. The four knowledge layers (static / procedural / live / corpus).
5. Integration with the existing OpenOva primitives, with code citations.
6. The one prerequisite gap (long-lived API token with `org_id` claim).
7. The `Sandbox` CRD shape and the controller responsibilities (sketch — to be detailed when we implement).

---

## 1. Surfaces

### Web / PC — 100% native agent TUI in the browser

The `claude` (or `cursor-agent`, `qwen-code`, `aider`, `opencode`) **binary itself** runs in a pod inside the user's vcluster. Its stdout/stderr — ANSI escape codes, animations, plan-mode cards, all of it — is piped over a WebSocket to **xterm.js** in the user's browser tab. Keystrokes flow back over the same socket.

```
┌──────────────────────────────────┐       ┌─────────────────────────────────────────┐
│        BROWSER  (user tab)        │       │     SANDBOX POD  (in the Org vcluster)  │
│                                  │       │                                          │
│   xterm.js                       │       │   pty-server  (Go, ~300 LOC)             │
│   ├─ ANSI renderer (canvas/DOM)  │  WS   │   ├─ opens a PTY                         │
│   ├─ keyboard -> bytes ──────────┼──────►│   ├─ writes WS frames to PTY stdin       │
│   ├─ scrollback buffer           │◄──────┤   ├─ reads PTY stdout, fans out to WS    │
│   └─ resize events               │       │   ├─ ring buffer (1 MiB default) for replay│
│                                  │       │   └─ SIGWINCH on browser resize          │
└──────────────────────────────────┘       │              │                           │
                                           │              ▼ spawns once per session   │
                                           │   ┌──────────────────────────────────┐  │
                                           │   │  claude  (the CLI binary)         │  │
                                           │   │  --dangerously-skip-permissions   │  │
                                           │   │  Ink/React writes ANSI to stdout  │  │
                                           │   │  cwd = /repo/<repo>               │  │
                                           │   │  MCP client config attached       │  │
                                           │   └──────────────────────────────────┘  │
                                           └──────────────────────────────────────────┘
```

We do **not** re-implement Claude Code. We do **not** translate its output. xterm.js renders ANSI; the agent writes ANSI; they meet by accident.

### Mobile — same xterm surface today; card-protocol deferred

Today the mobile surface is the same xterm.js attach as desktop. The phone opens a WebSocket to `WS /sessions/{id}/attach`; the pty-server replays the ring buffer on connect and streams live thereafter. Multi-device coherence is real: closing a laptop tab and reopening the same `session_id` on a phone resumes the conversation with scrollback intact.

A card-protocol surface for small screens (the mobile PWA opens a parallel WS with `mode=cards`; the pty-server runs an in-pod card-translator that parses agent ANSI/Ink output into structured cards — `text`, `tool-call`, `diff`, `bash`, `preview-link`) was the architectural intent but is **deferred — see TBD-V30 [#2057](https://github.com/openova-io/openova/issues/2057)**. The blocker is real: agents emit Ink/TUI-rendered ANSI frames, not structured events, so the translator would have to reverse-engineer rendered frames per-agent and re-validate against every upstream agent release. Investigation memo `/tmp/investigate-f2-card-protocol-2026-05-20.md` (audit 2026-05-20) covers the scope (1500–2500 LOC, 3–5 PR chain) and the un-park criteria.

The shipped route `WS /sessions/{id}/cards` is a stub today: it wraps the same raw byte stream as `/attach` in `{"type":"raw","bytes":...}` JSON frames, with no parsing (see `pty-server/internal/server/routes.go:461-506` and the in-source comment "A future card-translator replaces the body with parsed cards"). The endpoint is preserved as a future-extension hook; no client consumes it today.

Both surfaces — the shipped xterm attach and the deferred card-protocol — observe the same persistent process. Closing a tab does not stop the agent.

---

## 2. The `pty-server` shim — explicitly not tmux

```
pty-server  (per Sandbox pod, listens on :7681)
├── POST /sessions                  spawn  <agent> in a fresh PTY,
│                                   return session_id, write to JetStream
├── WS   /sessions/{id}/attach      bidi: WS bytes ↔ PTY fd
│                                   on connect, replay ring buffer
├── WS   /sessions/{id}/cards       STUB — JSON-framed raw bytes today
│                                   (full card-translator deferred,
│                                    TBD-V30 #2057)
├── POST /sessions/{id}/resize      cols/rows -> SIGWINCH
├── POST /sessions/{id}/signal      INT / QUIT for user-driven aborts
└── DELETE /sessions/{id}           graceful stop, then SIGKILL
```

| We want | tmux gives us | Use tmux? |
|---|---|---|
| Persist a PTY across client disconnects | YES | No |
| Fan out PTY stdout to N concurrent WS clients | YES | No |
| Replay last N KB on reconnect | (via screen) | No |
| SIGWINCH on browser resize | YES | No |

| We do not want | tmux forces | Why bad |
|---|---|---|
| A second TUI rendering on top of the agent | Status bar, borders, panes | Fights with Ink redraw |
| A prefix-key input model (Ctrl-B) | YES | Hijacks keys the agent and user want |
| Window-manager features (split, detach UI) | YES | Bloat |
| Mobile-hostile rendering at small widths | YES | Breaks card view fallback |

The tmux **behaviour model** (one PTY, multiple clients, persistent across disconnect) is what we keep. The tmux **stack** is not in the picture.

---

## 3. MCP server — `openova-sandbox-mcp`

One MCP server per Sandbox session (sidecar in the same pod). The agent process speaks MCP over stdio. The server speaks to the Sovereign control plane over HTTPS using the user's token.

### Tool namespaces

```
gitea.*           repos, PRs, issues, releases on this Sovereign's Gitea
gitea-actions.*   workflow dispatch, run logs, artifact fetch
github.*          present ONLY if iOS/macOS-runner work is detected;
                  scoped GH PAT for that pipeline only
sandbox.db.*      provision / drop / dump  CNPG clusters in this Sandbox
sandbox.auth.*    provisionRealm, listClients, registerClient (Keycloak)
sandbox.stripe.*  bindAccount, listProducts, listPrices, createCheckoutSession
                  (key stored in Sandbox secret store; never re-passed)
sandbox.secrets.* read/write Sandbox-scoped secrets (never echoes)
sandbox.storage.* bindBucket / signedUploadURL (SeaweedFS-backed)
sandbox.preview.* status, rebuild, teardown
sandbox.deploy.*  staging / production (production gated on RBAC)
marketplace.*     domain.byod, domain.subdomain  (BYOD live today)
flux.*            status, reconcile, suspend  (scoped)
k8s.read.*        get / list / watch within Org vcluster
k8s.write.*       create / patch / delete   (only Sandbox namespace)
sovereign.*       describe, listOrgs        (super-admin only)
rag.search        per-Org hybrid index over repos + manifests + audit
skills.list/get   versioned OCI skill packs
```

### Subscriptions (the moat)

The agent calls MCP `resources/subscribe` once at session start with:

```
openova://flux/*
openova://hr/*
openova://preview/*
openova://gitea/pr/*
openova://gha/run/*
```

The MCP server consumes from the existing JetStream subjects under `catalyst.<domain>.<event>` (ADR-0001 §6 — `core/services/shared/events/nats.go:34-45`) and forwards them as MCP `notifications/resources/updated`. The agent gets *push* updates with no polling. The same JetStream subjects power the existing browser SSE feeds (`core/services/shared/events/nats.go`, `products/openova-flow/server/internal/api/stream.go`).

Browser and agent are subscribers to the **same** event bus, in their native protocols. One source of truth.

### RBAC enforcement

Every tool call is authorised against the bearer token's claims `{sovereign_id, org_id, roles[]}`. Tools annotated `super-admin only` (e.g., `sovereign.listOrgs`) require the `sovereign-admin` role. Tools annotated `org-admin only` (e.g., `sandbox.deploy.production`) require `org:<id>:admin`. The shape is a single permission string per tool, no role-graph traversal at call time.

---

## 4. Knowledge layers

Sandbox layers four flavours of "knowing the cloud", each chosen for what its time/freshness/size profile justifies:

| Layer | Answers | Mechanism | Loaded |
|---|---|---|---|
| **Static identity** | "What is OpenOva? Inviolable rules. Architecture invariants." | `CLAUDE.md` / `AGENTS.md` / `.cursorrules` — same content, each agent's native file. Mounted into `/repo/.claude/`, `/repo/AGENTS.md`, etc. at session start. | Every prompt. |
| **Procedural runbooks** | "How do I provision a CNPG cluster? How do I bind a domain via marketplace?" | **Skills** in `.claude/skills/openova-sandbox/*.md` (Claude) + parallel `.cursor/rules/*.mdc` / `aider/conventions/*.md` files. Versioned OCI artifact `ghcr.io/openova-io/sandbox-skills:<semver>`. | On demand. |
| **Live cluster state** | "What HRs are reconciling? Did the preview build finish?" | **MCP subscriptions** (above) translated from JetStream `catalyst.<domain>.<event>`. | Pushed. |
| **Corpus** | "Where in the repo is X defined? What did we ship last week?" | Per-Org **RAG** — hybrid (vector + BM25 + recency) over `/repo`, manifests, audit log, ADRs. NATS-watched for incremental reindex. | On `rag.search`. |

The Sandbox Pack distribution is one command:

```
openova pack install --agent=claude    # or cursor / qwen / aider / opencode
```

It drops the right CLAUDE.md / .cursorrules / aider conventions / skills directory / `.mcp.json` into the user's repo or global config.

---

## 5. Integration with existing OpenOva primitives

Every plumbing point below is verified against the codebase as of 2026-05-15.

| Sandbox use | Existing primitive | Reference |
|---|---|---|
| One vcluster per Org | `Organization` CRD + organization-controller renders vcluster HelmRelease into per-Org Gitea repo | `products/catalyst/chart/crds/organization.yaml:1-322`, `core/controllers/organization/internal/gitops/manifests.go:65-146` |
| One Keycloak realm per Sovereign (corporate) or per Org | mutually exclusive chart modes | `platform/keycloak/chart/values.yaml:24-192`, `chart/templates/configmap-{sovereign,tenant}-realm.yaml` |
| One Gitea Org per Org | auto-provisioned at Org create | `core/controllers/organization/internal/controller/organization_controller.go:177-198` |
| Marketplace subdomain + BYOD custom domain | `POST /domain/byod` returns CNAME target after validation | `core/services/domain/handlers/handlers.go:206-290`, `core/marketplace-api/handlers/handlers.go` |
| JetStream subject convention | `catalyst.<domain>.<event>`, tenancy in payload (`TenantID`) — ADR-0001 §6 | `core/services/shared/events/nats.go:34-45` |
| Browser SSE subscribers | Existing endpoints for deployments, cutover, RBAC audit, k8s, continuum, flow snapshot, openova-flow native (snapshot/upsert-*/delete-*) | `products/catalyst/bootstrap/api/internal/handler/*.go`, `products/openova-flow/server/internal/api/stream.go:23` |
| Harbor (host-cluster scope) | One Harbor per host cluster, proxy-pull mode | `platform/harbor/README.md:1-5, 73-96` |
| SeaweedFS (host-cluster scope) | Unified S3 endpoint `seaweedfs.storage.svc:8333` shared by all consumers | `platform/seaweedfs/README.md:1-4, 59-71` |
| CNPG (provisioned by apps on demand) | `Cluster.postgresql.cnpg.io` CRs created by `sandbox.db.provision` MCP tool | `platform/cnpg/README.md` |
| UserAccess CRs (RBAC fan-out) | One per Org owner, materialised to RoleBindings by useraccess-controller | `core/controllers/organization/internal/controller/organization_controller.go:227-234, 323-375` |

**Sandbox builds on top — it does not duplicate any of these.**

---

## 6. The one prerequisite — long-lived org-scoped tokens

Today the only access token in the system is a 15-minute JWT with claims `{sub, email, role}` (`core/services/auth/handlers/handlers.go:271-292`). The Keycloak tenant-realm has a `groups` protocol mapper but **no `org` mapper** (`platform/keycloak/chart/templates/configmap-tenant-realm.yaml:365-386`). The 5-minute handover JWT only carries `{email, sovereign_fqdn, deployment_id, role="sovereign-admin"}` (`catalyst/bootstrap/api/internal/handoverjwt/signer.go:79-99`).

Sandbox needs a token that:

- Lasts long enough for a coding session (hours, refreshable).
- Carries `org_id` and the user's role within that org as first-class claims.
- Carries a Sandbox capability set so the MCP server can authorise tool calls without round-tripping to Keycloak per call.

The minimal change:

1. Add an `org` protocol mapper to both `configmap-sovereign-realm.yaml` and `configmap-tenant-realm.yaml` — emits the user's Keycloak group attribute `org` into the token.
2. Extend `core/services/auth/handlers/handlers.go` to:
   - Read the user's groups from the Keycloak claim.
   - Include `org_id`, `groups[]`, and `capabilities[]` in the JWT it issues.
   - Add a personal-access-token issuance endpoint (`POST /auth/pat`) with admin-configurable TTL.
3. Document the claim contract in `core/services/shared/auth/claims.go` (new file) so the MCP server, sandbox-controller, and any future consumer share one definition.

No other prerequisite blocks Sandbox.

---

## 7. The `Sandbox` CRD (sketch — to be detailed at implementation)

```yaml
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: emrah
  namespace: acme            # = the Org's namespace
spec:
  owner:
    email: emrah@acme.com
    orgRef:
      slug: acme
  quota:
    cpu: "4"
    memory: "8Gi"
    storage: "50Gi"
    concurrentSessions: 3
  repos:
    - giteaRepo: acme/eventforge          # auto-cloned to a PVC
    - giteaRepo: acme/internal-tools
  agentCatalogue:                          # subset of Sovereign-enabled
    - claude-code
    - cursor-agent
  previewDomain: sb-emrah.rzk7.openova.io
status:
  sessions: 3
  storageUsed: 8.2Gi
  spend30d: "USD 42.10"
  previews:
    - prNumber: 1483
      url: https://pr-1483.eventforge.sb-emrah.rzk7.openova.io
      sha: a8f3c12
```

### Controller responsibilities

The `sandbox-controller` (new — sister to `organization-controller`) reconciles:

- A namespace `sandbox-<owner-uid>` inside the Org vcluster (not the host).
- PVCs for each `spec.repos[]` entry, with initContainer that does `git clone` against the Org's Gitea using a Sandbox-scoped deploy key.
- A shared build-cache pod (per Org, not per Sandbox) bound to a Sovereign-wide SeaweedFS bucket.
- A `pty-server` StatefulSet (1 replica, stable identity) per Sandbox.
- A `openova-sandbox-mcp` Deployment per Sandbox.
- The HTTPRoute / Gateway resources that publish `<pr>.<app>.<sb-<owner>>.<sov>.openova.io` (extends the marketplace subdomain registration).
- Keycloak ServiceAccount + ClientScope bindings for the Sandbox's long-lived token.

The controller writes its desired-state manifests into the Org's `catalyst-tenant` Gitea repo at `sandbox/<owner-uid>/`, exactly the same idiom `organization-controller` already uses for vcluster manifests. Flux on the host picks it up and reconciles into the Org's vcluster.

This is the only new controller Sandbox adds.
