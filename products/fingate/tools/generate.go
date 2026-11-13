// Package tools generates the Go code of the six documents of profile rw-4.0.0.
//
// Each line below runs the pinned oapi-codegen (see the tool line of go.mod) with one
// settings file of oapi/ on one document. The output, internal/gen/<package>/, is
// committed and never edited by hand: run go generate ./... in this folder instead.
package tools

//go:generate go tool oapi-codegen -config oapi/accountinfo.yaml ../profiles/rw-4.0.0/account-info-openapi.yaml
//go:generate go tool oapi-codegen -config oapi/paymentinitiation.yaml ../profiles/rw-4.0.0/payment-initiation-openapi.yaml
//go:generate go tool oapi-codegen -config oapi/vrp.yaml ../profiles/rw-4.0.0/vrp-openapi.yaml
//go:generate go tool oapi-codegen -config oapi/confirmationfunds.yaml ../profiles/rw-4.0.0/confirmation-funds-openapi.yaml
//go:generate go tool oapi-codegen -config oapi/events.yaml ../profiles/rw-4.0.0/events-openapi.yaml
//go:generate go tool oapi-codegen -config oapi/eventnotifications.yaml ../profiles/rw-4.0.0/event-notifications-openapi.yaml
