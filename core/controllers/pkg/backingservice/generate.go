// Package backingservice generates the DECLARATIVE binding objects that
// wire a consumer Blueprint to a shareable data-instance, per ADR-0010.
//
// The contract is strictly declarative: given a consumer's
// `spec.backingServices[]` declaration, Generate returns the YAML/values
// the catalyst layer writes into IaC —
//
//   - a `databases[]` entry to merge into the instance HR's values
//     (which renders the CNPG `Database` CR + the
//     `Cluster.spec.managed.roles[]` entry + the reflected connection
//     Secret in platform/postgres/chart);
//   - the Flux `dependsOn` HR name the consumer HR must reference
//     (`<instanceBlueprint>-<instance>`, NOT the operator/engine HR);
//   - the connection Secret name the consumer chart reads in
//     externalDatabase mode.
//
// ── DE-HARDCODED ENGINE (#3648) ─────────────────────────────────────
// The mapping from a backing-service `type` (the engine KIND, e.g.
// `postgres`) to the INSTANCE Blueprint id (`bp-postgres`) is NOT a
// hardcoded literal. It is resolved against a ProducerRegistry built from
// the operator Blueprints that DECLARE `spec.producesInstances` (kind +
// instanceBlueprint). Any operator Blueprint that declares it — CNPG
// today, a Redis/Valkey or Kafka operator tomorrow — produces instances
// through the SAME code with zero engine-name literals in the decision
// path. The default registry carries one entry, and that entry is the
// declared `producesInstances` on platform/cnpg/blueprint.yaml, NOT a
// `"postgres"` constant baked into the generator.
//
// There is NO custom controller and NO Crossplane here. This package only
// produces declarative values; Flux + the engine operator do all the
// reconciliation. See docs/adr/0010-reusable-shareable-backing-services.md.
package backingservice

import (
	"fmt"
	"sort"
	"strings"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// DatabaseBinding is one entry the generator emits into the instance HR's
// `databases[]` values. It mirrors the chart's values.databases[] shape
// (platform/postgres/chart/values.yaml) so the catalyst layer can marshal
// it straight into the instance HR.
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
	// InstanceName — the data-instance name. For shared bindings this is
	// the declared InstanceRef; for private it is derived from the
	// consumer (`<consumer>-pg`).
	InstanceName string
	// InstanceBlueprintRef — the Flux HR name the consumer HR must
	// `dependsOn` (NOT the engine/operator HR). Equals
	// `<instanceBlueprint>-<InstanceName>`, where instanceBlueprint comes
	// from the producer Blueprint's declared `producesInstances`.
	InstanceBlueprintRef string
	// Mode — resolved binding mode (private|shared).
	Mode bpv1.BackingServiceMode
	// Database — the database binding entry for the instance HR's
	// `databases[]` values.
	Database DatabaseBinding
	// ConnectionSecretName — the Secret the consumer reads
	// (externalDatabase mode). Equals Database.Reflect.SecretName.
	ConnectionSecretName string

	// InstanceTargets — #3969 (§8a/§8b): the placement targets the OWNED
	// (private) data-instance follows. Defaults to the consumer's resolved
	// Targets (Primary↔Primary, Standby↔Standby) so the database follows
	// the app by default. Empty when: the binding is SHARED (a shared
	// instance has its own independent placement — never cascaded); the
	// consumer carries no Targets; OR the owned dep was explicitly
	// decoupled via an OwnedDependencyOverride{Follow:false}. The catalyst
	// layer projects these onto the instance HR's per-cluster fan-out.
	InstanceTargets []bpv1.PlacementTarget

	// Followed — #3969 (§8b): true when this OWNED instance's placement
	// was cascaded from the consumer (the default). False when the binding
	// is shared, the consumer had no targets, or the dep was decoupled
	// (Follow:false). Lets the catalyst layer + UI show "[✓ follow]" vs a
	// decoupled badge without re-deriving the decision.
	Followed bool

	// Recommendations — #3969 (§8e): SOFT, non-blocking advisories. For a
	// SHARED binding whose instance placement is incompatible with the
	// consumer's targets (e.g. consumer Primary in region-b but the shared
	// instance lives only in region-a) the generator appends a
	// recommendation here. NEVER a hard error — the bind still reconciles.
	// The platform recommends, it never enforces, for shared deps.
	Recommendations []string
}

