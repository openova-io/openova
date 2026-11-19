package runner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// The files of the task test in the work folder, and the prefix of its refusals.
const (
	fileTargets          = "test.targets"
	fileNucleiSARIF      = "nuclei.sarif.json"
	messageTargetRefused = "target-refused: "
	sarifVersion         = "2.1.0"
	sarifSchema          = "https://json.schemastore.org/sarif-2.1.0.json"
)

// NucleiTags are the only template tags profile network-passive runs: fingerprints of
// services and technologies, and TLS and DNS checks. NucleiExcludedTags are never run,
// even on a template that has one of NucleiTags: templates that change state or deny
// service, fuzzing, password guessing and vulnerability probes.
const (
	NucleiTags         = "tech,detect,ssl,tls,dns"
	NucleiExcludedTags = "intrusive,dos,fuzz,bruteforce,default-login,vuln"
)

var numberRE = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)

// ParsePorts reads --ports: comma-separated whole numbers from 1 to 65535, each once.
func ParsePorts(s string) ([]int, bool) {
	var ports []int
	for _, p := range splitKeys(s) {
		n, err := strconv.Atoi(p)
		if !numberRE.MatchString(p) || err != nil || n > 65535 || containsInt(ports, n) {
			return nil, false
		}
		ports = append(ports, n)
	}
	return ports, len(ports) > 0
}

// parseRate reads --requests-per-second; an empty value is the default.
func parseRate(s string) (int, bool) {
	if s == "" {
		return DefaultRequestsPerSec, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && numberRE.MatchString(s) && n <= MaxRequestsPerSec
}

// eachOnce reports whether every value is in allowed and none is given twice.
func eachOnce(values, allowed []string) bool {
	for i, v := range values {
		if !contains(allowed, v) || contains(values[:i], v) {
			return false
		}
	}
	return true
}

// Targets returns the target list of a test: one line <address>:<port> per port, in the
// order of ports, an IPv6 address in brackets. Nothing else is ever a target. It refuses
// an address that is not one unicast address a machine can hold (unspecified, loopback,
// multicast, link-local, broadcast, an address with a zone) and a port outside 1 to 65535
// or given twice.
func Targets(address string, ports []int) ([]string, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" {
		return nil, fmt.Errorf("%q is not one IP address", address)
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return nil, fmt.Errorf("%s is not the unicast address of a machine", ip)
	}
	if len(ports) == 0 {
		return nil, errors.New("no port")
	}
	var out []string
	for i, p := range ports {
		if p < 1 || p > 65535 || containsInt(ports[:i], p) {
			return nil, fmt.Errorf("port %d is not a port from 1 to 65535 given once", p)
		}
		out = append(out, netip.AddrPortFrom(ip, uint16(p)).String()) //nolint:gosec // p is from 1 to 65535, checked above
	}
	return out, nil
}

// CheckTargets refuses a target list that holds anything but the targets of address and
// ports, each at most once: another address, another port, a name, a URL, a range, an
// option or an empty line. The task test checks the file the tool reads with it.
func CheckTargets(lines []string, address string, ports []int) error {
	allowed, err := Targets(address, ports)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return errors.New("the target list is empty")
	}
	for i, l := range lines {
		if !contains(allowed, l) || contains(lines[:i], l) {
			return fmt.Errorf("%q is not a declared target", l)
		}
	}
	return nil
}

// NucleiArgs are the arguments of profile network-passive: the templates of the image only,
// with no update check and no download; signed templates only; protocols tcp, ssl, dns and
// http, so no code or headless template, and no -dast, so no fuzzing; the tags of
// NucleiTags without NucleiExcludedTags; no out-of-band interaction server and no HTTP
// redirect away from the target; the rate limit; and the findings as SARIF.
func NucleiArgs(templates, targets, sarif string, rate int) []string {
	return []string{"-duc", "-t", templates, "-l", targets, "-pt", "tcp,ssl,dns,http", "-dut",
		"-tags", NucleiTags, "-etags", NucleiExcludedTags, "-ni", "-dr",
		"-rl", strconv.Itoa(rate), "-se", sarif, "-silent", "-nc"}
}

