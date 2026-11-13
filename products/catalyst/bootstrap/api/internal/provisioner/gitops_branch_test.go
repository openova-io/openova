// Tests for the request field gitopsBranch (FND-014). A deployment request
// may name the Git ref the new Sovereign follows: "main", or a candidate ref
// rc/<UTC yyyymmddThhmmssZ>-<first 7 hex characters of the commit>. Without
// the field a request behaves as before. Any other value is refused.

package provisioner

import (
	"encoding/json"
	"strings"
	"testing"
)

const gitopsBranchCandidate = "rc/20261120T060000Z-4be7a11"

// gitopsBranchRequest returns a request that is valid apart from the field.
func gitopsBranchRequest(value string) Request {
	req := validBase()
	req.Region = "fsn1"
	req.GitopsBranch = value
	return req
}

// gitopsBranchTfvars returns the tofu variable gitops_branch, the field's reader.
func gitopsBranchTfvars(t *testing.T, value string) any {
	t.Helper()
	dir := t.TempDir()
	if err := writeTfvars(dir, gitopsBranchRequest(value)); err != nil {
		t.Fatalf("writeTfvars with gitopsBranch=%q: %v", value, err)
	}
	return readTfvars(t, dir)["gitops_branch"]
}

// Case "empty": the ref is the value of CATALYST_GITOPS_BRANCH, or "main" when
// that is unset. The environment value is not matched against the pattern.
func TestGitopsBranchEmpty(t *testing.T) {
	for _, tc := range []struct{ env, want string }{{"", "main"}, {"mirror-branch", "mirror-branch"}} {
		t.Setenv("CATALYST_GITOPS_BRANCH", tc.env)
		if got, err := resolveGitopsBranch(Request{}); err != nil || got != tc.want {
			t.Fatalf("env %q: resolveGitopsBranch(empty) = %q, %v; want %q", tc.env, got, err, tc.want)
		}
		if got := gitopsBranchTfvars(t, ""); got != tc.want {
			t.Fatalf("env %q: gitops_branch = %v, want %q", tc.env, got, tc.want)
		}
	}
	req := gitopsBranchRequest("")
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() without gitopsBranch: %v", err)
	}
	if raw, err := json.Marshal(Request{}); err != nil || strings.Contains(string(raw), "gitopsBranch") {
		t.Fatalf("a request without the field must encode without the key gitopsBranch: %s, %v", raw, err)
	}
}

// gitopsBranchAccepted proves that value arrives under its JSON name, passes
// Validate(), wins over the environment and reaches gitops_branch unchanged.
func gitopsBranchAccepted(t *testing.T, value string) {
	t.Helper()
	t.Setenv("CATALYST_GITOPS_BRANCH", "mirror-branch")
	var decoded Request
	if err := json.Unmarshal([]byte(`{"gitopsBranch":"`+value+`"}`), &decoded); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if got, err := resolveGitopsBranch(decoded); err != nil || got != value {
		t.Fatalf("resolveGitopsBranch(%q) = %q, %v; want %q", value, got, err, value)
	}
	req := gitopsBranchRequest(value)
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() with gitopsBranch=%q: %v", value, err)
	}
	if got := gitopsBranchTfvars(t, value); got != value {
		t.Fatalf("gitops_branch = %v, want %q", got, value)
	}
}

// Cases "main" and "valid candidate ref".
func TestGitopsBranchMain(t *testing.T)         { gitopsBranchAccepted(t, "main") }
func TestGitopsBranchCandidateRef(t *testing.T) { gitopsBranchAccepted(t, gitopsBranchCandidate) }

// Case "invalid value": refused by resolveGitopsBranch, by Validate() and by
// writeTfvars, with the same error. The request is otherwise valid.
func TestGitopsBranchInvalid(t *testing.T) {
	const want = "invalid gitopsBranch"
	for _, value := range []string{
		"develop", "Main", " main", "main ", "main\n", " ", "refs/heads/main", "wp/FND-014",
		"rc/", "rc/latest", "rc/20261120T060000Z", "rc/20261120T060000Z-4be7a1",
		"rc/20261120T060000Z-4be7a111", "rc/20261120T060000Z-4BE7A11", "rc/20261120T060000Z-4be7a1g",
		"rc/2026-11-20T06:00:00Z-4be7a11", "rc/20261120T060000Z-4be7a11/x", "x/rc/20261120T060000Z-4be7a11",
	} {
		if got, err := resolveGitopsBranch(Request{GitopsBranch: value}); err == nil || err.Error() != want {
			t.Errorf("resolveGitopsBranch(%q) = %q, %v; want the error %q", value, got, err, want)
		}
		req := gitopsBranchRequest(value)
		if err := req.Validate(); err == nil || err.Error() != want {
			t.Errorf("Validate() with gitopsBranch=%q: %v; want the error %q", value, err, want)
		}
		if err := writeTfvars(t.TempDir(), gitopsBranchRequest(value)); err == nil || err.Error() != want {
			t.Errorf("writeTfvars with gitopsBranch=%q: %v; want the error %q", value, err, want)
		}
	}
}