// Producer is one resolved operator→instance mapping: a backing-service
// KIND (e.g. `postgres`) and the INSTANCE Blueprint id that provisions it
// (e.g. `bp-postgres`). It is the in-engine projection of an operator
// Blueprint's declared `spec.producesInstances`.
type Producer struct {
	// Kind — the engine-class id a consumer names in
	// `backingServices[].type`. Lower-cased on registration.
	Kind string
	// InstanceBlueprint — the instance Blueprint id whose installs are
	// the shareable data-instances (the dependsOn HR prefix).
	InstanceBlueprint string
}

// ProducerRegistry resolves a backing-service KIND to the INSTANCE
// Blueprint that produces it. It is built from the operator Blueprints
// that DECLARE `spec.producesInstances` — so the engine never hardcodes
// "postgres". An unregistered kind is unsupported and fails loudly.
type ProducerRegistry struct {
	byKind map[string]Producer
}

// NewProducerRegistry builds a registry from a set of declared producer
// specs (each operator Blueprint's `spec.producesInstances`). Entries
// with an empty Kind or InstanceBlueprint are skipped (an operator that
// doesn't fully declare the pairing produces nothing). Later entries for
// the same kind win (deterministic for callers that pre-sort).
func NewProducerRegistry(specs ...bpv1.ProducesInstancesSpec) *ProducerRegistry {
	r := &ProducerRegistry{byKind: make(map[string]Producer, len(specs))}
	for _, s := range specs {
		r.Register(s.Kind, s.InstanceBlueprint)
	}
	return r
}

// Register adds (or overrides) one kind→instanceBlueprint mapping. A
// blank kind or instanceBlueprint is ignored. Kind is matched
// case-insensitively (lower-cased here and on lookup).
func (r *ProducerRegistry) Register(kind, instanceBlueprint string) {
	k := strings.ToLower(strings.TrimSpace(kind))
	ib := strings.TrimSpace(instanceBlueprint)
	if k == "" || ib == "" {
		return
	}
	r.byKind[k] = Producer{Kind: k, InstanceBlueprint: ib}
}

// Lookup returns the producer for a backing-service kind (case-
// insensitive) and whether one is registered.
func (r *ProducerRegistry) Lookup(kind string) (Producer, bool) {
	if r == nil {
		return Producer{}, false
	}
	p, ok := r.byKind[strings.ToLower(strings.TrimSpace(kind))]
	return p, ok
}

// Kinds returns the registered kinds, sorted — used to build a helpful
// "supported types" error message without hardcoding any engine name.
func (r *ProducerRegistry) Kinds() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefaultRegistry is the process-wide producer registry. It is seeded
// with the postgres producer so the legacy 1:1 + ADR-0010 shared-postgres
// paths keep working unchanged — but that seed is the DECLARED
// `producesInstances` of platform/cnpg/blueprint.yaml, not a literal the
// generator decision-logic branches on. The catalyst layer replaces /
// augments it from the live catalog (RegisterFromBlueprints) so a new
// operator Blueprint needs ZERO change here.
var DefaultRegistry = NewProducerRegistry(
	bpv1.ProducesInstancesSpec{Kind: "postgres", InstanceBlueprint: "bp-postgres"},
)

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

	// Targets — #3969 (§8a): the consumer's RESOLVED placement targets
	// (region × cluster × vCluster × role Primary|Standby, standby type
	// Hot|Cold). When non-empty, an OWNED (private) backing service's
	// placement DEFAULTS to follow these targets (Primary↔Primary,
	// Standby↔Standby, region-matched) so the app and its own database can
	// never silently diverge. Empty ⇒ the instance keeps whatever its own
	// Blueprint declares (legacy behaviour). The generator maps the
	// consumer's Primary target → the instance primary, each Standby(Hot)
	// → a streaming replica, each Standby(Cold) → a snapshot/bucket
	// follower. SHARED bindings do NOT cascade (a shared instance has its
	// own independent placement) — they get a soft recommendation only,
	// surfaced via Binding.Recommendations.
	Targets []bpv1.PlacementTarget

	// OwnedOverrides — #3969 (§8b): per-owned-dependency cascade overrides
	// keyed by the owned instance name. An entry with Follow:false
	// DECOUPLES that owned dep (it keeps its own placement; the consumer's
	// Targets are NOT projected onto it). Absent ⇒ follow by default.
	OwnedOverrides []bpv1.OwnedDependencyOverride

	// SharedInstanceTargets — #3969 (§8e): the known placement targets of
	// the SHARED instances a consumer binds to, keyed by instance name.
	// When present, the generator runs the read-only placement-closure
	// check: if the consumer's Primary region is not served by the shared
	// instance, it appends a SOFT recommendation (never a block). Absent ⇒
	// no closure check (the bind still reconciles).
	SharedInstanceTargets map[string][]bpv1.PlacementTarget
}

