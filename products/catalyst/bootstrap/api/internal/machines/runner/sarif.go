package runner

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ZAPLevels maps the risk code of a ZAP alert to the SARIF level of its results, as ZAP's
// own SARIF report does: 3 (High) error, 2 (Medium) warning, 1 (Low) note, 0
// (Informational) none.
var ZAPLevels = map[string]string{"3": "error", "2": "warning", "1": "note", "0": "none"}

// zapReport is the part of ZAP's JSON report (template traditional-json) that the runner
// reads. Its fields match the keys of the report without regard to case (pluginid,
// alertRef, riskcode, cweid, uri), as encoding/json matches them.
type zapReport struct {
	Program string `json:"@programName"`
	Version string `json:"@version"`
	Site    *[]struct {
		Alerts []struct {
			PluginID, AlertRef, Name, RiskCode, Confidence, CWEID string
			Instances                                             []zapInstance
		}
	}
}

type zapInstance struct{ URI, Method, Param, Evidence string }

// ZAPRun converts the JSON report of one ZAP baseline scan of url into one SARIF 2.1.0 run
// and returns it with its number of results. Each instance of an alert is one result at
// the instance's URI; an alert without an instance is one result without a location. The
// rule of a result is the alert's reference (its plugin when it has none). A report that
// is not ZAP's, has no site list, or holds an alert without plugin, name or a known risk
// code is refused.
func ZAPRun(data []byte, url string) (json.RawMessage, int, error) {
	var r zapReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, 0, err
	}
	if r.Program != "ZAP" || r.Site == nil {
		return nil, 0, errors.New("not a ZAP report with a site list")
	}
	rules, results, seen := []any{}, []any{}, map[string]bool{}
	for _, site := range *r.Site {
		for _, a := range site.Alerts {
			level, ok := ZAPLevels[a.RiskCode]
			if a.PluginID == "" || a.Name == "" || !ok {
				return nil, 0, fmt.Errorf("an alert without plugin, name or a known risk code: %q", a.Name)
			}
			id := a.AlertRef
			if id == "" {
				id = a.PluginID
			}
			if !seen[id] {
				seen[id] = true
				rules = append(rules, map[string]any{"id": id, "name": a.Name, "shortDescription": text(a.Name),
					"properties": map[string]string{"pluginId": a.PluginID, "cweId": a.CWEID}})
			}
			instances := a.Instances
			if len(instances) == 0 {
				instances = []zapInstance{{}}
			}
			for _, in := range instances {
				res := map[string]any{"ruleId": id, "level": level, "message": text(a.Name), "properties": map[string]string{
					"confidence": a.Confidence, "method": in.Method, "param": in.Param, "evidence": in.Evidence}}
				if in.URI != "" {
					res["locations"] = []any{map[string]any{"physicalLocation": map[string]any{"artifactLocation": map[string]string{"uri": in.URI}}}}
				}
				results = append(results, res)
			}
		}
	}
	out, err := json.Marshal(map[string]any{"results": results, "properties": map[string]string{"target": url},
		"tool": map[string]any{"driver": map[string]any{"name": "ZAP", "version": r.Version, "informationUri": "https://www.zaproxy.org/", "rules": rules}}})
	return out, len(results), err
}

func text(s string) map[string]string { return map[string]string{"text": s} }
