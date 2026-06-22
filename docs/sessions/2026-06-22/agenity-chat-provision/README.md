# Agenity chat→provision — runtime brought online (2026-06-22)

**Env:** omantel.biz / kom4dc (Huawei) dep `4635277cae4ffed9` (PERMANENT).
**Surface:** `https://agenity.demo.omani.homes/app/` (chepherd dashboard) + the demo Org `org-7283eb4a-19e5-4e86-9066-d4aa26762064`.
**Issue:** #4111 (runtime) / #3988 + #4116 (the remaining MCP-bearer authz leg).
**Chart fix shipped:** `bp-agenity` 0.4.0 → 0.5.0.

## Status — last validated: omantel.biz / agenity-demo (2026-06-22)

| UAT row | Was | Now | What changed |
|---|---|---|---|
| 220 — agent live + chat works | ❌ runtime offline | ✅ | Runtime ONLINE, `1 worker`, claude-code authenticates via OAuth + receives chat |
| 221 — chat→MCP→create_application | ❌ blocked on 220 | ⚠️ | Chat + MCP work; create 403s on the catalyst-api `org-scoped-forbidden` (#3988/#4116) |
| 222 — created app converges in Org | ❌ | ❌ | Blocked on 221 (the write 403) |
| 223 — MCP RBAC Org-scoped | ⚠️ console-only | ⚠️ | MCP `whoami` exercised live; Org-scoping enforced but bearer lacks `org_id` |

## The defect chain (all on the spawned claude-code path, all fixed)

The dashboard served fine but every "+ spawn agent" died: claude-code exited
code 1 at 113 bytes → "runtime offline · 0 workers". Five independent defects,
each fixed in `bp-agenity` 0.5.0 and verified live:

1. **`ANTHROPIC_API_KEY` shadowed the OAuth blob.** claude-code prefers
   `$ANTHROPIC_API_KEY` over `~/.claude/.credentials.json`, and an
   `sk-ant-oat01` OAuth access-token is NOT a valid bare API key → "Invalid API
   key". Fix: omit the env entirely in `credentialsKey` (OAuth) mode.
2. **Wrong spawner.** In-K8s the daemon auto-picked the `operator` (K8s-Job)
   spawner, which needs an unshipped `bp-chepherd-operator` (upstream #130) →
   "operator spawner not yet wired". Fix: blank `KUBERNETES_SERVICE_HOST/PORT`
   so it uses the in-pod BareExec spawner.
3. **First-run gates.** The "trust this folder?" + "Bypass Permissions mode …
   No,exit" prompts killed every `--dangerously-skip-permissions` PTY session.
   Fix: seed `.claude.json` with `bypassPermissionsModeAccepted:true` +
   per-project `hasTrustDialogAccepted` (claude-code preserves both).
4. **`.mcp.json` EACCES.** The root daemon created the per-session dir
   `0700-root` but forks the agent as uid 1000 → EACCES reading its own
   `--mcp-config` → exit 1. Fix: pin the pod to `runAsUser/fsGroup 1000`.
5. **chepherd-MCP NXDOMAIN + unexpanded `$()`.** chepherd writes the MCP URL as
   `ws://host.containers.internal:9090` (NXDOMAIN in BareExec), and embeds
   literal `$(OPENOVA_MCP_BEARER)`/`$(…PUBKEY)` in `CHEPHERD_EXTRA_MCP_JSON`
   which it does NOT `$()`-expand. Fix: add a `host.containers.internal →
   127.0.0.1` hostAlias + drop the two literals (the openova-mcp child inherits
   both from the container env).

## Proven live

- Banner: `1 session · 1 worker` (no "runtime offline") — `04-runtime-online-1-worker.png`.
- Agent live-attached, `status: ● live`, pid in `/var/chepherd/repo` — `05-agent-live-attached.png`.
- Full chat round-trip in the Team Transcript + the agent's escalation — `06-chat-provision-agent-working.png`.
- The agent's own session transcript calling `mcp__openova__whoami` →
  `demo@openova.io` and attempting `create_application` —
  `agent-transcript-readable.txt` / `agent-transcript-create_application-attempt.jsonl`.

## The remaining blocker (NOT the runtime — #3988/#4116)

`create_application` 403s with `org-scoped-forbidden`. Root cause:
- The openova-mcp **always** sends `X-Tenant-Host` (#4116), so catalyst-api
  treats the session as Org-scoped.
- `create_application` routes to the **sovereign-wide** path
  `POST /api/v1/sovereigns/{id}/applications`, which `OrgScopeGuard` denies for
  an org-scoped session (it is NOT on `orgSafePathPrefixes`).
- The demo bearer is minted `tier:owner role:sovereign-admin` with **no
  `org_id`**, so it is in a dead zone: rejected for org-scoped writes AND, as a
  sovereign-admin, it must name an explicit org — which then also 403s.

Fix belongs in #3988/#4116: either mint an **Org-pinned** session bearer
(`tier:org-admin`, `org:<slug>`) for the demo Org, or route `create_application`
to the org-scoped install path that is on the allowlist.
