# syntax=docker/dockerfile:1.7
#
# pty-server — runs inside every Sandbox pod (per Sandbox CRD spec).
# Surface: HTTP+WS on :7681. See architecture.md §2.
#
# Image is the "agent-runner" — it bundles the four publicly fetchable
# agent CLIs the Sandbox catalog promises (architecture.md §1, §7):
#
#   * qwen-code     — npm  @qwen-code/qwen-code      (Node.js)
#   * claude-code   — npm  @anthropic-ai/claude-code (Node.js)
#   * opencode      — npm  opencode-ai               (Node.js)
#   * aider         — pip  aider-chat                (Python 3)
#
# `cursor-agent` is intentionally NOT bundled. Per cursor.sh's product
# shape it is a cloud-resident IDE companion, not a self-hosted CLI;
# the architecture.md §7 catalog flags it `cursor-agent (cloud)` and
# the BYOS flow (claude-code-byos.md) is the analogous bring-your-own
# bridge for hosted vendors. If Cursor ships a public self-hosted CLI
# we add it here and bump the chart.
#
# Why we left distroless/static:
#   exec.Command("qwen-code") on a distroless image returned ENOENT
#   because none of the four CLIs are statically linked Go binaries —
#   three of them need a Node.js runtime, one needs Python. The single
#   replacement base that covers both runtimes with the smallest blast
#   radius is `node:22-bookworm-slim`, which already carries the
#   Debian apt index we need to add `python3` + `pip` in one layer.
#
# Image-size budget: ~600 MiB (Node 22 slim ≈ 220 MiB + pip aider tree
# ≈ 90 MiB + three npm trees ≈ 200 MiB + pty-server binary ≈ 12 MiB +
# overhead). Distroless was ≈ 14 MiB; we trade ~580 MiB for end-user
# Pillar 4 (TBD-P4 / #1986). Worth it — the four agents are the
# Sandbox surface.

# ---------------------------------------------------------------------
# Stage 1 — build pty-server (Go).
# ---------------------------------------------------------------------
FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/pty-server ./cmd/pty-server

# ---------------------------------------------------------------------
# Stage 2 — final runtime image with pty-server + agent CLIs.
#
# Base: node:22-bookworm-slim (LTS, Debian 12 family — matches the
# `distroless/static-debian12` libc family the previous image used so
# the pty-server binary's static link stays portable).
# ---------------------------------------------------------------------
FROM node:22-bookworm-slim

# OCI labels — auto-discoverable by the GitHub Packages UI.
LABEL org.opencontainers.image.title="sandbox-pty-server-agent-runner" \
      org.opencontainers.image.description="In-pod PTY broker + bundled agent CLIs (qwen-code, claude-code, aider, opencode). See products/sandbox/pty-server/Dockerfile for the agent matrix." \
      org.opencontainers.image.source="https://github.com/openova-io/openova"

# System packages: python3 + pip for aider, ca-certificates for HTTPS
# from the agent binaries to vendor LLM endpoints, git because aider +
# claude-code both shell out to `git` for repo operations, tini for
# proper PID-1 signal handling under the StatefulSet shape.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        python3 \
        python3-pip \
        python3-venv \
        tini \
    && rm -rf /var/lib/apt/lists/*

# ---- Agent #1: qwen-code (npm) ---------------------------------------
# Canonical CLI per https://www.npmjs.com/package/@qwen-code/qwen-code.
# We install globally so the binary is on PATH for exec.Command.
RUN npm install -g --omit=dev --no-fund --no-audit @qwen-code/qwen-code \
    && qwen --version || true

# ---- Agent #2: claude-code (npm) -------------------------------------
# Canonical CLI per https://www.npmjs.com/package/@anthropic-ai/claude-code.
RUN npm install -g --omit=dev --no-fund --no-audit @anthropic-ai/claude-code \
    && claude --version || true

# ---- Agent #3: opencode (npm) ----------------------------------------
# Canonical CLI per https://www.npmjs.com/package/opencode-ai.
RUN npm install -g --omit=dev --no-fund --no-audit opencode-ai \
    && opencode --version || true

# ---- Agent #4: aider (pip, via pipx-style venv) ----------------------
# Aider is Python; isolate it in /opt/aider-venv so its dependency
# closure doesn't collide with system python. Symlink the entrypoint
# into /usr/local/bin so it's on PATH alongside the npm-installed
# trio.
RUN python3 -m venv /opt/aider-venv \
    && /opt/aider-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/aider-venv/bin/pip install --no-cache-dir aider-chat \
    && ln -s /opt/aider-venv/bin/aider /usr/local/bin/aider \
    && aider --version || true

# Stable alias names matching the agent slugs the user-journey wizard
# advertises (user-journey.md:204 "[x] claude-code [x] cursor-agent
# [x] qwen-code"). The npm packages expose short names (`claude`,
# `qwen`, `opencode`) — alias the slug form so `exec.Command("qwen-code")`
# resolves without B3's slug→binary registry having to translate.
# B3 (slug-aware exec) will replace these symlinks with a typed
# registry; until then the symlinks unblock the runtime path.
RUN set -eux; \
    for pair in \
        "qwen:qwen-code" \
        "claude:claude-code" \
        ; do \
        from="${pair%%:*}"; to="${pair##*:}"; \
        if [ -x "/usr/local/bin/${from}" ] && [ ! -e "/usr/local/bin/${to}" ]; then \
            ln -s "/usr/local/bin/${from}" "/usr/local/bin/${to}"; \
        fi; \
    done

# Copy the pty-server binary from the build stage.
COPY --from=build /out/pty-server /usr/local/bin/pty-server

# Run as the pre-baked `node` user (uid 1000) — non-root posture
# preserved from the distroless image (which ran as nonroot:nonroot).
# Each agent CLI reads $HOME for its config/state; /home/node is
# writable to that uid.
USER node
WORKDIR /home/node

EXPOSE 7681

# tini for signal-safe pid 1 (agents fork worker processes that need
# clean SIGTERM propagation on session DELETE).
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/pty-server"]
