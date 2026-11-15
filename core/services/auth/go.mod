module github.com/openova-io/openova/core/services/auth

go 1.22

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/openova-io/openova/core/services/shared v0.0.0
	github.com/valkey-io/valkey-go v1.0.47
	golang.org/x/crypto v0.29.0
)

require (
	github.com/lib/pq v1.10.9 // indirect
	golang.org/x/sys v0.27.0 // indirect
)

replace github.com/openova-io/openova/core/services/shared => ../shared