// ErrUnsupportedType is returned for a backing-service type that no
// producer Blueprint declares. The message lists the registered kinds so
// the error is helpful without hardcoding any engine name.
type ErrUnsupportedType struct {
	Type      string
	Supported []string
}

func (e ErrUnsupportedType) Error() string {
	if len(e.Supported) == 0 {
		return fmt.Sprintf("backingservice: unsupported type %q (no producer Blueprint declares producesInstances)", e.Type)
	}
	return fmt.Sprintf("backingservice: unsupported type %q (declared kinds: %s)", e.Type, strings.Join(e.Supported, ", "))
}

// ErrSharedMissingInstanceRef is returned when mode=shared but no
// instanceRef is declared.
type ErrSharedMissingInstanceRef struct{ Consumer string }

func (e ErrSharedMissingInstanceRef) Error() string {
	return fmt.Sprintf("backingservice: consumer %q declares mode=shared but no instanceRef", e.Consumer)
}

// instanceBlueprintRef returns the Flux HR name a consumer dependsOn for a
// given data-instance: `<instanceBlueprint>-<instanceName>`, where
// instanceBlueprint is the producer's DECLARED instance Blueprint id (no
// hardcoded engine name).
func instanceBlueprintRef(instanceBlueprint, instanceName string) string {
	return instanceBlueprint + "-" + instanceName
}

// Generate translates ONE consumer backing-service declaration into the
// declarative binding plan (ADR-0010) using the DefaultRegistry. It is
// pure: no I/O, no cluster calls. For a custom producer set (e.g. built
// from the live catalog), use GenerateWith.
func Generate(spec bpv1.BackingServiceSpec, in Input) (Binding, error) {
	return GenerateWith(DefaultRegistry, spec, in)
}

