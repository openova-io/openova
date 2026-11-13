package compliance

import (
	"encoding/json"
	"fmt"
	"regexp"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The one object kind LoadModel accepts.
const (
	ModelAPIVersion = "compliance.openova.io/v1alpha1"
	ModelKind       = "ComplianceModel"
)

// Axis types.
const (
	AxisTypeChecks  = "checks"
	AxisTypeMeasure = "measure"
)

// Model is the parsed spec of a ComplianceModel object. Every default is
// already applied: Check.Weight, Check.Mandatory and Check.Switchable hold
// their final values.
type Model struct {
	Version         string
	Levels          []Level // sorted by Min, highest first; the last Min is 0
	MandatoryCap    MandatoryCap
	SeverityWeights SeverityWeights
	Groups          []Group
	Axes            []Axis
	Checks          []Check
	Exemptions      []Exemption
}

// Level is one band of scores. A score at or above Min and below the Min
// of the level before it has this level.
type Level struct {
	Name string
	Min  int
}

// MandatoryCap is the upper bound of a subject's score while a mandatory
// check fails, in the listed environment types.
type MandatoryCap struct {
	Value            int
	EnvironmentTypes []string
}

// SeverityWeights is the weight of a check that states no weight.
type SeverityWeights struct {
	Critical, High, Medium, Low int
}

// Group is a share of the overall figure.
type Group struct {
	ID, Title string
	Weight    int
}

// Axis is one line of the score card.
type Axis struct {
	ID, Title, Group string
	Type             string // AxisTypeChecks or AxisTypeMeasure
	Measure          string // the named measure when Type is AxisTypeMeasure, else ""
	NAText           string
}

// Check is one rule of the model.
type Check struct {
	ID, Title   string
	Source      Source
	Axis        string
	Severity    string
	Weight      int // 0 = shown, not scored
	Mandatory   bool
	OwnerSide   string
	ValuesKey   string
	Switchable  bool
	Description string
	Remediation string
}

// Exemption takes the listed checks of one namespace out of the score.
type Exemption struct {
	Namespace string
	Checks    []string // check ids, or the single item "*"
	Reason    string
	Owner     string
}

type rawModel struct {
	Version         *string             `json:"version"`
	Levels          []rawLevel          `json:"levels"`
	MandatoryCap    *rawMandatoryCap    `json:"mandatoryCap"`
	SeverityWeights *rawSeverityWeights `json:"severityWeights"`
	Groups          []rawGroup          `json:"groups"`
	Axes            []rawAxis           `json:"axes"`
	Checks          *[]rawCheck         `json:"checks"`
	Exemptions      *[]rawExemption     `json:"exemptions"`
}

type rawLevel struct {
	Name *string `json:"name"`
	Min  *int    `json:"min"`
}

type rawMandatoryCap struct {
	Value            *int      `json:"value"`
	EnvironmentTypes *[]string `json:"environmentTypes"`
}

type rawSeverityWeights struct {
	Critical *int `json:"critical"`
	High     *int `json:"high"`
	Medium   *int `json:"medium"`
	Low      *int `json:"low"`
}

type rawGroup struct {
	ID     *string `json:"id"`
	Title  *string `json:"title"`
	Weight *int    `json:"weight"`
}

type rawAxis struct {
	ID      *string `json:"id"`
	Title   *string `json:"title"`
	Group   *string `json:"group"`
	Type    *string `json:"type"`
	Measure *string `json:"measure"`
	NAText  *string `json:"naText"`
}

type rawCheck struct {
	ID          *string `json:"id"`
	Title       *string `json:"title"`
	Source      *string `json:"source"`
	Axis        *string `json:"axis"`
	Severity    *string `json:"severity"`
	Weight      *int    `json:"weight"`
	Mandatory   *bool   `json:"mandatory"`
	OwnerSide   *string `json:"ownerSide"`
	ValuesKey   *string `json:"valuesKey"`
	Switchable  *bool   `json:"switchable"`
	Description *string `json:"description"`
	Remediation *string `json:"remediation"`
}

type rawExemption struct {
	Namespace *string   `json:"namespace"`
	Checks    *[]string `json:"checks"`
	Reason    *string   `json:"reason"`
	Owner     *string   `json:"owner"`
}

var (
	semverRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	idRE     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

	validSources = map[Source]bool{
		SourceKyverno: true, SourceEvaluator: true, "trivy-vulnerability": true,
		"trivy-configaudit": true, "trivy-exposedsecret": true, "trivy-rbac": true,
	}
	validSeverities = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	validOwnerSides = map[string]bool{"organization": true, "sovereign": true}
	validMeasures   = map[string]bool{"runtime-clean": true, "memory-efficiency": true}
)

// LoadModel parses and checks the spec of a ComplianceModel object. It
// returns an error naming the first rule the object breaks; the caller then
// keeps the model it had. Fields this package does not read are ignored.
func LoadModel(obj *unstructured.Unstructured) (*Model, error) {
	if obj == nil {
		return nil, fmt.Errorf("compliance model: no object")
	}
	if obj.GetAPIVersion() != ModelAPIVersion || obj.GetKind() != ModelKind {
		return nil, fmt.Errorf("compliance model: object is %s %s, want %s %s", obj.GetAPIVersion(), obj.GetKind(), ModelAPIVersion, ModelKind)
	}
	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("compliance model: spec: required")
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("compliance model: spec: %w", err)
	}
	var raw rawModel
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("compliance model: spec: %w", err)
	}
	m := &Model{}
	for _, step := range []func(*rawModel, *Model) error{
		loadVersion, loadLevels, loadMandatoryCap, loadSeverityWeights,
		loadGroups, loadAxes, loadChecks, loadExemptions,
	} {
		if err := step(&raw, m); err != nil {
			return nil, fmt.Errorf("compliance model: %w", err)
		}
	}
	return m, nil
}

