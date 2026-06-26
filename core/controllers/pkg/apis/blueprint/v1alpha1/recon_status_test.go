package v1alpha1

import "testing"

func TestDeriveReconStatus(t *testing.T) {
	primary := ObservedTarget{Region: "region-a", Role: DataRolePrimary, Ready: true}
	hotStandbyReady := ObservedTarget{Region: "region-b", Role: DataRoleStandby, StandbyType: StandbyHot, Ready: true}
	hotStandbyNotReady := ObservedTarget{Region: "region-b", Role: DataRoleStandby, StandbyType: StandbyHot, Ready: false}
	hotStandbyDegraded := ObservedTarget{Region: "region-b", Role: DataRoleStandby, StandbyType: StandbyHot, Degraded: true}

	t.Run("all ready -> Reconciled, no reason", func(t *testing.T) {
		s := DeriveReconStatus([]ObservedTarget{primary, hotStandbyReady})
		if s.Placement != ReconStatusReconciled {
			t.Fatalf("placement = %q, want Reconciled", s.Placement)
		}
		if s.Reason != "" {
			t.Errorf("reason = %q, want empty (no second value)", s.Reason)
		}
	})

	t.Run("one not ready -> Reconciling with plain reason", func(t *testing.T) {
		s := DeriveReconStatus([]ObservedTarget{primary, hotStandbyNotReady})
		if s.Placement != ReconStatusReconciling {
			t.Fatalf("placement = %q, want Reconciling", s.Placement)
		}
		if s.Reason == "" {
			t.Error("expected a plain reason naming the not-ready target")
		}
	})

	t.Run("one degraded -> Degraded", func(t *testing.T) {
		s := DeriveReconStatus([]ObservedTarget{primary, hotStandbyDegraded})
		if s.Placement != ReconStatusDegraded {
			t.Fatalf("placement = %q, want Degraded", s.Placement)
		}
	})

	t.Run("empty -> Reconciling (honest, never a fabricated class)", func(t *testing.T) {
		s := DeriveReconStatus(nil)
		if s.Placement != ReconStatusReconciling {
			t.Fatalf("placement = %q, want Reconciling", s.Placement)
		}
	})

	// §13 — the Armed attribute is orthogonal to the recon status: an armed
	// Hot standby that is still reconciling stays Reconciling (Armed is a
	// display attribute of the standby, never a second status value), and an
	// armed-and-ready placement is Reconciled. Armed never fabricates a class.
	t.Run("Armed is orthogonal to recon status", func(t *testing.T) {
		armedReady := ObservedTarget{Region: "region-b", Role: DataRoleStandby, StandbyType: StandbyHot, Ready: true, Armed: true}
		armedNotReady := ObservedTarget{Region: "region-b", Role: DataRoleStandby, StandbyType: StandbyHot, Ready: false, Armed: true}

		if s := DeriveReconStatus([]ObservedTarget{primary, armedReady}); s.Placement != ReconStatusReconciled {
			t.Errorf("armed+ready ⇒ Reconciled, got %q", s.Placement)
		}
		if s := DeriveReconStatus([]ObservedTarget{primary, armedNotReady}); s.Placement != ReconStatusReconciling {
			t.Errorf("armed+not-ready ⇒ Reconciling, got %q", s.Placement)
		}
	})
}
