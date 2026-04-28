module github.com/openova-io/openova/core/services/gateway

go 1.22

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/openova-io/openova/core/services/shared v0.0.0
	github.com/valkey-io/valkey-go v1.0.47
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/lib/pq v1.10.9 // indirect
	golang.org/x/sys v0.24.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
