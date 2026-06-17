package github

import (
	"fmt"
	"testing"
)

// TestIsGiteaRefRaceError covers the #3744 widening: Gitea surfaces a
// concurrent-commit collision as an HTTP 500 whose body carries the raw git
// push rejection ("PushRejected ... cannot lock ref ... failed to update ref"),
// which the original matcher (409 / "not a fast forward" only) missed — so the
// loser of a genuine concurrent commit was classified fatal with zero retries.
func TestIsGiteaRefRaceError(t *testing.T) {
	// The verbatim error string the provisioning service logged on hw158 when
	// two provisions for one tenant raced the sme-tenants branch.
	hw158 := fmt.Errorf(`change files: GitHub API POST http://gitea-http.gitea.svc.cluster.local:3000/api/v1/repos/openova/openova/contents: 500 {"message":"PushRejected Error: exit status 1 - remote: error: cannot lock ref 'refs/heads/sme-tenants': is at 97576e3c but expected 3fd04dbe\nTo /data/git/gitea-repositories/openova/openova.git\n ! [remote rejected] c68a2775 -> sme-tenants (failed to update ref)\nerror: failed to push some refs"}`)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"hw158 cannot-lock-ref 500", hw158, true},
		{"bare cannot lock ref", fmt.Errorf("remote: error: cannot lock ref 'refs/heads/sme-tenants'"), true},
		{"failed to update ref", fmt.Errorf("! [remote rejected] -> sme-tenants (failed to update ref)"), true},
		{"PushRejected", fmt.Errorf(`500 {"message":"PushRejected Error: exit status 1"}`), true},
		{"is at X but expected Y", fmt.Errorf("is at 97576e3c but expected 3fd04dbe"), true},
		// Pre-existing signals must still match (no regression).
		{"409 conflict", fmt.Errorf("GitHub API POST ...: 409 conflict"), true},
		{"not a fast forward", fmt.Errorf("update ref failed: not a fast forward"), true},
		{"branch has been changed", fmt.Errorf("the target branch has been changed"), true},
		// Genuinely unrelated errors must remain fatal (not retried).
		{"401 unauthorized", fmt.Errorf("GitHub API POST ...: 401 unauthorized"), false},
		{"404 not found", fmt.Errorf("GitHub API GET ...: 404 not found"), false},
		{"malformed path 422", fmt.Errorf("422 tree.path contains a malformed path component"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGiteaRefRaceError(tc.err); got != tc.want {
				t.Fatalf("isGiteaRefRaceError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