func requiredString(path string, v *string) (string, error) {
	if v == nil || *v == "" {
		return "", fmt.Errorf("%s: required", path)
	}
	return *v, nil
}

func requiredInt(path string, v *int, lo, hi int) (int, error) {
	if v == nil {
		return 0, fmt.Errorf("%s: required", path)
	}
	if *v < lo || *v > hi {
		return 0, fmt.Errorf("%s: %d is outside %d..%d", path, *v, lo, hi)
	}
	return *v, nil
}

func loadVersion(raw *rawModel, m *Model) error {
	v, err := requiredString("version", raw.Version)
	if err != nil {
		return err
	}
	if !semverRE.MatchString(v) {
		return fmt.Errorf("version: %q is not a semantic version", v)
	}
	m.Version = v
	return nil
}

func loadLevels(raw *rawModel, m *Model) error {
	if len(raw.Levels) < 2 || len(raw.Levels) > 10 {
		return fmt.Errorf("levels: %d items, want 2..10", len(raw.Levels))
	}
	seen := map[string]bool{}
	for i, rl := range raw.Levels {
		path := fmt.Sprintf("levels[%d]", i)
		name, err := requiredString(path+".name", rl.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("%s.name: %q is not unique", path, name)
		}
		seen[name] = true
		lmin, err := requiredInt(path+".min", rl.Min, 0, 100)
		if err != nil {
			return err
		}
		if i > 0 && lmin >= m.Levels[i-1].Min {
			return fmt.Errorf("%s.min: levels are not sorted by min, highest first", path)
		}
		m.Levels = append(m.Levels, Level{Name: name, Min: lmin})
	}
	if last := m.Levels[len(m.Levels)-1]; last.Min != 0 {
		return fmt.Errorf("levels: the last min is %d, want 0", last.Min)
	}
	return nil
}

func loadMandatoryCap(raw *rawModel, m *Model) error {
	if raw.MandatoryCap == nil {
		return fmt.Errorf("mandatoryCap: required")
	}
	v, err := requiredInt("mandatoryCap.value", raw.MandatoryCap.Value, 0, 100)
	if err != nil {
		return err
	}
	if raw.MandatoryCap.EnvironmentTypes == nil {
		return fmt.Errorf("mandatoryCap.environmentTypes: required")
	}
	m.MandatoryCap = MandatoryCap{Value: v, EnvironmentTypes: append([]string{}, *raw.MandatoryCap.EnvironmentTypes...)}
	return nil
}

func loadSeverityWeights(raw *rawModel, m *Model) error {
	sw := raw.SeverityWeights
	if sw == nil {
		return fmt.Errorf("severityWeights: required")
	}
	var err error
	if m.SeverityWeights.Critical, err = requiredInt("severityWeights.critical", sw.Critical, 0, 100); err != nil {
		return err
	}
	if m.SeverityWeights.High, err = requiredInt("severityWeights.high", sw.High, 0, 100); err != nil {
		return err
	}
	if m.SeverityWeights.Medium, err = requiredInt("severityWeights.medium", sw.Medium, 0, 100); err != nil {
		return err
	}
	if m.SeverityWeights.Low, err = requiredInt("severityWeights.low", sw.Low, 0, 100); err != nil {
		return err
	}
	return nil
}

