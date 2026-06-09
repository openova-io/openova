package backingservice

import (
	"errors"
	"testing"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func TestGenerate_PrivateDefaults(t *testing.T) {
	// A private binding with only type+consumer derives its own
	// dedicated instance (the legacy 1:1 shape) and defaults the
	// database/role/secret from the consumer name.
	b, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres"},
		Input{ConsumerBlueprint: "bp-harbor", ConsumerName: "harbor", ConsumerNamespace: "harbor"},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Mode != bpv1.BackingServiceModePrivate {
		t.Errorf("mode = %q, want private", b.Mode)
	}
	if b.InstanceName != "harbor-pg" {
		t.Errorf("instance = %q, want harbor-pg", b.InstanceName)
	}
	if b.InstanceBlueprintRef != "bp-postgres-harbor-pg" {
		t.Errorf("dependsOn = %q, want bp-postgres-harbor-pg", b.InstanceBlueprintRef)
	}
	if b.Database.Name != "harbor" || b.Database.Owner != "harbor" {
		t.Errorf("db = %s/%s, want harbor/harbor", b.Database.Name, b.Database.Owner)
	}
	if b.ConnectionSecretName != "harbor-database-secret" {
		t.Errorf("secret = %q, want harbor-database-secret", b.ConnectionSecretName)
	}
	if got := b.Database.Reflect.Namespaces; len(got) != 1 || got[0] != "harbor" {
		t.Errorf("reflect ns = %v, want [harbor]", got)
	}
}

func TestGenerate_SharedExplicit(t *testing.T) {
	// A shared binding points at a named instance and provisions an
	// isolated database/role there. The dependsOn edge is the SHARED
	// instance, never bp-cnpg.
	b, err := Generate(
		bpv1.BackingServiceSpec{
			Type:        "postgres",
			Mode:        bpv1.BackingServiceModeShared,
			InstanceRef: "shared-pg",
			Database:    "registry",
			Role:        "harbor",
			SecretName:  "harbor-database-secret",
		},
		Input{ConsumerBlueprint: "bp-harbor", ConsumerName: "harbor", ConsumerNamespace: "harbor"},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.InstanceName != "shared-pg" {
		t.Errorf("instance = %q, want shared-pg", b.InstanceName)
	}
	if b.InstanceBlueprintRef != "bp-postgres-shared-pg" {
		t.Errorf("dependsOn = %q, want bp-postgres-shared-pg (NOT bp-cnpg)", b.InstanceBlueprintRef)
	}
	if b.Database.Name != "registry" || b.Database.Owner != "harbor" {
		t.Errorf("db = %s/%s, want registry/harbor", b.Database.Name, b.Database.Owner)
	}
}

func TestGenerate_SharedMissingInstanceRef(t *testing.T) {
	_, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared},
		Input{ConsumerName: "harbor"},
	)
	var want ErrSharedMissingInstanceRef
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want ErrSharedMissingInstanceRef", err)
	}
}

func TestGenerate_UnsupportedType(t *testing.T) {
	_, err := Generate(bpv1.BackingServiceSpec{Type: "mongodb"}, Input{ConsumerName: "x"})
	var want ErrUnsupportedType
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

// TestAggregate_TwoConsumersShareOneInstance is the reuse proof at the
// generator layer: harbor + gitea both declare a SHARED binding to the
// same shared-pg instance → ONE InstancePlan with TWO databases. The
// catalyst layer renders that as ONE bp-postgres HR → ONE CNPG Cluster
// with two isolated Database CRs + two roles.
func TestAggregate_TwoConsumersShareOneInstance(t *testing.T) {
	harbor, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "registry", Role: "harbor"},
		Input{ConsumerBlueprint: "bp-harbor", ConsumerName: "harbor", ConsumerNamespace: "harbor"},
	)
	if err != nil {
		t.Fatalf("harbor: %v", err)
	}
	gitea, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "gitea", Role: "gitea"},
		Input{ConsumerBlueprint: "bp-gitea", ConsumerName: "gitea", ConsumerNamespace: "gitea"},
	)
	if err != nil {
		t.Fatalf("gitea: %v", err)
	}

	plans := AggregateByInstance([]Binding{harbor, gitea})
	if len(plans) != 1 {
		t.Fatalf("got %d instance plans, want 1 (both consumers SHARE shared-pg)", len(plans))
	}
	p := plans[0]
	if p.InstanceName != "shared-pg" {
		t.Errorf("instance = %q, want shared-pg", p.InstanceName)
	}
	if len(p.Databases) != 2 {
		t.Fatalf("got %d databases on shared-pg, want 2 (registry + gitea)", len(p.Databases))
	}
	// Sorted by name: gitea < registry.
	if p.Databases[0].Name != "gitea" || p.Databases[1].Name != "registry" {
		t.Errorf("databases = %s,%s, want gitea,registry (sorted)", p.Databases[0].Name, p.Databases[1].Name)
	}
	if p.Databases[0].Owner != "gitea" || p.Databases[1].Owner != "harbor" {
		t.Errorf("owners = %s,%s, want gitea,harbor", p.Databases[0].Owner, p.Databases[1].Owner)
	}

	// The values fragment carries both databases under one instance.
	vals := p.InstanceValues()
	dbs, ok := vals["databases"].([]map[string]any)
	if !ok || len(dbs) != 2 {
		t.Fatalf("InstanceValues databases = %v, want 2 entries", vals["databases"])
	}
	inst, _ := vals["instance"].(map[string]any)
	if inst["name"] != "shared-pg" {
		t.Errorf("InstanceValues instance.name = %v, want shared-pg", inst["name"])
	}
}

func TestGenerateAll_OrderAndError(t *testing.T) {
	specs := []bpv1.BackingServiceSpec{
		{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "a", Role: "a"},
		{Type: "redis"}, // unsupported → must fail loudly
	}
	_, err := GenerateAll(specs, Input{ConsumerName: "c", ConsumerNamespace: "c"})
	if err == nil {
		t.Fatal("GenerateAll: want error on unsupported type, got nil")
	}
}
