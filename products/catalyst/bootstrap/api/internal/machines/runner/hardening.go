package runner

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// HardeningResultsSchema is the schema of the file hardening.results.json.
const HardeningResultsSchema = "openova.io/machine-hardening-results/v1"

// HardeningResults is the file hardening.results.json: the result of every rule the
// evaluation selected, and the four counts of the result document. Reason is
// no-profile-for-release when the image has no data stream for the target's release;
// DataStream and Rules are then empty.
type HardeningResults struct {
	Schema         string       `json:"schema"`
	ProfileID      string       `json:"profileId"`
	ContentRelease string       `json:"contentRelease"`
	DataStream     string       `json:"dataStream"`
	Reason         string       `json:"reason"`
	Counts         Counts       `json:"counts"`
	Rules          []RuleResult `json:"rules"`
}

// Counts are the rule results by class.
type Counts struct {
	Pass          int `json:"pass"`
	Fail          int `json:"fail"`
	NotApplicable int `json:"notApplicable"`
	Error         int `json:"error"`
}

// RuleResult is the XCCDF result of one rule.
type RuleResult struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

// dataStreamProducts maps an operating-system family to the product prefix of the
// ComplianceAsCode data stream files, ssg-<prefix><version>-ds.xml. Ubuntu takes its
// version without the dot (24.04 gives 2404); every other family its major version.
// A family that is not listed has no data stream.
var dataStreamProducts = map[string]string{
	"ubuntu":      "ubuntu",
	"rhel":        "rhel",
	"almalinux":   "almalinux",
	"oraclelinux": "ol",
}

// DataStream returns the file name of the data stream for an operating-system family and
// version, and false when the family has none.
func DataStream(family, version string) (string, bool) {
	product, ok := dataStreamProducts[family]
	if !ok {
		return "", false
	}
	parts := strings.Split(version, ".")
	if family == "ubuntu" {
		if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
			return "", false
		}
		return "ssg-" + product + parts[0] + parts[1] + "-ds.xml", true
	}
	return "ssg-" + product + parts[0] + "-ds.xml", true
}

// TailoredProfileID is the identifier of the profile a tailoring file derives from the
// profile it extends.
func TailoredProfileID(profileID string) string { return profileID + "_openova_tailored" }

// hardening evaluates one profile against the node's file system with oscap-chroot and
// writes hardening.arf.xml and hardening.results.json. A release of the content that the
// image does not hold is refused. A target whose release has no data stream succeeds with
// no rule and the message no-profile-for-release.
func hardening(ctx context.Context, cfg Config, env Env) taskResult {
	release := filepath.Join(env.ContentDir, cfg.ContentRelease)
	if info, err := os.Stat(release); err != nil || !info.IsDir() {
		return refused("content-release-not-in-image: " + cfg.ContentRelease)
	}
	summary := &runresult.Hardening{ProfileID: cfg.ProfileID, ContentRelease: cfg.ContentRelease}
	results := HardeningResults{Schema: HardeningResultsSchema, ProfileID: cfg.ProfileID,
		ContentRelease: cfg.ContentRelease, Rules: []RuleResult{}}
	name, ok := DataStream(cfg.OSFamily, cfg.OSVersion)
	ds := filepath.Join(release, name)
	if info, err := os.Stat(ds); !ok || err != nil || !info.Mode().IsRegular() {
		results.Reason = MessageNoProfile
		if err := writeJSON(filepath.Join(env.WorkDir, FileHardeningResults), results); err != nil {
			return failed("%s was not written: %v", FileHardeningResults, err)
		}
		if err := writeMarker(env, TaskHardening); err != nil {
			return failed("the completion marker was not written: %v", err)
		}
		return taskResult{outcome: runresult.Succeeded, message: MessageNoProfile, hardening: summary,
			files: []file{{KindHardeningResults, FileHardeningResults}}}
	}
	results.DataStream = name
	arf := filepath.Join(env.WorkDir, FileHardeningARF)
	args := []string{env.HostRoot, "xccdf", "eval", "--results-arf", arf}
	profile := cfg.ProfileID
	if len(cfg.DisabledRules) > 0 {
		tailoring := filepath.Join(env.WorkDir, "hardening.tailoring.xml")
		if err := os.WriteFile(tailoring, Tailoring(ds, cfg.ProfileID, cfg.DisabledRules, time.Now()), 0o600); err != nil {
			return failed("the tailoring file was not written: %v", err)
		}
		profile = TailoredProfileID(cfg.ProfileID)
		args = append(args, "--tailoring-file", tailoring)
	}
	args = append(args, "--profile", profile, ds)
	// oscap exits 0 when every rule passed and 2 when a rule failed; both are results.
	if r := collect(ctx, nil, []int{0, 2}, env.OscapChroot, args...); r != nil {
		return *r
	}
	rules, err := ReadARF(arf)
	if err != nil {
		return failed("%s cannot be read: %v", FileHardeningARF, err)
	}
	if len(rules) == 0 {
		return failed("%s holds no rule result", FileHardeningARF)
	}
	results.Rules = rules
	results.Counts = Count(rules)
	if err := writeJSON(filepath.Join(env.WorkDir, FileHardeningResults), results); err != nil {
		return failed("%s was not written: %v", FileHardeningResults, err)
	}
	if err := writeMarker(env, TaskHardening); err != nil {
		return failed("the completion marker was not written: %v", err)
	}
	summary.Pass, summary.Fail = results.Counts.Pass, results.Counts.Fail
	summary.NotApplicable, summary.Error = results.Counts.NotApplicable, results.Counts.Error
	return taskResult{outcome: runresult.Succeeded, hardening: summary,
		files: []file{{KindHardeningARF, FileHardeningARF}, {KindHardeningResults, FileHardeningResults}}}
}

