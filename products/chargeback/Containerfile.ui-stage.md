# Containerfile — UI build stage (lane B → lane A hand-off, EPIC #6723)

`products/chargeback/Containerfile` is owned by lane A (the Go service). It
does not exist on the lane-B branch, so this file carries the EXACT node
stage lane A must prepend, plus the two lines the Go stage needs to pick the
bundle up. Delete this file once the stage is in the Containerfile.

## Contract

- Build context is the **repo root** (`docker build -f products/chargeback/Containerfile .`),
  the same shape as `products/openova-mcp/Containerfile` and what
  `.github/workflows/build-chargeback.yaml` passes (`context: .`).
- The UI is built with **Node 22** from `products/chargeback/ui/` using the
  committed `package-lock.json` (`npm ci`, never `npm install`), and
  `npm run build` runs `tsc -b && vite build` — a TypeScript error fails
  the image build.
- Output is `products/chargeback/ui/dist/` (gitignored). The Go binary
  embeds it (spec §5 "built into the Go binary via embed"), so the Go
  stage copies the stage-1 output to wherever lane A's `//go:embed`
  directive points — the path below assumes `ui/dist` next to the
  embedding package at `products/chargeback/internal/ui/dist`; adjust the
  destination to the real embed path, not the stage.
- The runtime stage stays distroless static, numeric `USER 65532:65532`
  (the chart pins the same `runAsUser`), `ENTRYPOINT ["/usr/local/bin/chargeback"]`.

## Stage to prepend (verbatim)

```dockerfile
# ─── Stage 1: build the UI bundle (React + TypeScript + Vite) ──────────────
# Node 22 LTS. `npm ci` pins to products/chargeback/ui/package-lock.json;
# `npm run build` = `tsc -b && vite build` (zero TypeScript errors or the
# image build fails). Output: /build/products/chargeback/ui/dist/.
FROM node:22-alpine AS ui
WORKDIR /build/products/chargeback/ui
COPY products/chargeback/ui/package.json products/chargeback/ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY products/chargeback/ui/ ./
RUN npm run build
```

## Lines to add to the Go stage

```dockerfile
# after `COPY . .` and before `go build` — put the bundle where the
# //go:embed directive reads it (replace the destination with the real path).
COPY --from=ui /build/products/chargeback/ui/dist ./products/chargeback/internal/ui/dist
```

## Smoke gate the workflow runs on the built image

`.github/workflows/build-chargeback.yaml` starts a Postgres 16 service and
runs the image with `LISTEN_ADDR`, `DATABASE_URL`, `APP_ENCRYPTION_KEY`,
`OPERATOR_EMAILS`, `PUBLIC_URL`, `PROFILE`; it then requires:

1. `GET /healthz` 200 and `GET /readyz` 200 (migrations applied);
2. `GET /` returns the embedded `index.html` (contains `<div id="root">`);
3. `GET /api/v1/auth/me` without a cookie returns **401**;
4. the `APP_ENCRYPTION_KEY` value never appears in the container logs.

Every non-`/api` path must fall back to `index.html` (BrowserRouter deep
links, spec §5).
