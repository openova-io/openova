# products/chargeback — developer targets. Everything here is local: no
# cluster access, no live credentials.

BIN      ?= bin/chargeback
VERSION  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE    ?= ghcr.io/openova-io/chargeback:$(VERSION)
PG_URL   ?= postgres://chargeback:chargeback@localhost:5432/chargeback?sslmode=disable

.PHONY: build test vet lint run dev-db dev-db-stop integration image key clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/chargeback

vet:
	go vet ./...

test: vet
	go test ./...

# Integration tests need a Postgres; dev-db starts one in a container. They
# share one database and wipe it between tests, so packages run one at a time.
integration:
	CHARGEBACK_TEST_DATABASE_URL=$(PG_URL) go test -p 1 -count=1 ./...

dev-db:
	docker run -d --name chargeback-pg -e POSTGRES_USER=chargeback -e POSTGRES_PASSWORD=chargeback -e POSTGRES_DB=chargeback -p 127.0.0.1:5432:5432 postgres:16-alpine

dev-db-stop:
	docker rm -f chargeback-pg

# Generates a fresh APP_ENCRYPTION_KEY (32 random bytes, base64).
key:
	@head -c 32 /dev/urandom | base64

# Runs the service locally in development mode (PIN codes + invite links are
# logged instead of mailed; collector on; TLS verification off for the
# on-prem gateway CA). Set APP_ENCRYPTION_KEY and OPERATOR_EMAILS first.
run: build
	DATABASE_URL=$(PG_URL) PUBLIC_URL=http://localhost:8080 LISTEN_ADDR=:8080 $(BIN)

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) -f Containerfile .

clean:
	rm -rf bin