// Count sorts rule results into the four counts: pass and fixed are pass; fail is fail;
// notapplicable is not applicable; error and unknown are error. The results notchecked
// and informational are not counted.
func Count(rules []RuleResult) Counts {
	var c Counts
	for _, r := range rules {
		switch r.Result {
		case "pass", "fixed":
			c.Pass++
		case "fail":
			c.Fail++
		case "notapplicable":
			c.NotApplicable++
		case "error", "unknown":
			c.Error++
		}
	}
	return c
}

const xccdfNS = "http://checklists.nist.gov/xccdf/1.2"

// ReadARF returns the XCCDF rule results of an ARF file, in file order, without the
// rules whose result is notselected.
func ReadARF(path string) ([]RuleResult, error) {
	f, err := os.Open(path) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	dec := xml.NewDecoder(f)
	var out []RuleResult
	var current *RuleResult
	inResult := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != xccdfNS {
				continue
			}
			switch t.Name.Local {
			case "rule-result":
				current = &RuleResult{}
				for _, a := range t.Attr {
					if a.Name.Local == "idref" {
						current.ID = a.Value
					}
				}
			case "result":
				inResult = current != nil
			}
		case xml.CharData:
			if inResult {
				current.Result += strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			if t.Name.Space != xccdfNS {
				continue
			}
			switch t.Name.Local {
			case "result":
				inResult = false
			case "rule-result":
				if current == nil {
					continue
				}
				if current.ID == "" || current.Result == "" {
					return nil, fmt.Errorf("a rule-result without idref or result")
				}
				if current.Result != "notselected" {
					out = append(out, *current)
				}
				current = nil
			}
		}
	}
	return out, nil
}

// Tailoring returns an XCCDF 1.2 tailoring file for the data stream ds. Its one profile
// extends profileID and deselects each rule in disabled. Every identifier has been
// checked by ParseArgs; the text is escaped all the same.
func Tailoring(ds, profileID string, disabled []string, now time.Time) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<xccdf:Tailoring xmlns:xccdf="` + xccdfNS + `" id="xccdf_io.openova_tailoring_machine-runner">` + "\n")
	b.WriteString(`  <xccdf:benchmark href="` + escape(ds) + `"/>` + "\n")
	b.WriteString(`  <xccdf:version time="` + now.UTC().Format("2006-01-02T15:04:05") + `">1</xccdf:version>` + "\n")
	b.WriteString(`  <xccdf:Profile id="` + escape(TailoredProfileID(profileID)) + `" extends="` + escape(profileID) + `">` + "\n")
	b.WriteString(`    <xccdf:title>` + escape(profileID) + ` tailored by machine-runner</xccdf:title>` + "\n")
	for _, rule := range disabled {
		b.WriteString(`    <xccdf:select idref="` + escape(rule) + `" selected="false"/>` + "\n")
	}
	b.WriteString("  </xccdf:Profile>\n</xccdf:Tailoring>\n")
	return []byte(b.String())
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
