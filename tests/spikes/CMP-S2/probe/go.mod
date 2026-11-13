module github.com/openova-io/openova/tests/spikes/cmp-s2/probe

go 1.22

require (
	github.com/nats-io/nats.go v1.37.0
	github.com/openova-io/openova/core/services/shared v0.0.0
)

// The stream is created by the platform's own code, not by a copy of it.
replace github.com/openova-io/openova/core/services/shared => ../../../../core/services/shared
