package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// plain is a provider without a leftover lister; lister has one.
type plain struct{ providers.CloudProvider }

type lister struct {
	plain
	rep *providers.LeftoverReport
	err error
}

func (l lister) ListLeftovers(context.Context, providers.LeftoverSpec) (*providers.LeftoverReport, error) {
	return l.rep, l.err
}

func doc(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tofu.auto.tfvars.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunExitCodes(t *testing.T) {
	good := `{"provider":"x","sovereign_fqdn":"t1.omani.works","deployment_id":"0123abcd","secret":"s"}`
	clean := &providers.LeftoverReport{NamePrefix: "p", Kinds: []providers.LeftoverKind{{Kind: "servers", Used: 1, Leftovers: []string{}}}}
	left := &providers.LeftoverReport{NamePrefix: "p", Kinds: []providers.LeftoverKind{{Kind: "servers", Used: 1, Leftovers: []string{"p-a"}}}}
	with := func(p providers.CloudProvider) func(string) (providers.CloudProvider, error) {
		return func(string) (providers.CloudProvider, error) { return p, nil }
	}
	cases := []struct {
		name string
		args []string
		get  func(string) (providers.CloudProvider, error)
		want int
	}{
		{"no flag", nil, with(lister{rep: clean}), 2},
		{"a document that is not there", []string{"-vars", "/nonexistent/doc.json"}, with(lister{rep: clean}), 2},
		{"a document that is not JSON", []string{"-vars", doc(t, "x")}, with(lister{rep: clean}), 2},
		{"a document without deployment_id", []string{"-vars", doc(t, `{"provider":"x","sovereign_fqdn":"f"}`)}, with(lister{rep: clean}), 2},
		{"an unknown provider", []string{"-vars", doc(t, good)}, func(string) (providers.CloudProvider, error) { return nil, errors.New("unknown") }, 3},
		{"a provider without a lister", []string{"-vars", doc(t, good)}, with(plain{}), 3},
		{"a listing that failed", []string{"-vars", doc(t, good)}, with(lister{err: errors.New("status 500")}), 3},
		{"leftovers", []string{"-vars", doc(t, good)}, with(lister{rep: left}), 1},
		{"a clean account", []string{"-vars", doc(t, good)}, with(lister{rep: clean}), 0},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb, c.get); got != c.want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", c.name, got, c.want, errb.String())
		}
		var rep struct{ LeftoverCount *int }
		if c.want < 2 && (json.Unmarshal(out.Bytes(), &rep) != nil || rep.LeftoverCount == nil || *rep.LeftoverCount != c.want) {
			t.Errorf("%s: stdout %q is not a report with leftoverCount %d", c.name, out.String(), c.want)
		}
	}
}

// The registry the command uses holds an adapter with a leftover lister.
func TestTheRegisteredAdapterHasALister(t *testing.T) {
	p, err := providers.Get("huawei")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(providers.LeftoverLister); !ok {
		t.Fatal("the adapter registered as huawei does not implement providers.LeftoverLister")
	}
}