// GenerateWith is Generate against an explicit producer registry. The
// engine resolves the backing-service KIND to its INSTANCE Blueprint via
// the registry (built from declared `producesInstances`), so there is no
// engine-name literal in the decision path.
func GenerateWith(reg *ProducerRegistry, spec bpv1.BackingServiceSpec, in Input) (Binding, error) {
	producer, ok := reg.Lookup(spec.Type)
	if !ok {
		return Binding{}, ErrUnsupportedType{Type: spec.Type, Supported: reg.Kinds()}
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

	b := Binding{
		InstanceName:         instanceName,
		InstanceBlueprintRef: instanceBlueprintRef(producer.InstanceBlueprint, instanceName),
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
	}

	// #3969 cascade — resolve the OWNED-vs-SHARED placement behaviour.
	switch mode {
	case bpv1.BackingServiceModePrivate:
		// Owned (private) instance: follow the consumer's placement by
		// DEFAULT (§8a/§8b), unless an OwnedDependencyOverride{Follow:false}
		// decouples it. When followed, the instance's per-cluster fan-out is
		// driven by the consumer's resolved targets (Primary↔Primary,
		// Standby↔Standby) so the database can never silently diverge from
		// the app.
		if follows(instanceName, in.OwnedOverrides) && len(in.Targets) > 0 {
			b.Followed = true
			b.InstanceTargets = cloneTargets(in.Targets)
		}
	case bpv1.BackingServiceModeShared:
		// Shared instance: NEVER cascade (it has its own independent
		// placement). Run only the read-only placement-closure check and
		// emit a SOFT recommendation when incompatible — never a block (§8e).
		b.Recommendations = recommendForSharedClosure(instanceName, in.Targets, in.SharedInstanceTargets)
	}

	return b, nil
}

// follows reports whether the owned instance named `name` should follow
// the consumer's placement. Default true; an explicit override with
// Follow:false decouples it. Match is case-sensitive on the instance name
// (the catalyst layer mints both deterministically).
func follows(name string, overrides []bpv1.OwnedDependencyOverride) bool {
	for _, o := range overrides {
		if o.Name == name {
			return o.Follow
		}
	}
	return true
}

// cloneTargets returns a defensive copy of the targets slice so a Binding
// never aliases the caller's Input.Targets (a switchover that re-runs the
// resolver with flipped roles must not mutate a prior Binding).
func cloneTargets(in []bpv1.PlacementTarget) []bpv1.PlacementTarget {
	if len(in) == 0 {
		return nil
	}
	out := make([]bpv1.PlacementTarget, len(in))
	copy(out, in)
	return out
}

// recommendForSharedClosure is the first placement-closure logic in the
// codebase (#3969 §3 gap, §8e). It is READ-ONLY and NON-BLOCKING: when a
// consumer binds a SHARED instance whose placement does not serve the
// consumer's Primary region, it returns a SOFT recommendation string. It
// NEVER returns an error — the platform recommends, it never enforces, for
// shared deps. Returns nil when compatible, when the shared instance's
// placement is unknown, or when the consumer declared no targets.
func recommendForSharedClosure(
	instanceName string,
	consumerTargets []bpv1.PlacementTarget,
	sharedTargets map[string][]bpv1.PlacementTarget,
) []string {
	if len(consumerTargets) == 0 || len(sharedTargets) == 0 {
		return nil
	}
	instTargets, known := sharedTargets[instanceName]
	if !known || len(instTargets) == 0 {
		return nil
	}

	// The consumer's Primary region must be reachable on the shared
	// instance — i.e. the shared instance must run (Primary or Standby) in
	// the consumer's Primary region. If not, recommend (never block).
	instRegions := map[string]bool{}
	for _, t := range instTargets {
		instRegions[t.Region] = true
	}
	var recs []string
	for _, t := range consumerTargets {
		if t.Role != bpv1.DataRolePrimary {
			continue
		}
		if !instRegions[t.Region] {
			recs = append(recs, "shared instance "+quoteName(instanceName)+
				" has no target in the consumer's Primary region "+quoteName(t.Region)+
				"; consider placing it there to avoid cross-region writes (recommendation only — the bind still reconciles)")
		}
	}
	return recs
}

func quoteName(s string) string { return "\"" + s + "\"" }

// GenerateAll runs Generate over every backing-service declaration on a
// consumer (DefaultRegistry). The returned slice is in declaration order.
// The first error stops generation (a bad declaration must fail loudly,
// never silently drop a binding — the dependency-graph-audit relies on
// every edge).
func GenerateAll(specs []bpv1.BackingServiceSpec, in Input) ([]Binding, error) {
	return GenerateAllWith(DefaultRegistry, specs, in)
}

// GenerateAllWith is GenerateAll against an explicit producer registry.
func GenerateAllWith(reg *ProducerRegistry, specs []bpv1.BackingServiceSpec, in Input) ([]Binding, error) {
	out := make([]Binding, 0, len(specs))
	for i, s := range specs {
		b, err := GenerateWith(reg, s, in)
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
