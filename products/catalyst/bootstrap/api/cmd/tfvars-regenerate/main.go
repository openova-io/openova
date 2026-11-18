// Command tfvars-regenerate writes the variables file of a deployment again
// from its record, with the engine's own code (work package CLD-094, gate
// check CLD-G12).
//
// Usage: tfvars-regenerate -record <deployment record> -dir <working directory> [-providers <dir>]
//
// The record is the deployment's <id>.json in the engine's deployments
// directory. The directory holds tofu.auto.tfvars.json, the variables file the
// deployment was last applied with (the kept file). The file is written again
// in place, so give the command a copy of the working directory.
//
// The request is the record's: every field that the record holds comes from
// it. A record holds no credential and not every field of the request; those
// fields are read from the kept file, under the key that writeTfvars writes
// each one to, the way the wipe reads the cloud credentials back from it. The
// fields that are not credentials are named in type kept below, with their
// keys. The credentials are not named: a string field that the record does
// not give back is one, and its key is found by writing a probe file with
// writeTfvars (see credentials). The request is then validated, as Provision
// validates it, against the cloud profiles of -providers (the providers
// directory: _contract/cloudprofile.schema.json and <pack>/profile.yaml; by
// default the engine image's), and provisioner.WriteTfvars writes the file. The values that
// writeTfvars reads from the environment (the parent address, the repository
// address, and the branch and the address lists when the kept file has none)
// come from this process's environment: run the command where the engine runs.
//
// Standard output gets one line per top-level key whose value differs from the
// kept file, sorted: "changed <key>", "added <key>" or "removed <key>". No
// value is printed, because the file holds credentials. Exit codes: 0 the file
// was written; 2 usage error, or the record or the kept file cannot be read;
// 3 the engine refused the request or could not write the file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"

	// Validate resolves the provider of the request through the adapter
	// registry, which is empty until the adapters register themselves.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

// tfvarsName is the variables file of a working directory.
const tfvarsName = "tofu.auto.tfvars.json"

