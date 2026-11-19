package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// writeSARIF is a fake nuclei that writes a SARIF log with two results to its -se file.
const writeSARIF = `for a in "$@"; do [ "$prev" = "-se" ] && out="$a"; prev="$a"; done
printf '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Nuclei"}},"results":[{"ruleId":"tls-version"},{"ruleId":"apache-detect"}]}]}\n' > "$out"`

// writeZAPReport is a fake ZAP baseline script, with the work folder for its verb: it
// appends its arguments, HOME and IS_CONTAINERIZED to <script>.calls, writes a report with
// one Medium alert at the target URL to the -J file in the work folder, and exits 2.
const writeZAPReport = `printf '%%s\n' "$@" "HOME=$HOME" "IS_CONTAINERIZED=$IS_CONTAINERIZED" -- >> "$0.calls"
for a in "$@"; do [ "$prev" = "-J" ] && out="$a"; [ "$prev" = "-t" ] && url="$a"; prev="$a"; done
printf '{"@programName":"ZAP","@version":"2.17.0","site":[{"alerts":[{"pluginid":"10038","alertRef":"10038-1","name":"CSP Header Not Set","riskcode":"2","confidence":"3","instances":[{"uri":"%%s","method":"GET"}]}]}]}\n' "$url" > '%s'/"$out"
exit 2`

// testTaskEnv is testEnv with a templates folder, and a nuclei and a ZAP baseline script
// that fail when they run.
func testTaskEnv(t *testing.T, s3 *fakeS3) Env {
	env := testEnv(t, s3)
	env.Nuclei = filepath.Join(filepath.Dir(env.Trivy), "nuclei")
	env.Templates = filepath.Join(env.ContentDir, "nuclei-templates")
	must(t, os.MkdirAll(env.Templates, 0o750))
	fakeProgram(t, env.Nuclei, "echo nuclei must not run >&2; exit 99")
	saved := zapBaseline
	zapBaseline = filepath.Join(filepath.Dir(env.Trivy), "zap-baseline.py")
	t.Cleanup(func() { zapBaseline = saved })
	fakeProgram(t, zapBaseline, "echo zap must not run >&2; exit 99")
	return env
}

// testConfig is a valid run of the task test on 10.0.0.5, ports 80 and 443, run test-1.
func testConfig(s3 *fakeS3) Config {
	c := config(TaskTest, s3)
	c.Mode, c.Address, c.Run.Name, c.Evidence.Prefix = ModeSSH, "10.0.0.5", "test-1", "acme/ledger-db-1/test-1/"
	c.TestProfiles, c.Ports, c.RequestsPerSec = []string{ProfileNetworkPassive}, []int{80, 443}, 10
	return c
}

func testArgs(endpoint string, extra ...string) []string {
	return append([]string{"--task=test", "--mode=ssh", "--machine=acme/ledger-db-1", "--run=machine-ops/test-1",
		"--os-family=ubuntu", "--os-version=24.04", "--evidence-endpoint=" + endpoint, "--evidence-region=us-east-1",
		"--evidence-bucket=machine-evidence", "--evidence-prefix=acme/ledger-db-1/test-1/"}, extra...)
}

func TestNetworkPassiveRunsNucleiWithTheExactArguments(t *testing.T) {
	s3 := newFakeS3(t)
	env := testTaskEnv(t, s3)
	fakeProgram(t, env.Nuclei, writeSARIF)
	cfg := testConfig(s3)
	cfg.RequestsPerSec = 25
	doc := Run(context.Background(), cfg, env)
	key := "acme/ledger-db-1/test-1/" + FileTestSARIF
	if doc.Outcome != runresult.Succeeded || !doc.Completed || doc.Test == nil || *doc.Test != (runresult.Test{Findings: 2, ResultsKey: key}) {
		t.Fatalf("got %+v, want Succeeded with two findings under %s", doc, key)
	}
	list := filepath.Join(env.WorkDir, "test.targets")
	want := []string{"-duc", "-t", env.Templates, "-l", list, "-pt", "tcp,ssl,dns,http", "-dut",
		"-tags", "tech,detect,ssl,tls,dns", "-etags", "intrusive,dos,fuzz,bruteforce,default-login,vuln", "-ni", "-dr",
		"-rl", "25", "-se", filepath.Join(env.WorkDir, "nuclei.sarif.json"), "-silent", "-nc"}
	if got := args(t, env.Nuclei); !reflect.DeepEqual(got, want) {
		t.Fatalf("nuclei arguments %q, want %q", got, want)
	}
	if data, err := os.ReadFile(list); err != nil || string(data) != "10.0.0.5:80\n10.0.0.5:443\n" { //nolint:gosec // a file of the test folder
		t.Fatalf("target list %q %v", data, err)
	}
	if len(doc.Evidence) != 1 || doc.Evidence[0].Kind != KindTestSARIF || doc.Evidence[0].Key != key {
		t.Fatalf("evidence %+v", doc.Evidence)
	}
	if runs, n, err := ParseSARIF(s3.objects["/machine-evidence/"+key]); err != nil || len(runs) != 1 || n != 2 {
		t.Fatalf("stored %d runs with %d results: %v", len(runs), n, err)
	}
}

