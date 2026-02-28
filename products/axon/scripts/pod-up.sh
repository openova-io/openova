#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "==> Cleaning up existing pod (if any)"
podman pod rm -f axon 2>/dev/null || true

echo "==> Creating pod"
podman pod create --name axon -p 3000:3000

echo "==> Starting Valkey (available for Phase 2)"
podman run -d --pod axon --name axon-valkey docker.io/valkey/valkey:8

echo "==> Building gateway image"
podman build -t axon-gateway:dev -f "$PROJECT_DIR/Containerfile" "$PROJECT_DIR"

echo "==> Starting gateway"
podman run -d --pod axon --name axon-gateway \
  -v "${HOME}/.claude:/home/axon/.claude:ro" \
  -e AXON_API_KEYS="${AXON_API_KEYS:-sk-dev-test}" \
  -e AXON_DEFAULT_MODEL="${AXON_DEFAULT_MODEL:-claude-sonnet-4-6}" \
  -e ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
  localhost/axon-gateway:dev

echo "==> Pod running. Test with:"
echo "    curl localhost:3000/health"
echo "    curl localhost:3000/v1/models -H 'Authorization: Bearer sk-dev-test'"
