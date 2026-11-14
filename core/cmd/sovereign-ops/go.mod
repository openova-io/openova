module github.com/openova-io/openova/core/cmd/sovereign-ops

go 1.23

require (
	github.com/openova-io/openova/core/controllers v0.0.0-00010101000000-000000000000
	sigs.k8s.io/yaml v1.4.0
)

require (
	k8s.io/apimachinery v0.31.1 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
)

replace github.com/openova-io/openova/core/controllers => ../../controllers
