module github.com/openova-io/openova/core/services/notification

go 1.24.0

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/openova-io/openova/core/services/shared v0.0.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/nats-io/nats.go v1.37.0 // indirect
	github.com/nats-io/nkeys v0.4.8 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/twmb/franz-go v1.18.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
	golang.org/x/crypto v0.45.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