func TestNetworkPassiveWithoutFindingsStoresOneEmptyRun(t *testing.T) {
	s3 := newFakeS3(t)
	env := testTaskEnv(t, s3)
	fakeProgram(t, env.Nuclei, "exit 0")
	doc := Run(context.Background(), testConfig(s3), env)
	if doc.Outcome != runresult.Succeeded || doc.Test == nil || doc.Test.Findings != 0 || doc.Test.ResultsKey == "" {
		t.Fatalf("got %+v, want Succeeded with no finding", doc)
	}
	if runs, n, err := ParseSARIF(s3.objects["/machine-evidence/"+doc.Test.ResultsKey]); err != nil || len(runs) != 1 || n != 0 {
		t.Fatalf("stored %d runs with %d results: %v", len(runs), n, err)
	}
}

func TestWebPassiveRunsZAPOncePerPortBesideNuclei(t *testing.T) {
	s3 := newFakeS3(t)
	env := testTaskEnv(t, s3)
	fakeProgram(t, env.Nuclei, writeSARIF)
	fakeProgram(t, zapBaseline, fmt.Sprintf(writeZAPReport, env.WorkDir))
	cfg := testConfig(s3)
	cfg.TestProfiles = []string{ProfileNetworkPassive, ProfileWebPassive}
	doc := Run(context.Background(), cfg, env)
	if doc.Outcome != runresult.Succeeded || !doc.Completed || doc.Test == nil || doc.Test.Findings != 4 {
		t.Fatalf("got %+v, want Succeeded with two Nuclei and two ZAP findings", doc)
	}
	h80, h443 := filepath.Join(env.WorkDir, "zap-80"), filepath.Join(env.WorkDir, "zap-443")
	want := strings.Join([]string{"-t", "http://10.0.0.5:80/", "-J", "zap-80.json", "-m", "1", "-z", "-silent -dir " + h80, "HOME=" + h80, "IS_CONTAINERIZED=true", "--",
		"-t", "https://10.0.0.5:443/", "-J", "zap-443.json", "-m", "1", "-z", "-silent -dir " + h443, "HOME=" + h443, "IS_CONTAINERIZED=true", "--", ""}, "\n")
	if data, err := os.ReadFile(zapBaseline + ".calls"); err != nil || string(data) != want { //nolint:gosec // a file of the test folder
		t.Fatalf("zap runs %q, want %q", data, want)
	}
	runs, n, err := ParseSARIF(s3.objects["/machine-evidence/"+doc.Test.ResultsKey])
	if err != nil || len(runs) != 3 || n != 4 {
		t.Fatalf("stored %d runs with %d results: %v", len(runs), n, err)
	}
	var got []string
	for _, raw := range runs {
		var r parsedRun
		must(t, json.Unmarshal(raw, &r))
		got = append(got, fmt.Sprintf("%s %s %d", r.Tool.Driver.Name, r.Properties["target"], len(r.Results)))
	}
	if want := []string{"Nuclei  2", "ZAP http://10.0.0.5:80/ 1", "ZAP https://10.0.0.5:443/ 1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored runs %q, want %q", got, want)
	}
}

func TestWebURLProbesOnlyAPortOtherThan80And443(t *testing.T) {
	var probed []string
	probe := func(_ context.Context, target string) bool {
		probed = append(probed, target)
		return !strings.HasSuffix(target, ":8080")
	}
	ctx := context.Background()
	for target, want := range map[string]string{"10.0.0.5:80": "http://10.0.0.5:80/", "10.0.0.5:443": "https://10.0.0.5:443/",
		"10.0.0.5:8443": "https://10.0.0.5:8443/", "[2001:db8::5]:8080": "http://[2001:db8::5]:8080/"} {
		if got := WebURL(ctx, target, int(netip.MustParseAddrPort(target).Port()), probe); got != want {
			t.Errorf("%s: got %s, want %s", target, got, want)
		}
	}
	if sort.Strings(probed); !reflect.DeepEqual(probed, []string{"10.0.0.5:8443", "[2001:db8::5]:8080"}) {
		t.Errorf("probed %q", probed)
	}
	tlsServer, plain := httptest.NewTLSServer(nil), httptest.NewServer(nil)
	defer tlsServer.Close()
	defer plain.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	must(t, closed.Close())
	if !TLSHandshake(ctx, tlsServer.Listener.Addr().String()) || TLSHandshake(ctx, plain.Listener.Addr().String()) || TLSHandshake(ctx, closed.Addr().String()) {
		t.Errorf("the handshake must complete only with the TLS server, whatever its certificate")
	}
}

