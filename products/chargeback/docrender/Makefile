# products/chargeback/docrender — developer targets. Everything here is
# local: no cluster access, no live credentials, no network at run time.

BIN      ?= bin/docrender
VERSION  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE    ?= ghcr.io/openova-io/chargeback-docrender:$(VERSION)
PREVIEW  ?= $(CURDIR)/bin/preview

.PHONY: build vet test run image preview clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/docrender

vet:
	go vet ./...

# -count=1 — Refs #6235: this repo has no root go.mod and several tests read
# fixtures outside their own package, which the build cache cannot see.
test: vet
	go test -count=1 ./...

run: build
	LISTEN_ADDR=:8080 $(BIN)

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) -f Containerfile .

# Writes every fixture's PDF to bin/preview so the layout can be LOOKED AT.
# Counts and testids do not prove a document reads well; opening it does.
preview:
	mkdir -p $(PREVIEW)
	DOCRENDER_PREVIEW_DIR=$(PREVIEW) go test -count=1 -run TestWritePreviews -v ./internal/pdf/
	@echo "previews in $(PREVIEW)"

clean:
	rm -rf bin