func loadGroups(raw *rawModel, m *Model) error {
	if len(raw.Groups) < 1 || len(raw.Groups) > 4 {
		return fmt.Errorf("groups: %d items, want 1..4", len(raw.Groups))
	}
	seen := map[string]bool{}
	for i, rg := range raw.Groups {
		path := fmt.Sprintf("groups[%d]", i)
		id, err := requiredString(path+".id", rg.ID)
		if err != nil {
			return err
		}
		if !idRE.MatchString(id) {
			return fmt.Errorf("%s.id: %q does not match %s", path, id, idRE)
		}
		if seen[id] {
			return fmt.Errorf("%s.id: %q is not unique", path, id)
		}
		seen[id] = true
		title, err := requiredString(path+".title", rg.Title)
		if err != nil {
			return err
		}
		w, err := requiredInt(path+".weight", rg.Weight, 1, 100)
		if err != nil {
			return err
		}
		m.Groups = append(m.Groups, Group{ID: id, Title: title, Weight: w})
	}
	return nil
}

func loadAxes(raw *rawModel, m *Model) error {
	if len(raw.Axes) < 1 || len(raw.Axes) > 30 {
		return fmt.Errorf("axes: %d items, want 1..30", len(raw.Axes))
	}
	groups := map[string]bool{}
	for _, g := range m.Groups {
		groups[g.ID] = true
	}
	seen := map[string]bool{}
	for i, ra := range raw.Axes {
		path := fmt.Sprintf("axes[%d]", i)
		id, err := requiredString(path+".id", ra.ID)
		if err != nil {
			return err
		}
		if !idRE.MatchString(id) {
			return fmt.Errorf("%s.id: %q does not match %s", path, id, idRE)
		}
		if seen[id] {
			return fmt.Errorf("%s.id: %q is not unique", path, id)
		}
		seen[id] = true
		title, err := requiredString(path+".title", ra.Title)
		if err != nil {
			return err
		}
		group, err := requiredString(path+".group", ra.Group)
		if err != nil {
			return err
		}
		if !groups[group] {
			return fmt.Errorf("%s.group: %q is not an id of groups", path, group)
		}
		typ, err := requiredString(path+".type", ra.Type)
		if err != nil {
			return err
		}
		measure := ""
		switch typ {
		case AxisTypeChecks:
			if ra.Measure != nil && *ra.Measure != "" {
				return fmt.Errorf("%s.measure: must be absent when type is checks", path)
			}
		case AxisTypeMeasure:
			if measure, err = requiredString(path+".measure", ra.Measure); err != nil {
				return err
			}
			if !validMeasures[measure] {
				return fmt.Errorf("%s.measure: %q is not a known measure", path, measure)
			}
		default:
			return fmt.Errorf("%s.type: %q is not checks or measure", path, typ)
		}
		naText, err := requiredString(path+".naText", ra.NAText)
		if err != nil {
			return err
		}
		m.Axes = append(m.Axes, Axis{ID: id, Title: title, Group: group, Type: typ, Measure: measure, NAText: naText})
	}
	return nil
}

func (sw SeverityWeights) of(severity string) int {
	switch severity {
	case "critical":
		return sw.Critical
	case "high":
		return sw.High
	case "medium":
		return sw.Medium
	default:
		return sw.Low
	}
}