func TestZAPFailureOrABadReportStoresNothing(t *testing.T) {
	for want, body := range map[string]string{
		"zap-baseline.py exited 3: no summary; Job spider failed": "echo Job spider failed; echo no summary >&2; exit 3",
		"zap-80.json cannot be read":                              "exit 0",
		"zap-80.json cannot be read: not a ZAP":                   `for a in "$@"; do [ "$prev" = "-J" ] && out="$a"; prev="$a"; done; echo '{"@programName":"X","site":[]}' > '%s'/"$out"`,
	} {
		s3 := newFakeS3(t)
		env := testTaskEnv(t, s3)
		if strings.Contains(body, "%s") {
			body = fmt.Sprintf(body, env.WorkDir)
		}
		fakeProgram(t, zapBaseline, body)
		cfg := testConfig(s3)
		cfg.TestProfiles = []string{ProfileWebPassive}
		doc := Run(context.Background(), cfg, env)
		if doc.Outcome != runresult.Failed || !strings.HasPrefix(doc.Message, want) || doc.Test != nil || s3.requests() != 0 {
			t.Errorf("got %+v and %d requests, want Failed %q and nothing stored", doc, s3.requests(), want)
		}
	}
	env := testTaskEnv(t, nil)
	must(t, os.Remove(zapBaseline))
	cfg := testConfig(nil)
	cfg.TestProfiles = []string{ProfileWebPassive}
	if doc := Run(context.Background(), cfg, env); doc.Outcome != runresult.Failed || !strings.HasPrefix(doc.Message, "zap-not-in-image") {
		t.Errorf("without ZAP: %+v", doc)
	}
}

func TestATargetOutsideTheDeclaredSetIsRefusedBeforeTheToolStarts(t *testing.T) {
	for _, c := range []struct{ address, ports, message string }{
		{"ledger-db-1.acme.internal", "443", "invalid-arguments: --address must be an IP address"},
		{"https://10.0.0.5", "443", "invalid-arguments: --address must be an IP address"},
		{"10.0.0.5:443", "443", "invalid-arguments: --address must be an IP address"},
		{"10.0.0.0/24", "443", "invalid-arguments: --address must be an IP address"},
		{"10.0.0.5,10.0.0.6", "443", "invalid-arguments: --address must be an IP address"},
		{"10.0.0.5\n10.0.0.6", "443", "invalid-arguments: --address must be an IP address"},
		{"", "443", "invalid-arguments: --address is required for the task test"},
		{"127.0.0.1", "443", "target-refused: 127.0.0.1 is not the unicast address of a machine"},
		{"0.0.0.0", "443", "target-refused: 0.0.0.0 is not the unicast address of a machine"},
		{"169.254.169.254", "80", "target-refused: 169.254.169.254 is not the unicast address of a machine"},
		{"fe80::1", "443", "target-refused: fe80::1 is not the unicast address of a machine"},
		{"224.0.0.1", "443", "target-refused: 224.0.0.1 is not the unicast address of a machine"},
		{"255.255.255.255", "443", "target-refused: 255.255.255.255 is not the unicast address of a machine"},
		{"10.0.0.5", "", "invalid-arguments: --ports must be"},
		{"10.0.0.5", "443,443", "invalid-arguments: --ports must be"},
		{"10.0.0.5", "0", "invalid-arguments: --ports must be"},
		{"10.0.0.5", "65536", "invalid-arguments: --ports must be"},
		{"10.0.0.5", "1-1024", "invalid-arguments: --ports must be"},
		{"10.0.0.5", "443 -t /tmp", "invalid-arguments: --ports must be"},
	} {
		for _, profiles := range []string{"network-passive", "web-passive", "web-passive,network-passive"} {
			s3 := newFakeS3(t)
			env := testTaskEnv(t, s3)
			var stdout, stderr bytes.Buffer
			code := Main(context.Background(), testArgs(s3.srv.URL, "--address="+c.address, "--ports="+c.ports, "--test-profile="+profiles), env, &stdout, &stderr)
			doc := runresult.OrNoResult(bytes.TrimSpace(stdout.Bytes()))
			if code != 1 || doc.Outcome != runresult.Refused || !strings.HasPrefix(doc.Message, c.message) || doc.Completed {
				t.Errorf("%s: address %q ports %q: got %d %+v, want Refused %q", profiles, c.address, c.ports, code, doc, c.message)
			}
			if args(t, env.Nuclei) != nil || args(t, zapBaseline) != nil || s3.requests() != 0 {
				t.Errorf("%s: address %q ports %q: a tool ran or the bucket was called", profiles, c.address, c.ports)
			}
		}
	}
}