// kept holds the request fields that a deployment record does not hold and
// that are not credentials, under the keys of the variables file that
// writeTfvars writes them to. A field of kept that is a string has the name of
// its request field, so that credentials leaves it alone. The test
// TestRoundTrip fails when a field of the request is missing here.
type kept struct {
	BcpTopology         string         `json:"bcp_topology"`
	AdminCIDRs          []string       `json:"admin_cidrs"`
	ConsoleIsolation    string         `json:"console_isolation_enabled"`
	SharedPostgres      string         `json:"enable_shared_pg"`
	QATest              string         `json:"qa_test_session_enabled"`
	QAFixturesNamespace string         `json:"qa_fixtures_namespace"`
	QAOrganization      string         `json:"qa_organization"`
	ClusterMeshName     string         `json:"cluster_mesh_name"`
	ClusterMeshID       int            `json:"cluster_mesh_id"`
	GitopsBranch        string         `json:"gitops_branch"`
	Variables           map[string]any `json:"variables"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "tfvars-regenerate: "+format+"\n", a...)
		return code
	}
	fs := flag.NewFlagSet("tfvars-regenerate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	recordPath := fs.String("record", "", "the deployment record, <id>.json of the deployments directory")
	dir := fs.String("dir", "", "a copy of the deployment's working directory, holding "+tfvarsName)
	packs := fs.String("providers", provisioner.ProvidersDir, "the providers directory whose cloud profiles Validate reads")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *recordPath == "" || *dir == "" || *packs == "" || fs.NArg() != 0 {
		return fail(2, "usage: tfvars-regenerate -record <deployment record> -dir <working directory> [-providers <dir>]")
	}
	provisioner.ProvidersDir = *packs
	var rec store.Record
	if err := readJSON(*recordPath, &rec); err != nil {
		return fail(2, "record: %v", err)
	}
	if strings.TrimSpace(rec.ID) == "" {
		return fail(2, "record: %s has no id", *recordPath)
	}
	path := filepath.Join(*dir, tfvarsName)
	var before map[string]any
	if err := readJSON(path, &before); err != nil {
		return fail(2, "kept file: %v", err)
	}
	var k kept
	if err := readJSON(path, &k); err != nil {
		return fail(2, "kept file: %v", err)
	}
	req := fromRecord(rec)
	fill(&req, k)
	if err := credentials(&req, before); err != nil {
		return fail(3, "find the keys of the credentials: %v", err)
	}
	if err := req.Validate(); err != nil {
		return fail(3, "the engine refuses the request of the record: %v", err)
	}
	if err := provisioner.WriteTfvars(*dir, req); err != nil {
		return fail(3, "write %s: %v", path, err)
	}
	var after map[string]any
	if err := readJSON(path, &after); err != nil {
		return fail(3, "read the written file again: %v", err)
	}
	for _, line := range differences(before, after) {
		_, _ = fmt.Fprintln(stdout, line)
	}
	return 0
}

// fromRecord is the request that a record gives back.
func fromRecord(rec store.Record) provisioner.Request {
	req := rec.Request.ToProvisionerRequest()
	req.DeploymentID = rec.ID
	return req
}

// fill sets on req the fields of type kept, from the kept file.
func fill(req *provisioner.Request, k kept) {
	req.BcpTopology = k.BcpTopology
	req.AdminCIDRs = k.AdminCIDRs
	if k.ConsoleIsolation != "" {
		on := k.ConsoleIsolation == "true"
		req.ConsoleIsolationEnabled = &on
	}
	req.EnableSharedPostgres = k.SharedPostgres == "true"
	req.QATestEnabled = k.QATest == "true"
	req.QAFixturesNamespace, req.QAOrganization = k.QAFixturesNamespace, k.QAOrganization
	req.ClusterMeshName, req.ClusterMeshID = k.ClusterMeshName, k.ClusterMeshID
	req.GitopsBranch = k.GitopsBranch
	if auto, ok := k.Variables["HANDOVER_AUTO"].(string); ok && auto != "" {
		on := auto == "true"
		req.FireCutoverOnHandover = &on
	}
}

// credentials sets on req every credential from the kept file, under the key
// that credentialKeys finds for it. A credential is always the kept file's:
// the record holds none, only the marker of a redacted value. A field that
// writeTfvars does not write for this request keeps the record's value. The
// record's credentials map holds the keys with markers only, so it is dropped:
// Validate builds it again from the fields set here (a marker left in it would
// fill an empty field).
func credentials(req *provisioner.Request, file map[string]any) error {
	req.Credentials = nil
	keys, err := credentialKeys(*req)
	if err != nil {
		return err
	}
	rv := reflect.ValueOf(req).Elem()
	for i, key := range keys {
		s, _ := file[key].(string)
		rv.Field(i).SetString(s)
	}
	return nil
}

// credentialKeys returns, by field index, the key of the variables file that
// writeTfvars writes each credential of req to. The credentials are the
// fields that unheld returns. Each is given a probe value of its own,
// writeTfvars writes a probe file for the request in a temporary directory,
// and the top-level key that holds the probe value of a field is its key (the
// first in sorted order, when several hold it). The probe file holds no
// credential, because a probe value stands in for each one, and it is removed.
func credentialKeys(req provisioner.Request) (map[int]string, error) {
	fields := unheld()
	pv := reflect.ValueOf(&req).Elem()
	for _, i := range fields {
		pv.Field(i).SetString(probeValue(i))
	}
	tmp, err := os.MkdirTemp("", "tfvars-regenerate-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := provisioner.WriteTfvars(tmp, req); err != nil {
		return nil, err
	}
	var written map[string]any
	if err := readJSON(filepath.Join(tmp, tfvarsName), &written); err != nil {
		return nil, err
	}
	keyOf := map[string]string{} // probe value -> key
	for key, value := range written {
		if s, ok := value.(string); ok {
			if old, seen := keyOf[s]; !seen || key < old {
				keyOf[s] = key
			}
		}
	}
	out := map[int]string{}
	for _, i := range fields {
		if key, ok := keyOf[probeValue(i)]; ok {
			out[i] = key
		}
	}
	return out, nil
}

// unheld returns the indexes of the string fields of the request that a
// record does not give back and that type kept does not name: a request with
// each of them set loses the value through store.Redact and fromRecord,
// because the record holds the field redacted or not at all.
func unheld() []int {
	named := map[string]bool{}
	kt := reflect.TypeOf(kept{})
	for i := 0; i < kt.NumField(); i++ {
		named[kt.Field(i).Name] = true
	}
	t := reflect.TypeOf(provisioner.Request{})
	var full provisioner.Request
	fv := reflect.ValueOf(&full).Elem()
	var strs []int
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.IsExported() && f.Type.Kind() == reflect.String && !named[f.Name] {
			fv.Field(i).SetString(probeValue(i))
			strs = append(strs, i)
		}
	}
	back := reflect.ValueOf(fromRecord(store.Record{ID: full.DeploymentID, Request: store.Redact(full)}))
	var out []int
	for _, i := range strs {
		if back.Field(i).String() != fv.Field(i).String() {
			out = append(out, i)
		}
	}
	return out
}

// probeValue is the value that field i of the request carries in a probe.
func probeValue(i int) string {
	return "tfvars-regenerate-probe-" + strconv.Itoa(i)
}

// differences names the top-level keys whose value differs between the kept
// file and the written one, sorted.
func differences(before, after map[string]any) []string {
	var out []string
	for key, value := range after {
		old, ok := before[key]
		switch {
		case !ok:
			out = append(out, "added "+key)
		case !reflect.DeepEqual(old, value):
			out = append(out, "changed "+key)
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			out = append(out, "removed "+key)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.SplitN(out[i], " ", 2)[1] < strings.SplitN(out[j], " ", 2)[1]
	})
	return out
}

func readJSON(path string, into any) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%s is not the expected JSON: %v", path, err)
	}
	return nil
}
