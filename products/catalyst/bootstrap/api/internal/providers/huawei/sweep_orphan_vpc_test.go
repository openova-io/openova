package huawei

import "testing"

// TestIsReclaimableOrphanVPC locks the #4431 match/protection contract:
// catalyst- prefix required, bastion + non-catalyst hard-protected, any
// in-flight deployment-ID prefix in the name protects the VPC.
func TestIsReclaimableOrphanVPC(t *testing.T) {
	// Two in-flight deployments protected by their 8-char ID prefix.
	active := map[string]struct{}{
		"5b413990": {},
		"deadbeef": {},
	}

	cases := []struct {
		name string
		vpc  string
		want bool
	}{
		// Reclaimable: catalyst-prefixed, no active prefix in the name.
		{"terminal orphan from wiped prov", "catalyst-t38-omani-works-aabbccdd-me-east-215-a-vpc", true},
		{"terminal orphan second region", "catalyst-t38-omani-works-aabbccdd-me-east-215-b-vpc", true},

		// Protected: name carries an in-flight deployment-ID prefix.
		{"in-flight prov region a", "catalyst-omantel-biz-5b413990-me-east-215-a-vpc", false},
		{"in-flight prov region b", "catalyst-omantel-biz-deadbeef-me-east-215-b-vpc", false},

		// Hard-protected: bastion + non-catalyst.
		{"bastion vpc", "bastion-openova-vpc", false},
		{"catalyst-named-bastion still protected", "catalyst-bastion-vpc", false},
		{"operator-owned non-catalyst", "default-vpc", false},
		{"empty name", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReclaimableOrphanVPC(tc.vpc, active); got != tc.want {
				t.Fatalf("isReclaimableOrphanVPC(%q) = %v, want %v", tc.vpc, got, tc.want)
			}
		})
	}
}

// TestIsReclaimableOrphanVPC_NoActiveProvs proves the post-wipe sweep
// (empty active set = no protection) reclaims every catalyst- VPC while
// still hard-protecting bastion + non-catalyst.
func TestIsReclaimableOrphanVPC_NoActiveProvs(t *testing.T) {
	empty := map[string]struct{}{}
	if !isReclaimableOrphanVPC("catalyst-t38-omani-works-5b413990-me-east-215-a-vpc", empty) {
		t.Fatal("with no in-flight provs every catalyst- VPC must be reclaimable")
	}
	if isReclaimableOrphanVPC("bastion-openova-vpc", empty) {
		t.Fatal("bastion VPC must stay protected even with empty active set")
	}
}