// zapBaseline is the ZAP baseline script of the image. With IS_CONTAINERIZED=true it writes
// its reports into /zap/wrk, which the image links to the work folder. A test replaces it.
var zapBaseline = "/zap/zap-baseline.py"

// probeTimeout bounds the TLS handshake that chooses the scheme of a port.
const probeTimeout = 5 * time.Second

// ZAPArgs are the arguments of one run of profile web-passive: the target URL, the JSON
// report in /zap/wrk, the spider capped at one minute, and the ZAP options -silent (no
// update check and no add-on download, so nothing is fetched at run time) and -dir (the
// isolated home of this run). The scan has no active-scan, fuzzing or attack step.
func ZAPArgs(url, report, home string) []string {
	return []string{"-t", url, "-J", report, "-m", "1", "-z", "-silent -dir " + home}
}

// WebURL returns the URL ZAP scans for one line <address>:<port> of the checked target
// list: https on port 443, http on port 80, and on any other port https when a TLS
// handshake with the target completes, whatever the certificate, else http.
func WebURL(ctx context.Context, target string, port int, handshake func(context.Context, string) bool) string {
	scheme := "http"
	if port == 443 || port != 80 && handshake(ctx, target) {
		scheme = "https"
	}
	return scheme + "://" + target + "/"
}

// TLSHandshake reports whether a TLS handshake with target completes within probeTimeout,
// whatever the certificate. It sends no request and closes the connection.
func TLSHandshake(ctx context.Context, target string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // the probe asks only whether the port speaks TLS; no request is sent
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// test runs the profiles of the scope against the target and writes test.sarif.json, one
// SARIF run per tool run, in the order of the profiles. It opens no SSH session. The target
// list is written, read back and checked before any tool starts, and each tool reads only
// the lines of that checked list.
func test(ctx context.Context, cfg Config, env Env) taskResult {
	for _, p := range cfg.TestProfiles {
		if !contains(testProfiles, p) {
			return refused(messageNotSupported + "test-profile " + p)
		}
	}
	targets, err := Targets(cfg.Address, cfg.Ports)
	if err != nil {
		return refused(messageTargetRefused + err.Error())
	}
	if info, err := os.Stat(env.Templates); contains(cfg.TestProfiles, ProfileNetworkPassive) && (err != nil || !info.IsDir()) {
		return failed("nuclei-templates-not-in-image: %s", env.Templates)
	}
	if _, err := os.Stat(zapBaseline); contains(cfg.TestProfiles, ProfileWebPassive) && err != nil {
		return failed("zap-not-in-image: %s", zapBaseline)
	}
	list := filepath.Join(env.WorkDir, fileTargets)
	if err := os.WriteFile(list, []byte(strings.Join(targets, "\n")+"\n"), 0o600); err != nil {
		return failed("the target list was not written: %v", err)
	}
	data, err := os.ReadFile(list) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return failed("the target list cannot be read: %v", err)
	}
	checked := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if err := CheckTargets(checked, cfg.Address, cfg.Ports); err != nil {
		return refused(messageTargetRefused + err.Error())
	}
	var runs []json.RawMessage
	findings := 0
	for _, p := range cfg.TestProfiles {
		var r []json.RawMessage
		var n int
		var bad *taskResult
		if p == ProfileNetworkPassive {
			r, n, bad = nuclei(ctx, cfg, env, list)
		} else {
			r, n, bad = zap(ctx, env, checked)
		}
		if bad != nil {
			return *bad
		}
		runs, findings = append(runs, r...), findings+n
	}
	out := map[string]any{"version": sarifVersion, "$schema": sarifSchema, "runs": runs}
	if err := writeJSON(filepath.Join(env.WorkDir, FileTestSARIF), out); err != nil {
		return failed("%s was not written: %v", FileTestSARIF, err)
	}
	if err := writeMarker(env, TaskTest); err != nil {
		return failed("the completion marker was not written: %v", err)
	}
	return taskResult{outcome: runresult.Succeeded, test: &runresult.Test{Findings: findings},
		files: []file{{KindTestSARIF, FileTestSARIF}}}
}

