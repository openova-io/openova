module github.com/openova-io/openova/core/services/model-meter

go 1.26.0

require github.com/openova-io/openova/core/services/shared v0.0.0

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/nats-io/nats.go v1.37.0 // indirect
	github.com/nats-io/nkeys v0.4.8 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
