// Tests for the sources of the Kubernetes API port 6443 (NET-011). A create
// request is refused with HTTP 422 while CATALYST_EGRESS_CIDRS is empty, and
// when an entry of either list is not an IPv4 network or opens the port to
// every address. Otherwise both lists reach the OpenTofu variables
// provisioner_cidrs and admin_cidrs.

package provisioner

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	cpsEgress = "192.0.2.10/32"
	cpsAdmin  = "198.51.100.0/28"
)

// cpsRequest returns a request that Validate() accepts.
func cpsRequest() Request {
	req := validBase()
	req.Region = "fsn1"
	return req
}

// cpsTfvars returns the two tofu variables that writeTfvars writes for req.
func cpsTfvars(t *testing.T, req Request) (admin, provisioner any) {
	t.Helper()
	dir := t.TempDir()
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	vars := readTfvars(t, dir)
	return vars["admin_cidrs"], vars["provisioner_cidrs"]
}

// cpsList turns a decoded JSON list into []string; anything else fails.
func cpsList(t *testing.T, name string, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want a JSON list (never null)", name, v)
	}
	out := []string{}
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s holds %#v, want strings only", name, item)
		}
		out = append(out, s)
	}
	return out
}

// TestControlPlaneSources_RefusesWhenEgressUnset: without
// CATALYST_EGRESS_CIDRS a valid request is refused with 422 and the fixed
// text, and Provision refuses to start.
func TestControlPlaneSources_RefusesWhenEgressUnset(t *testing.T) {
	const want = "CATALYST_EGRESS_CIDRS is not set: the provisioner could not reach the new cluster"
	for _, env := range []string{"", " , "} {
		t.Setenv(EnvEgressCIDRs, env)
		t.Setenv(EnvDefaultAdminCIDRs, cpsAdmin)
		req := cpsRequest()
		status, err := AdmitRequest(&req)
		if status != http.StatusUnprocessableEntity || err == nil || err.Error() != want {
			t.Fatalf("env %q: AdmitRequest = %d, %v; want 422 and %q", env, status, err, want)
		}
		if _, err := (&Provisioner{}).Provision(context.Background(), cpsRequest(), make(chan Event, 8)); !errors.Is(err, ErrEgressCIDRsUnset) {
			t.Fatalf("env %q: Provision = %v; want ErrEgressCIDRsUnset", env, err)
		}
	}
}

// TestControlPlaneSources_RefusesAnySourceAndMalformedRanges: every list
// refuses a range of prefix length 0 and anything that is not an IPv4
// network in canonical CIDR form, with 422.
func TestControlPlaneSources_RefusesAnySourceAndMalformedRanges(t *testing.T) {
	bad := []string{"0.0.0.0/0", "::/0", "2001:db8::/32", "10.0.0.1/24", "10.0.0.1", "10.0.0.0/33", "ten", "::ffff:10.0.0.0/104"}
	for _, entry := range bad {
		t.Run("request "+entry, func(t *testing.T) {
			t.Setenv(EnvEgressCIDRs, cpsEgress)
			t.Setenv(EnvDefaultAdminCIDRs, "")
			req := cpsRequest()
			req.AdminCIDRs = []string{cpsAdmin, entry}
			status, err := AdmitRequest(&req)
			if status != http.StatusUnprocessableEntity || err == nil || !strings.Contains(err.Error(), "adminCIDRs entry") {
				t.Fatalf("AdmitRequest = %d, %v; want 422 naming adminCIDRs", status, err)
			}
		})
		t.Run("default "+entry, func(t *testing.T) {
			t.Setenv(EnvEgressCIDRs, cpsEgress)
			t.Setenv(EnvDefaultAdminCIDRs, cpsAdmin+","+entry)
			req := cpsRequest()
			status, err := AdmitRequest(&req)
			if status != http.StatusUnprocessableEntity || err == nil || !strings.Contains(err.Error(), EnvDefaultAdminCIDRs+" entry") {
				t.Fatalf("AdmitRequest = %d, %v; want 422 naming %s", status, err, EnvDefaultAdminCIDRs)
			}
		})
		t.Run("egress "+entry, func(t *testing.T) {
			t.Setenv(EnvEgressCIDRs, cpsEgress+","+entry)
			t.Setenv(EnvDefaultAdminCIDRs, "")
			req := cpsRequest()
			status, err := AdmitRequest(&req)
			if status != http.StatusUnprocessableEntity || err == nil || !strings.Contains(err.Error(), EnvEgressCIDRs+" entry") {
				t.Fatalf("AdmitRequest = %d, %v; want 422 naming %s", status, err, EnvEgressCIDRs)
			}
		})
	}
	t.Setenv(EnvEgressCIDRs, cpsEgress)
	req := cpsRequest()
	req.AdminCIDRs = []string{"0.0.0.0/0"}
	if _, err := AdmitRequest(&req); err == nil || !strings.Contains(err.Error(), "never open to every address") {
		t.Fatalf("AdmitRequest with 0.0.0.0/0 = %v; want the any-source refusal", err)
	}
}

// TestControlPlaneSources_ValidateErrorStays400: a request that Validate()
// refuses is still answered with 400, also while the egress list is empty.
func TestControlPlaneSources_ValidateErrorStays400(t *testing.T) {
	t.Setenv(EnvEgressCIDRs, "")
	req := cpsRequest()
	req.SovereignFQDN = ""
	if status, err := AdmitRequest(&req); status != http.StatusBadRequest || err == nil {
		t.Fatalf("AdmitRequest without FQDN = %d, %v; want 400", status, err)
	}
}

// TestControlPlaneSources_ReachTfvars: the request field wins over the
// default list, the default list is used without it, both lists reach the
// tfvars file, and with nothing set both are [] and not null.
func TestControlPlaneSources_ReachTfvars(t *testing.T) {
	t.Setenv(EnvEgressCIDRs, " 192.0.2.10/32 , 203.0.113.0/29 ")
	t.Setenv(EnvDefaultAdminCIDRs, "198.51.100.64/27")

	req := cpsRequest()
	req.AdminCIDRs = []string{cpsAdmin}
	if status, err := AdmitRequest(&req); status != http.StatusOK || err != nil {
		t.Fatalf("AdmitRequest = %d, %v; want 200 and no error", status, err)
	}
	op, pr := cpsTfvars(t, req)
	if got := cpsList(t, "admin_cidrs", op); !reflect.DeepEqual(got, []string{cpsAdmin}) {
		t.Fatalf("admin_cidrs = %v, want the request's [%s]", got, cpsAdmin)
	}
	if got := cpsList(t, "provisioner_cidrs", pr); !reflect.DeepEqual(got, []string{"192.0.2.10/32", "203.0.113.0/29"}) {
		t.Fatalf("provisioner_cidrs = %v, want the two entries of %s", got, EnvEgressCIDRs)
	}

	op, _ = cpsTfvars(t, cpsRequest())
	if got := cpsList(t, "admin_cidrs", op); !reflect.DeepEqual(got, []string{"198.51.100.64/27"}) {
		t.Fatalf("admin_cidrs without the request field = %v, want the default [198.51.100.64/27]", got)
	}

	t.Setenv(EnvEgressCIDRs, "")
	t.Setenv(EnvDefaultAdminCIDRs, "")
	op, pr = cpsTfvars(t, cpsRequest())
	if len(cpsList(t, "admin_cidrs", op)) != 0 || len(cpsList(t, "provisioner_cidrs", pr)) != 0 {
		t.Fatalf("with nothing set: admin_cidrs = %v, provisioner_cidrs = %v; want [] and []", op, pr)
	}
}
