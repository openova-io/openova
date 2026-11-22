package provisioner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// fakeTofu puts a tofu on PATH that appends its arguments, one call a line,
// to the returned log, and fails `tofu import` of an id that starts with
// "bad-".
func fakeTofu(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$1 $5\" in 'import bad-'*) echo 'Error: Cannot import non-existent remote object' >&2; exit 1 ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tofu"), []byte(script), 0o700); err != nil { //nolint:gosec // a fake program must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(log))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// CLD-G12: the address the adapter chose before the plan is handed to
// OpenTofu with `tofu import`, so the state names it; an address the state
// holds already is not imported again; the result names what was imported.
func TestImportPreCreated_HandsTheNewAddressToOpenTofu(t *testing.T) {
	log := fakeTofu(t)
	imports := []providers.StateImport{
		{Address: `huaweicloud_vpc_eip.nat["me-east-215-a"]`, ID: "8fedaa", Label: "212.72.24.24"},
		{Address: `huaweicloud_vpc_eip.nat["me-east-215-b"]`, ID: "77b1c0", Label: "212.72.24.31"},
	}
	inState := map[string]bool{`huaweicloud_vpc_eip.nat["me-east-215-b"]`: true}
	var events []string
	owned, err := (&Provisioner{}).importPreCreated(context.Background(), t.TempDir(), imports, inState, func(_, level, msg string) {
		events = append(events, level+" "+msg)
	})
	if err != nil {
		t.Fatalf("importPreCreated: %v (events %q)", err, events)
	}
	if want := imports[:1]; !reflect.DeepEqual(owned, want) {
		t.Errorf("owned = %+v; want %+v", owned, want)
	}
	if want := []string{`import -input=false -no-color huaweicloud_vpc_eip.nat["me-east-215-a"] 8fedaa`}; !reflect.DeepEqual(calls(t, log), want) {
		t.Errorf("tofu calls = %q; want %q", calls(t, log), want)
	}
	if !strings.Contains(strings.Join(events, "\n"), "212.72.24.24") {
		t.Errorf("events %q do not name the imported address", events)
	}
}

// A failed import fails the prov, before any plan.
func TestImportPreCreated_AFailedImportFailsTheProv(t *testing.T) {
	fakeTofu(t)
	_, err := (&Provisioner{}).importPreCreated(context.Background(), t.TempDir(),
		[]providers.StateImport{{Address: `x.y["a"]`, ID: "bad-1", Label: "203.0.113.1"}}, nil, func(string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), `tofu import x.y["a"] bad-1`) {
		t.Fatalf("err = %v; want the failed import named", err)
	}
	if _, err := (&Provisioner{}).importPreCreated(context.Background(), t.TempDir(),
		[]providers.StateImport{{Address: `x.y["a"]`}}, nil, func(string, string, string) {}); err == nil {
		t.Fatal("an import without an id was accepted")
	}
}

// stateAddresses reads the managed resource instances of the local state,
// with the index keys tofu prints; no state is an empty set; a state that
// cannot be decoded is an error.
func TestStateAddresses(t *testing.T) {
	dir := t.TempDir()
	got, err := stateAddresses(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("no state: %v, %v; want an empty set", got, err)
	}
	state := `{"version":4,"resources":[
	  {"mode":"managed","type":"huaweicloud_vpc_eip","name":"nat","instances":[{"index_key":"me-east-215-a"},{"index_key":"me-east-215-b"}]},
	  {"mode":"managed","type":"huaweicloud_compute_instance","name":"control_plane","instances":[{"index_key":0}]},
	  {"mode":"managed","type":"huaweicloud_vpc_eip","name":"elb_primary","instances":[{}]},
	  {"module":"module.seal","mode":"managed","type":"terraform_data","name":"key","instances":[{}]},
	  {"mode":"data","type":"huaweicloud_availability_zones","name":"zones","instances":[{}]}]}`
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = stateAddresses(dir)
	want := map[string]bool{
		`huaweicloud_vpc_eip.nat["me-east-215-a"]`: true, `huaweicloud_vpc_eip.nat["me-east-215-b"]`: true,
		`huaweicloud_compute_instance.control_plane[0]`: true, `huaweicloud_vpc_eip.elb_primary`: true,
		`module.seal.terraform_data.key`: true,
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("stateAddresses = %v, %v; want %v", got, err, want)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stateAddresses(dir); err == nil {
		t.Error("a state that cannot be decoded gave no error")
	}
}
