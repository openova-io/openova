// precheck_test.go — unit coverage for the three pre-PR endpoint checks.
package precheck

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidateMutation_OK(t *testing.T) {
	cases := []Mutation{
		{Org: "acme", App: "wp", Name: "ui", Hostname: "wp.acme.example.com", Port: 443, Protocol: "https", Visibility: "public", Op: OpCreate},
		{Org: "acme", App: "wp", Name: "metrics", Hostname: "wp-metrics.acme.example.com", Port: 9090, Protocol: "http", Visibility: "internal", Op: OpUpdate},
		{Org: "acme", App: "wp", Name: "delete-me", Op: OpDelete},
	}
	for i, m := range cases {
		if err := ValidateMutation(m); err != nil {
			t.Fatalf("case %d expected OK, got %v", i, err)
		}
	}
}

func TestValidateMutation_Errors(t *testing.T) {
	cases := []struct {
		m       Mutation
		wantSub string
	}{
		{Mutation{Org: "", App: "wp", Name: "ui", Op: OpCreate}, "Org"},
		{Mutation{Org: "acme", App: "", Name: "ui", Op: OpCreate}, "App"},
		{Mutation{Org: "acme", App: "wp", Name: "UI", Op: OpCreate}, "name"},
		{Mutation{Org: "acme", App: "wp", Name: "ui", Op: OpCreate}, "hostname"},
		{Mutation{Org: "acme", App: "wp", Name: "ui", Hostname: strings.Repeat("a", 254) + ".com", Op: OpCreate}, "RFC-1123"},
		{Mutation{Org: "acme", App: "wp", Name: "ui", Hostname: "ok.example.com", Port: 70000, Op: OpCreate}, "port"},
		{Mutation{Org: "acme", App: "wp", Name: "ui", Hostname: "ok.example.com", Protocol: "weird", Op: OpCreate}, "protocol"},
		{Mutation{Org: "acme", App: "wp", Name: "ui", Hostname: "ok.example.com", Visibility: "weird", Op: OpCreate}, "visibility"},
	}
	for i, c := range cases {
		err := ValidateMutation(c.m)
		if err == nil {
			t.Fatalf("case %d expected error, got nil", i)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.wantSub)) {
			t.Fatalf("case %d expected error containing %q; got %v", i, c.wantSub, err)
		}
	}
}

func TestCheckCertManager_NoLookup(t *testing.T) {
	res := CheckCertManager(context.Background(), Mutation{Op: OpCreate, Hostname: "x.example.com"}, nil)
	if res.Pass {
		t.Fatal("expected fail when lookup not wired")
	}
	if res.Code != "lookup-unavailable" {
		t.Fatalf("expected code lookup-unavailable, got %s", res.Code)
	}
}

func TestCheckCertManager_DeleteAlwaysPasses(t *testing.T) {
	res := CheckCertManager(context.Background(), Mutation{Op: OpDelete, Org: "acme", App: "wp", Name: "ui"}, nil)
	if !res.Pass {
		t.Fatalf("delete should always pass; got %+v", res)
	}
}