func loadChecks(raw *rawModel, m *Model) error {
	if raw.Checks == nil {
		return fmt.Errorf("checks: required")
	}
	checkAxes := map[string]bool{}
	for _, a := range m.Axes {
		if a.Type == AxisTypeChecks {
			checkAxes[a.ID] = true
		}
	}
	seen := map[string]bool{}
	for i, rc := range *raw.Checks {
		path := fmt.Sprintf("checks[%d]", i)
		c := Check{Switchable: true}
		var err error
		if c.ID, err = requiredString(path+".id", rc.ID); err != nil {
			return err
		}
		if seen[c.ID] {
			return fmt.Errorf("%s.id: %q is not unique", path, c.ID)
		}
		seen[c.ID] = true
		if c.Title, err = requiredString(path+".title", rc.Title); err != nil {
			return err
		}
		if utf8.RuneCountInString(c.Title) > 80 {
			return fmt.Errorf("%s.title: longer than 80 characters", path)
		}
		src, err := requiredString(path+".source", rc.Source)
		if err != nil {
			return err
		}
		c.Source = Source(src)
		if !validSources[c.Source] {
			return fmt.Errorf("%s.source: %q is not a known source", path, src)
		}
		if c.Axis, err = requiredString(path+".axis", rc.Axis); err != nil {
			return err
		}
		if !checkAxes[c.Axis] {
			return fmt.Errorf("%s.axis: %q is not an id of an axis of type checks", path, c.Axis)
		}
		if c.Severity, err = requiredString(path+".severity", rc.Severity); err != nil {
			return err
		}
		if !validSeverities[c.Severity] {
			return fmt.Errorf("%s.severity: %q is not critical, high, medium or low", path, c.Severity)
		}
		c.Weight = m.SeverityWeights.of(c.Severity)
		if rc.Weight != nil {
			if c.Weight, err = requiredInt(path+".weight", rc.Weight, 0, 100); err != nil {
				return err
			}
		}
		if rc.Mandatory != nil {
			c.Mandatory = *rc.Mandatory
		}
		if c.OwnerSide, err = requiredString(path+".ownerSide", rc.OwnerSide); err != nil {
			return err
		}
		if !validOwnerSides[c.OwnerSide] {
			return fmt.Errorf("%s.ownerSide: %q is not organization or sovereign", path, c.OwnerSide)
		}
		if c.Source == SourceKyverno {
			if c.ValuesKey, err = requiredString(path+".valuesKey", rc.ValuesKey); err != nil {
				return err
			}
		} else if rc.ValuesKey != nil && *rc.ValuesKey != "" {
			return fmt.Errorf("%s.valuesKey: must be absent when source is %s", path, c.Source)
		}
		if rc.Switchable != nil {
			c.Switchable = *rc.Switchable
		}
		if c.Description, err = requiredString(path+".description", rc.Description); err != nil {
			return err
		}
		if c.Remediation, err = requiredString(path+".remediation", rc.Remediation); err != nil {
			return err
		}
		if utf8.RuneCountInString(c.Remediation) < 20 {
			return fmt.Errorf("%s.remediation: shorter than 20 characters", path)
		}
		m.Checks = append(m.Checks, c)
	}
	return nil
}

func loadExemptions(raw *rawModel, m *Model) error {
	if raw.Exemptions == nil {
		return fmt.Errorf("exemptions: required")
	}
	for i, re := range *raw.Exemptions {
		path := fmt.Sprintf("exemptions[%d]", i)
		e := Exemption{}
		var err error
		if e.Namespace, err = requiredString(path+".namespace", re.Namespace); err != nil {
			return err
		}
		if re.Checks == nil || len(*re.Checks) == 0 {
			return fmt.Errorf("%s.checks: required, at least one item", path)
		}
		for j, id := range *re.Checks {
			if id == "" {
				return fmt.Errorf("%s.checks[%d]: empty", path, j)
			}
			if id == "*" && len(*re.Checks) > 1 {
				return fmt.Errorf("%s.checks: \"*\" must be the only item", path)
			}
		}
		e.Checks = append([]string{}, *re.Checks...)
		if e.Reason, err = requiredString(path+".reason", re.Reason); err != nil {
			return err
		}
		if utf8.RuneCountInString(e.Reason) < 20 {
			return fmt.Errorf("%s.reason: shorter than 20 characters", path)
		}
		if e.Owner, err = requiredString(path+".owner", re.Owner); err != nil {
			return err
		}
		m.Exemptions = append(m.Exemptions, e)
	}
	return nil
}

// exempts reports whether an exemption of the model covers the check in the namespace.
func (m *Model) exempts(namespace, check string) bool {
	if namespace == "" {
		return false
	}
	for _, e := range m.Exemptions {
		if e.Namespace != namespace {
			continue
		}
		for _, id := range e.Checks {
			if id == "*" || id == check {
				return true
			}
		}
	}
	return false
}

// level returns the name of the first level whose Min is at or below v.
func (m *Model) level(v int) string {
	for _, l := range m.Levels {
		if v >= l.Min {
			return l.Name
		}
	}
	return ""
}
