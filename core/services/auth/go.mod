module github.com/openova-io/openova/core/services/auth

go 1.22

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/openova-io/openova/core/services/shared v0.0.0
	github.com/valkey-io/valkey-go v1.0.47
	golang.org/x/crypto v0.23.0
)

require (
	github.com/klauspost/compress v1.17.8 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/twmb/franz-go v1.18.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
	golang.org/x/sys v0.24.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
