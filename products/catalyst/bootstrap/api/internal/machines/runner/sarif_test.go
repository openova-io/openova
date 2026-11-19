package runner

import (
	"encoding/json"
	"reflect"
	"testing"
)

// zapReportJSON is a ZAP 2.17.0 traditional-json report of two sites: a High alert with two
// instances, a Medium and a Low alert of one plugin under two references, an
// Informational alert without an instance, and a second site whose alert repeats a rule.
const zapReportJSON = `{"@programName":"ZAP","@version":"2.17.0","@generated":"Thu, 19 Nov 2026 10:00:00","created":"2026-11-19T10:00:00Z",
"site":[{"@name":"https://10.0.0.5:443","@host":"10.0.0.5","@port":"443","@ssl":"true","alerts":[
 {"pluginid":"10035","alertRef":"10035-1","alert":"Strict-Transport-Security Header Not Set","name":"Strict-Transport-Security Header Not Set","riskcode":"3","confidence":"3","cweid":"319","instances":[
  {"id":"1","uri":"https://10.0.0.5:443/","nodeName":"https://10.0.0.5/","method":"GET","param":"","attack":"","evidence":"","otherinfo":""},
  {"id":"2","uri":"https://10.0.0.5:443/robots.txt","method":"GET","param":"","attack":"","evidence":"","otherinfo":""}],"count":"2","systemic":true},
 {"pluginid":"10038","alertRef":"10038-1","name":"Content Security Policy (CSP) Header Not Set","riskcode":"2","confidence":"3","cweid":"693","instances":[{"uri":"https://10.0.0.5:443/","method":"GET"}]},
 {"pluginid":"10038","alertRef":"10038-3","name":"CSP: Wildcard Directive","riskcode":"1","confidence":"2","instances":[{"uri":"https://10.0.0.5:443/","method":"GET","param":"Content-Security-Policy","evidence":"default-src *"}]},
 {"pluginid":"10104","alertRef":"","name":"User Agent Fuzzer","riskcode":"0","confidence":"2","instances":[]}]},
 {"@name":"https://10.0.0.5:443/app","alerts":[{"pluginid":"10035","alertRef":"10035-1","name":"Strict-Transport-Security Header Not Set","riskcode":"3","confidence":"3","instances":[{"uri":"https://10.0.0.5:443/app","method":"GET"}]}]}]}`

// parsedRun reads a SARIF run back; its fields match the SARIF keys without regard to case.
type parsedRun struct {
	Tool struct {
		Driver struct {
			Name, Version string
			Rules         []struct {
				ID               string
				ShortDescription struct{ Text string }
				Properties       map[string]string
			}
		}
	}
	Results []struct {
		RuleID, Level string
		Locations     []struct {
			PhysicalLocation struct{ ArtifactLocation struct{ URI string } }
		}
		Properties map[string]string
	}
	Properties map[string]string
}

func TestZAPRunMapsEveryRiskCodeAndInstance(t *testing.T) {
	run, n, err := ZAPRun([]byte(zapReportJSON), "https://10.0.0.5:443/")
	var got parsedRun
	if err != nil || n != 6 || json.Unmarshal(run, &got) != nil {
		t.Fatalf("got %d results: %v\n%s", n, err, run)
	}
	if got.Tool.Driver.Name != "ZAP" || got.Tool.Driver.Version != "2.17.0" || got.Properties["target"] != "https://10.0.0.5:443/" {
		t.Fatalf("tool %+v properties %v", got.Tool.Driver, got.Properties)
	}
	var rules, results []string
	for _, r := range got.Tool.Driver.Rules {
		rules = append(rules, r.ID+" "+r.ShortDescription.Text+" "+r.Properties["pluginId"]+" "+r.Properties["cweId"])
	}
	for _, r := range got.Results {
		uri := "-"
		if len(r.Locations) == 1 {
			uri = r.Locations[0].PhysicalLocation.ArtifactLocation.URI
		}
		results = append(results, r.RuleID+" "+r.Level+" "+uri+" "+r.Properties["evidence"])
	}
	if want := []string{"10035-1 Strict-Transport-Security Header Not Set 10035 319", "10038-1 Content Security Policy (CSP) Header Not Set 10038 693",
		"10038-3 CSP: Wildcard Directive 10038 ", "10104 User Agent Fuzzer 10104 "}; !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules %q, want %q", rules, want)
	}
	if want := []string{"10035-1 error https://10.0.0.5:443/ ", "10035-1 error https://10.0.0.5:443/robots.txt ", "10038-1 warning https://10.0.0.5:443/ ",
		"10038-3 note https://10.0.0.5:443/ default-src *", "10104 none - ", "10035-1 error https://10.0.0.5:443/app "}; !reflect.DeepEqual(results, want) {
		t.Fatalf("results %q, want %q", results, want)
	}
	if _, total, err := ParseSARIF([]byte(`{"version":"2.1.0","runs":[` + string(run) + `]}`)); err != nil || total != 6 {
		t.Fatalf("the run is not read back as SARIF: %d %v", total, err)
	}
}

func TestZAPRunWithoutAlertsIsOneRunWithNoResult(t *testing.T) {
	run, n, err := ZAPRun([]byte(`{"@programName":"ZAP","@version":"2.17.0","site":[{"@name":"http://10.0.0.5:80","alerts":[]}]}`), "http://10.0.0.5:80/")
	if _, total, perr := ParseSARIF([]byte(`{"version":"2.1.0","runs":[` + string(run) + `]}`)); err != nil || n != 0 || perr != nil || total != 0 {
		t.Fatalf("got %d results: %v %v", n, err, perr)
	}
}

func TestZAPRunRefusesABadReport(t *testing.T) {
	for _, report := range []string{
		``, `[]`, `{"@programName":"Nuclei","site":[]}`, `{"@programName":"ZAP"}`,
		`{"@programName":"ZAP","site":[{"alerts":[{"pluginid":"","name":"x","riskcode":"1"}]}]}`,
		`{"@programName":"ZAP","site":[{"alerts":[{"pluginid":"1","name":"","riskcode":"1"}]}]}`,
		`{"@programName":"ZAP","site":[{"alerts":[{"pluginid":"1","name":"x","riskcode":"4"}]}]}`,
		`{"@programName":"ZAP","site":[{"alerts":[{"pluginid":"1","name":"x","riskcode":"-1"}]}]}`,
		`{"@programName":"ZAP","site":[{"alerts":[{"pluginid":"1","name":"x","riskcode":2}]}]}`,
	} {
		if _, _, err := ZAPRun([]byte(report), "http://10.0.0.5:80/"); err == nil {
			t.Errorf("report %s was accepted", report)
		}
	}
}