// nuclei runs profile network-passive on the checked target list and returns its runs.
func nuclei(ctx context.Context, cfg Config, env Env, list string) ([]json.RawMessage, int, *taskResult) {
	sarif := filepath.Join(env.WorkDir, fileNucleiSARIF)
	if err := os.Remove(sarif); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, 0, ptr(failed("%s cannot be removed: %v", fileNucleiSARIF, err))
	}
	if r := collect(ctx, nil, []int{0}, env.Nuclei, NucleiArgs(env.Templates, list, sarif, cfg.RequestsPerSec)...); r != nil {
		return nil, 0, r
	}
	data, err := os.ReadFile(sarif) //nolint:gosec // a fixed name in the work folder
	if errors.Is(err, os.ErrNotExist) {
		// Nuclei writes no file when it found nothing: that is one run without results.
		return []json.RawMessage{json.RawMessage(`{"tool":{"driver":{"name":"nuclei"}},"results":[]}`)}, 0, nil
	}
	if err != nil {
		return nil, 0, ptr(failed("%s cannot be read: %v", fileNucleiSARIF, err))
	}
	runs, n, err := ParseSARIF(data)
	if err != nil {
		return nil, 0, ptr(failed("%s cannot be read: %v", fileNucleiSARIF, err))
	}
	return runs, n, nil
}

// zap runs profile web-passive: the ZAP baseline scan once per line of the checked target
// list, each with its own home, and converts each JSON report into one SARIF run. Exit
// codes 0, 1 and 2 are results (no warning, a FAIL-level alert, a WARN-level alert); any
// other exit, for example 3 when the scan could not run, is a failure, whose message adds
// the end of the script's standard output, where the script says why.
func zap(ctx context.Context, env Env, checked []string) ([]json.RawMessage, int, *taskResult) {
	var runs []json.RawMessage
	findings := 0
	for _, target := range checked {
		port := int(netip.MustParseAddrPort(target).Port())
		home := filepath.Join(env.WorkDir, fmt.Sprintf("zap-%d", port))
		report := fmt.Sprintf("zap-%d.json", port)
		if err := os.MkdirAll(home, 0o700); err != nil {
			return nil, 0, ptr(failed("the home of ZAP was not created: %v", err))
		}
		if err := os.Remove(filepath.Join(env.WorkDir, report)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, 0, ptr(failed("%s cannot be removed: %v", report, err))
		}
		url := WebURL(ctx, target, port, TLSHandshake)
		var out tail
		if r := collectTo(ctx, &out, []string{"IS_CONTAINERIZED=true", "HOME=" + home}, []int{0, 1, 2}, zapBaseline, ZAPArgs(url, report, home)...); r != nil {
			if s := strings.TrimSpace(out.String()); r.outcome == runresult.Failed && s != "" {
				r.message += "; " + s
			}
			return nil, 0, r
		}
		data, err := os.ReadFile(filepath.Join(env.WorkDir, report)) //nolint:gosec // a fixed name in the work folder
		if err != nil {
			return nil, 0, ptr(failed("%s cannot be read: %v", report, err))
		}
		run, n, err := ZAPRun(data, url)
		if err != nil {
			return nil, 0, ptr(failed("%s cannot be read: %v", report, err))
		}
		runs, findings = append(runs, run), findings+n
	}
	return runs, findings, nil
}

func ptr(t taskResult) *taskResult { return &t }

// ParseSARIF returns the runs of a SARIF 2.1.0 log and the number of their results.
func ParseSARIF(data []byte) ([]json.RawMessage, int, error) {
	var log struct {
		Version string            `json:"version"`
		Runs    []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, 0, err
	}
	if log.Version != sarifVersion || len(log.Runs) == 0 {
		return nil, 0, fmt.Errorf("not a SARIF %s log with a run", sarifVersion)
	}
	n := 0
	for _, r := range log.Runs {
		var run struct {
			Results *[]json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(r, &run); err != nil || run.Results == nil {
			return nil, 0, errors.New("a run without results")
		}
		n += len(*run.Results)
	}
	return log.Runs, n, nil
}