func TestCheckTargetsRefusesEveryLineOutsideTheDeclaredSet(t *testing.T) {
	ports := []int{80, 443}
	if err := CheckTargets([]string{"10.0.0.5:80", "10.0.0.5:443"}, "10.0.0.5", ports); err != nil {
		t.Fatalf("the declared targets were refused: %v", err)
	}
	if got, err := Targets("2001:db8::5", []int{443}); err != nil || !reflect.DeepEqual(got, []string{"[2001:db8::5]:443"}) {
		t.Fatalf("IPv6 targets %q %v", got, err)
	}
	for _, lines := range [][]string{
		{"10.0.0.6:443"}, {"10.0.0.5:22"}, {"10.0.0.5:443", "10.0.0.6:443"}, {"10.0.0.5:443", "10.0.0.5:443"},
		{"ledger-db-1.acme.internal:443"}, {"https://10.0.0.5:443"}, {"10.0.0.0/24"}, {"10.0.0.5"},
		{"10.0.0.5:443 -t /tmp/x"}, {""}, {},
	} {
		if err := CheckTargets(lines, "10.0.0.5", ports); err == nil {
			t.Errorf("target list %q was accepted", lines)
		}
	}
}

func TestTestRefusesNodeModeAndAnUnknownProfile(t *testing.T) {
	node := testConfig(nil)
	node.Mode, node.TestProfiles = ModeNode, []string{ProfileWebPassive}
	active := testConfig(nil)
	active.TestProfiles = []string{ProfileWebPassive, "web-active"}
	for want, cfg := range map[string]Config{"not-supported: mode node": node, "not-supported: test-profile web-active": active} {
		env := testTaskEnv(t, nil)
		if doc := Run(context.Background(), cfg, env); doc.Outcome != runresult.Refused || doc.Message != want || doc.Completed {
			t.Errorf("got %+v, want Refused %q", doc, want)
		}
		if args(t, env.Nuclei) != nil || args(t, zapBaseline) != nil {
			t.Errorf("%s: a tool ran", want)
		}
	}
}

func TestNucleiFailureOrABadReportStoresNothing(t *testing.T) {
	for want, body := range map[string]string{
		"nuclei exited 1: no templates":    "echo no templates >&2; exit 1",
		"nuclei.sarif.json cannot be read": `for a in "$@"; do [ "$prev" = "-se" ] && out="$a"; prev="$a"; done; echo '{"version":"2.0.0"}' > "$out"`,
	} {
		s3 := newFakeS3(t)
		env := testTaskEnv(t, s3)
		fakeProgram(t, env.Nuclei, body)
		doc := Run(context.Background(), testConfig(s3), env)
		if doc.Outcome != runresult.Failed || !strings.HasPrefix(doc.Message, want) || doc.Test != nil || s3.requests() != 0 {
			t.Errorf("got %+v and %d requests, want Failed %q and nothing stored", doc, s3.requests(), want)
		}
	}
	env := testTaskEnv(t, nil)
	must(t, os.Remove(env.Templates))
	if doc := Run(context.Background(), testConfig(nil), env); doc.Outcome != runresult.Failed || !strings.HasPrefix(doc.Message, "nuclei-templates-not-in-image") {
		t.Errorf("without templates: %+v", doc)
	}
}

func TestParseArgsReadsATestRun(t *testing.T) {
	got, err := ParseArgs(testArgs("https://s3.example.org", "--address=10.0.0.5", "--ports=443,80", "--test-profile=network-passive"))
	if err != nil || got.RequestsPerSec != 10 || !reflect.DeepEqual(got.Ports, []int{443, 80}) || !reflect.DeepEqual(got.TestProfiles, []string{"network-passive"}) {
		t.Fatalf("got %+v %v", got, err)
	}
	for _, extra := range [][]string{
		{"--requests-per-second=0"}, {"--requests-per-second=51"}, {"--requests-per-second=ten"},
		{"--test-profile=network-passive,network-passive"}, {"--test-profile=network-active"},
	} {
		a := append(testArgs("https://s3.example.org", "--address=10.0.0.5", "--ports=443", "--test-profile=network-passive"), extra...)
		if _, err := ParseArgs(a); err == nil {
			t.Errorf("%q was accepted", extra)
		}
	}
	if _, err := ParseArgs(nodeArgs(TaskInventory, "--ports=443")); err == nil || !strings.Contains(err.Error(), "belong to the task test") {
		t.Errorf("--ports on the task inventory: %v", err)
	}
}
