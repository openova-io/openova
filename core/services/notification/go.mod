module github.com/openova-io/openova/core/services/notification

go 1.22

require github.com/openova-io/openova/core/services/shared v0.0.0

require (
	github.com/golang-jwt/jwt/v5 v5.2.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.8 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/twmb/franz-go v1.18.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
