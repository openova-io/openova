// Package backingservice generates the DECLARATIVE binding objects that
// wire a consumer Blueprint to a bp-postgres data-instance, per ADR-0010.
//
// The contract is strictly declarative: given a consumer's
// `spec.backingServices[]` declaration, Generate returns the YAML/values
// the catalyst layer writes into IaC —
//
//   - a `databases[]` entry to merge into the bp-postgres instance HR's
//     values (which renders the CNPG `Database` CR + the
//     `Cluster.spec.managed.roles[]` entry + the reflected connection
//     Secret in platform/postgres/chart);
//   - the Flux `dependsOn` HR name the consumer HR must reference
//     (`bp-postgres-<instance>`, NOT bp-cnpg);
//   - the connection Secret name the consumer chart reads in
//     externalDatabase mode.
//
// There is NO custom controller and NO Crossplane here. This package only
// produces declarative values; Flux + the CNPG operator do all the
// reconciliation. See docs/adr/0010-reusable-shareable-backing-services.md.
package backingservice

import (
	"fmt"
	"strings"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// DatabaseBinding is one entry the generator emits into the bp-postgres
// instance HR's `databases[]` values. It mirrors the chart's
// values.databases[] shape (platform/postgres/chart/values.yaml) so the
// catalyst layer can marshal it straight into the instance HR.
type DatabaseBinding struct {
	// Name — the isolated database on the shared Cluster.
	Name string `json:"name" yaml:"name"`
	// Owner — the per-consumer login role (CNPG-managed).
	Owner string `json:"owner" yaml:"owner"`
	// Consumer — provenance for the Consumers (bindings) table.
	Consumer DatabaseConsumer `json:"consumer" yaml:"consumer"`
	// Reflect — where to mirror the connection Secret.
	Reflect DatabaseReflect `json:"reflect" yaml:"reflect"`
}

// DatabaseConsumer is the binding provenance shown in the UI Consumers table.
type DatabaseConsumer struct {
	Blueprint string `json:"blueprint" yaml:"blueprint"`
	Mode      string `json:"mode" yaml:"mode"`
}

// DatabaseReflect names the connection Secret + target namespace(s).
type DatabaseReflect struct {
	SecretName string   `json:"secretName" yaml:"secretName"`
	Namespaces []string `json:"namespaces" yaml:"namespaces"`
}

// Binding is the full generated result for one consumer backing-service
// declaration: the data-instance it binds to, the database binding to
// merge into that instance's HR, and the consumer-side dependsOn edge +
// connection Secret.
type Binding struct {
	// InstanceName — the bp-postgres data-instance name. For shared
	// bindings this is the declared InstanceRef; for private it is
	// derived from the consumer (`<consumer>-pg`).
	InstanceName string
	// InstanceBlueprintRef — the Flux HR name the consumer HR must
	// `dependsOn` (NOT bp-cnpg). Equals `bp-postgres-<InstanceName>`.
	InstanceBlueprintRef string
	// Mode — resolved binding mode (private|shared).
	Mode bpv1.BackingServiceMode
	// Database — the database binding entry for the instance HR's
	// `databases[]` values.
	Database DatabaseBinding
	// ConnectionSecretName — the Secret the consumer reads
	// (externalDatabase mode). Equals Database.Reflect.SecretName.
	ConnectionSecretName string
}

// Input carries the per-consumer context the generator needs that is not
// on the BackingServiceSpec itself.
type Input struct {
	// ConsumerBlueprint — the consumer Blueprint id (e.g. bp-harbor).
	ConsumerBlueprint string
	// ConsumerName — the consumer app/release name (defaults the
	// database/role/instance names). e.g. "harbor".
	ConsumerName string
	// ConsumerNamespace — the namespace the consumer runs in; the
	// connection Secret is reflected here.
	ConsumerNamespace string
}

// ErrUnsupportedType is returned for a backing-service type the generator
// does not handle. Postgres only, today.
type ErrUnsupportedType struct{ Type string }

func (e ErrUnsupportedType) Error() string {
	return fmt.Sprintf("backingservice: unsupported type %q (only \"postgres\" is supported)", e.Type)
}

// ErrSharedMissingInstanceRef is returned when mode=shared but no
// instanceRef is declared.
type ErrSharedMissingInstanceRef struct{ Consumer string }

func (e ErrSharedMissingInstanceRef) Error() string {
	return fmt.Sprintf("backingservice: consumer %q declares mode=shared but no instanceRef", e.Consumer)
}

// instanceBlueprintRef returns the Flux HR name a consumer dependsOn for
// a given data-instance. It is the bp-postgres release for that instance.
func instanceBlueprintRef(instanceName string) string {
	return "bp-postgres-" + instanceName
}

// Generate translates ONE consumer backing-service declaration into the
// declarative binding plan (ADR-0010). It is pure: no I/O, no cluster
// calls — it only computes the YAML/values the catalyst layer applies.
func Generate(spec bpv1.BackingServiceSpec, in Input) (Binding, error) {
	if strings.ToLower(strings.TrimSpace(spec.Type)) != "postgres" {
		return Binding{}, ErrUnsupportedType{Type: spec.Type}
	}

	mode := spec.Mode
	if mode == "" {
		mode = bpv1.BackingServiceModePrivate
	}

	// Resolve names with sensible defaults derived from the consumer.
	database := firstNonEmpty(spec.Database, in.ConsumerName)
	role := firstNonEmpty(spec.Role, database)
	secretName := firstNonEmpty(spec.SecretName, database+"-database-secret")

	// Resolve the data-instance the consumer binds to.
	var instanceName string
	switch mode {
	case bpv1.BackingServiceModeShared:
		if strings.TrimSpace(spec.InstanceRef) == "" {
			return Binding{}, ErrSharedMissingInstanceRef{Consumer: in.ConsumerName}
		}
		instanceName = spec.InstanceRef
	case bpv1.BackingServiceModePrivate:
		// A private binding gets its own dedicated instance named after
		// the consumer (the legacy 1:1 shape, e.g. harbor → harbor-pg).
		instanceName = firstNonEmpty(spec.InstanceRef, in.ConsumerName+"-pg")
	default:
		return Binding{}, fmt.Errorf("backingservice: unknown mode %q", mode)
	}

	return Binding{
		InstanceName:         instanceName,
		InstanceBlueprintRef: instanceBlueprintRef(instanceName),
		Mode:                 mode,
		ConnectionSecretName: secretName,
		Database: DatabaseBinding{
			Name:  database,
			Owner: role,
			Consumer: DatabaseConsumer{
				Blueprint: in.ConsumerBlueprint,
				Mode:      string(mode),
			},
			Reflect: DatabaseReflect{
				SecretName: secretName,
				Namespaces: []string{in.ConsumerNamespace},
			},
		},
	}, nil
}

// GenerateAll runs Generate over every backing-service declaration on a
// consumer. The returned slice is in declaration order. The first error
// stops generation (a bad declaration must fail loudly, never silently
// drop a binding — the dependency-graph-audit relies on every edge).
func GenerateAll(specs []bpv1.BackingServiceSpec, in Input) ([]Binding, error) {
	out := make([]Binding, 0, len(specs))
	for i, s := range specs {
		b, err := Generate(s, in)
		if err != nil {
			return nil, fmt.Errorf("backingservice[%d]: %w", i, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
