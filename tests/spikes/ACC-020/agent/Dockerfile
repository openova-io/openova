# Throwaway image of lab test ACC-020: the relay/driver binary plus the two
# test pages and the upstream browser library (placed in web/lib/ by the
# workflow from the Apache release on Maven Central). Build context: the
# folder tests/spikes/ACC-020.
FROM docker.io/library/golang:1.23.4-alpine AS build
WORKDIR /src
COPY agent/ ./
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent .

FROM scratch
COPY --from=build /out/agent /agent
COPY web/ /web/
USER 10001:10001
ENTRYPOINT ["/agent"]
