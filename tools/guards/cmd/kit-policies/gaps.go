package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"
)

// gapRow is one known refusal: the object, the policy that refuses it, why it
// is not repaired yet, the day the row was written and the package that
// retires it.
type gapRow struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Policy    string `json:"policy"`
	Reason    string `json:"reason"`
	Since     string `json:"since"`
	RetiredBy string `json:"retiredBy"`
}

var packageID = regexp.MustCompile(`^[A-Z]{3}-[0-9]{3}$`)

// readGaps reads the register. A missing file, an unknown field, a row without
// one of its fields, a day that is not a date, a package that is not a package
// identifier and two rows for one object and policy are errors.
func readGaps(path string) ([]gapRow, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the guard's own input
	if err != nil {
		return nil, err
	}
	var file struct {
		Gaps []gapRow `json:"gaps"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("not the register's JSON: %v", err)
	}
	if file.Gaps == nil {
		return nil, fmt.Errorf("no field gaps (an empty register is \"gaps\": [])")
	}
	seen := map[string]bool{}
	for i, g := range file.Gaps {
		for field, v := range map[string]string{"kind": g.Kind, "namespace": g.Namespace, "name": g.Name, "policy": g.Policy, "reason": g.Reason, "since": g.Since, "retiredBy": g.RetiredBy} {
			if v == "" {
				return nil, fmt.Errorf("row %d has no %s", i+1, field)
			}
		}
		if _, err := time.Parse("2006-01-02", g.Since); err != nil {
			return nil, fmt.Errorf("row %d: since %q is not a day (YYYY-MM-DD)", i+1, g.Since)
		}
		if !packageID.MatchString(g.RetiredBy) {
			return nil, fmt.Errorf("row %d: retiredBy %q is not a package of the plan (AAA-000)", i+1, g.RetiredBy)
		}
		k := key(g.Kind, g.Namespace, g.Name) + "|" + g.Policy
		if seen[k] {
			return nil, fmt.Errorf("row %d repeats %s %s/%s and %s", i+1, g.Kind, g.Namespace, g.Name, g.Policy)
		}
		seen[k] = true
	}
	return file.Gaps, nil
}
