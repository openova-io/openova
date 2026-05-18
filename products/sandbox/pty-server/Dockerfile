# syntax=docker/dockerfile:1.7
#
# pty-server — runs inside every Sandbox pod (per Sandbox CRD spec).
# Surface: HTTP+WS on :7681. See architecture.md §2.

FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/pty-server ./cmd/pty-server

FROM gcr.io/distroless/static-debian12:nonroot
USER nonroot:nonroot
COPY --from=build /out/pty-server /usr/local/bin/pty-server
EXPOSE 7681
ENTRYPOINT ["/usr/local/bin/pty-server"]
