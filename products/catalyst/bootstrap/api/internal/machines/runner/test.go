package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

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

// test runs the profiles of the scope against the target and writes test.sarif.json, one
// SARIF run per tool run. It opens no SSH session. Profile web-passive is refused in this
// version. The target list is written, read back and checked before the tool starts.
func test(ctx context.Context, cfg Config, env Env) taskResult {
	for _, p := range cfg.TestProfiles {
		if p != ProfileNetworkPassive {
			return refused(messageNotSupported + "test-profile " + p)
		}
	}
	targets, err := Targets(cfg.Address, cfg.Ports)
	if err != nil {
		return refused(messageTargetRefused + err.Error())
	}
	if info, err := os.Stat(env.Templates); err != nil || !info.IsDir() {
		return failed("nuclei-templates-not-in-image: %s", env.Templates)
	}
	list := filepath.Join(env.WorkDir, fileTargets)
	if err := os.WriteFile(list, []byte(strings.Join(targets, "\n")+"\n"), 0o600); err != nil {
		return failed("the target list was not written: %v", err)
	}
	data, err := os.ReadFile(list) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return failed("the target list cannot be read: %v", err)
	}
	if err := CheckTargets(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), cfg.Address, cfg.Ports); err != nil {
		return refused(messageTargetRefused + err.Error())
	}
	sarif := filepath.Join(env.WorkDir, fileNucleiSARIF)
	if err := os.Remove(sarif); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failed("%s cannot be removed: %v", fileNucleiSARIF, err)
	}
	if r := collect(ctx, nil, []int{0}, env.Nuclei, NucleiArgs(env.Templates, list, sarif, cfg.RequestsPerSec)...); r != nil {
		return *r
	}
	// Nuclei writes no file when it found nothing: that is one run without results.
	runs, findings := []json.RawMessage{json.RawMessage(`{"tool":{"driver":{"name":"nuclei"}},"results":[]}`)}, 0
	if data, err := os.ReadFile(sarif); err == nil { //nolint:gosec // a fixed name in the work folder
		if runs, findings, err = ParseSARIF(data); err != nil {
			return failed("%s cannot be read: %v", fileNucleiSARIF, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return failed("%s cannot be read: %v", fileNucleiSARIF, err)
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
