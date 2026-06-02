// Unit tests for Application multi-instance types — locked semantics
// for the admission package + the migration script.
package v1alpha1

import "testing"

func TestDefaultNamingTemplate(t *testing.T) {
	cases := []struct {
		name     string
		isolation IsolationLevel
		want     string
	}{
		{"namespace isolation → AppName-InstanceID", IsolationNamespace, "{{.AppName}}-{{.InstanceID}}"},
		{"vcluster isolation → AppName only (vcluster scopes)", IsolationVCluster, "{{.AppName}}"},
		{"empty isolation → namespace default", "", "{{.AppName}}-{{.InstanceID}}"},
		{"unknown isolation → namespace default (safe)", "cluster-per-instance", "{{.AppName}}-{{.InstanceID}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultNamingTemplate(tc.isolation)
			if got != tc.want {
				t.Fatalf("DefaultNamingTemplate(%q) = %q, want %q", tc.isolation, got, tc.want)
			}
		})
	}
}

func TestIsValidIsolationLevel(t *testing.T) {
	for _, v := range []string{"", "namespace", "vcluster"} {
		if !IsValidIsolationLevel(v) {
			t.Errorf("IsValidIsolationLevel(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"vCluster", "Namespace", "cluster", "host", "vm"} {
		if IsValidIsolationLevel(v) {
			t.Errorf("IsValidIsolationLevel(%q) = true, want false", v)
		}
	}
}

func TestMultiInstanceSpec_ApplyDefaults(t *testing.T) {
	t.Run("nil receiver is a no-op", func(t *testing.T) {
		var m *MultiInstanceSpec
		m.ApplyDefaults() // must not panic
	})

	t.Run("empty isolation → namespace; empty template → AppName-InstanceID", func(t *testing.T) {
		m := &MultiInstanceSpec{}
		m.ApplyDefaults()
		if m.IsolationLevel != IsolationNamespace {
			t.Fatalf("IsolationLevel = %q, want namespace", m.IsolationLevel)
		}
		if m.NamingTemplate != "{{.AppName}}-{{.InstanceID}}" {
			t.Fatalf("NamingTemplate = %q, want {{.AppName}}-{{.InstanceID}}", m.NamingTemplate)
		}
	})

	t.Run("vcluster isolation → AppName-only default template", func(t *testing.T) {
		m := &MultiInstanceSpec{IsolationLevel: IsolationVCluster}
		m.ApplyDefaults()
		if m.IsolationLevel != IsolationVCluster {
			t.Fatalf("IsolationLevel = %q, want vcluster", m.IsolationLevel)
		}
		if m.NamingTemplate != "{{.AppName}}" {
			t.Fatalf("NamingTemplate = %q, want {{.AppName}}", m.NamingTemplate)
		}
	})

	t.Run("explicit template preserved", func(t *testing.T) {
		m := &MultiInstanceSpec{NamingTemplate: "{{.Blueprint}}-{{.AppName}}"}
		m.ApplyDefaults()
		if m.NamingTemplate != "{{.Blueprint}}-{{.AppName}}" {
			t.Fatalf("NamingTemplate clobbered: %q", m.NamingTemplate)
		}
	})

	t.Run("InstanceID never auto-populated by ApplyDefaults", func(t *testing.T) {
		// Per W2.C2 contract: InstanceID is supplied by the caller at
		// CREATE time (admission webhook computes from metadata.uid).
		// ApplyDefaults() must NOT touch it — otherwise the immutability
		// invariant breaks on Update reconciles that pass an empty spec.
		m := &MultiInstanceSpec{}
		m.ApplyDefaults()
		if m.InstanceID != "" {
			t.Fatalf("InstanceID = %q, must be empty (no auto-fill)", m.InstanceID)
		}
	})
}

func TestFirstUIDChars(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a3f7c91d-1234-5678-9abc-def012345678", "a3f7c91d"},
		{"short", "short"},
		{"", ""},
		{"01234567", "01234567"},
		{"0123456789", "01234567"},
	}
	for _, tc := range cases {
		got := FirstUIDChars(tc.in)
		if got != tc.want {
			t.Errorf("FirstUIDChars(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