func TestCheckCertManager_NoExisting(t *testing.T) {
	res := CheckCertManager(context.Background(),
		Mutation{Op: OpCreate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		func(_ context.Context, _ string) (CertOwner, bool, error) { return CertOwner{}, false, nil },
	)
	if !res.Pass {
		t.Fatalf("no existing cert should pass; got %+v", res)
	}
}

func TestCheckCertManager_SameOwner(t *testing.T) {
	res := CheckCertManager(context.Background(),
		Mutation{Op: OpUpdate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		func(_ context.Context, _ string) (CertOwner, bool, error) {
			return CertOwner{Org: "acme", App: "wp"}, true, nil
		},
	)
	if !res.Pass {
		t.Fatalf("same owner should pass; got %+v", res)
	}
	if res.Code != "same-owner" {
		t.Fatalf("expected same-owner; got %s", res.Code)
	}
}

func TestCheckCertManager_DifferentOwner(t *testing.T) {
	res := CheckCertManager(context.Background(),
		Mutation{Op: OpCreate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		func(_ context.Context, _ string) (CertOwner, bool, error) {
			return CertOwner{Org: "bigcorp", App: "shop"}, true, nil
		},
	)
	if res.Pass {
		t.Fatalf("different owner should fail; got %+v", res)
	}
	if res.Code != "cert-conflict" {
		t.Fatalf("expected cert-conflict; got %s", res.Code)
	}
	if !strings.Contains(res.Message, "bigcorp/shop") {
		t.Fatalf("message should mention conflicting owner; got %q", res.Message)
	}
}

func TestCheckCertManager_LookupError(t *testing.T) {
	res := CheckCertManager(context.Background(),
		Mutation{Op: OpCreate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		func(_ context.Context, _ string) (CertOwner, bool, error) {
			return CertOwner{}, false, errors.New("apiserver down")
		},
	)
	if res.Pass {
		t.Fatal("lookup error should fail")
	}
	if res.Code != "lookup-failed" {
		t.Fatalf("expected lookup-failed; got %s", res.Code)
	}
}

func TestCheckDNSConflict_NoLookup(t *testing.T) {
	res := CheckDNSConflict(context.Background(), Mutation{Op: OpCreate, Hostname: "x.example.com"}, "gateway.example.com", nil)
	if res.Pass {
		t.Fatal("expected fail when lookup not wired")
	}
}

func TestCheckDNSConflict_DeleteAlwaysPasses(t *testing.T) {
	res := CheckDNSConflict(context.Background(), Mutation{Op: OpDelete, Name: "ui"}, "gateway.example.com", nil)
	if !res.Pass {
		t.Fatalf("delete should always pass; got %+v", res)
	}
}

func TestCheckDNSConflict_NoExisting(t *testing.T) {
	res := CheckDNSConflict(context.Background(),
		Mutation{Op: OpCreate, Hostname: "x.example.com"},
		"gateway.example.com",
		func(_ context.Context, _ string) (string, bool, error) { return "", false, nil },
	)
	if !res.Pass {
		t.Fatalf("no existing should pass; got %+v", res)
	}
}

func TestCheckDNSConflict_SameTarget(t *testing.T) {
	res := CheckDNSConflict(context.Background(),
		Mutation{Op: OpUpdate, Hostname: "x.example.com"},
		"gateway.example.com",
		func(_ context.Context, _ string) (string, bool, error) { return "gateway.example.com", true, nil },
	)
	if !res.Pass {
		t.Fatalf("same target should pass; got %+v", res)
	}
	if res.Code != "same-target" {
		t.Fatalf("expected same-target; got %s", res.Code)
	}
}

func TestCheckDNSConflict_DifferentTarget(t *testing.T) {
	res := CheckDNSConflict(context.Background(),
		Mutation{Op: OpCreate, Hostname: "x.example.com"},
		"gateway.example.com",
		func(_ context.Context, _ string) (string, bool, error) { return "other.example.com", true, nil },
	)
	if res.Pass {
		t.Fatal("different target should fail")
	}
	if res.Code != "dns-conflict" {
		t.Fatalf("expected dns-conflict; got %s", res.Code)
	}
}

func TestCheckKyverno_DeferredWhenNoLookup(t *testing.T) {
	res := CheckKyverno(context.Background(), Mutation{}, nil)
	if !res.Pass {
		t.Fatal("expected deferred-pass when lookup nil")
	}
	if res.Code != "deferred-to-repo-workflow" {
		t.Fatalf("expected deferred-to-repo-workflow; got %s", res.Code)
	}
}

func TestCheckKyverno_LookupError(t *testing.T) {
	res := CheckKyverno(context.Background(), Mutation{},
		func(_ context.Context, _ Mutation) (Result, error) {
			return Result{}, errors.New("boom")
		},
	)
	if res.Pass {
		t.Fatal("expected fail on lookup error")
	}
}

func TestCheckKyverno_LookupReturnsResult(t *testing.T) {
	res := CheckKyverno(context.Background(), Mutation{},
		func(_ context.Context, _ Mutation) (Result, error) {
			return Result{Pass: false, Code: "policy-violation", Message: "name must lowercase"}, nil
		},
	)
	if res.Pass {
		t.Fatal("expected fail")
	}
	if res.Stage != "kyverno-admission" {
		t.Fatalf("expected stage to be stamped; got %s", res.Stage)
	}
}

func TestRun_AllPass(t *testing.T) {
	b := Run(context.Background(),
		Mutation{Op: OpCreate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		"gateway.example.com",
		func(_ context.Context, _ string) (CertOwner, bool, error) { return CertOwner{}, false, nil },
		func(_ context.Context, _ string) (string, bool, error) { return "", false, nil },
		nil,
	)
	if !b.AllPass() {
		t.Fatalf("expected all pass; got %+v", b)
	}
	if len(b.FailedStages()) != 0 {
		t.Fatalf("expected 0 failed stages, got %v", b.FailedStages())
	}
}

func TestRun_OneFailsListed(t *testing.T) {
	b := Run(context.Background(),
		Mutation{Op: OpCreate, Org: "acme", App: "wp", Name: "ui", Hostname: "x.example.com"},
		"gateway.example.com",
		func(_ context.Context, _ string) (CertOwner, bool, error) {
			return CertOwner{Org: "other", App: "x"}, true, nil
		},
		func(_ context.Context, _ string) (string, bool, error) { return "", false, nil },
		nil,
	)
	if b.AllPass() {
		t.Fatal("expected at least one failure")
	}
	failed := b.FailedStages()
	if len(failed) != 1 || failed[0] != "cert-manager-precheck" {
		t.Fatalf("expected cert-manager-precheck failure; got %v", failed)
	}
}
