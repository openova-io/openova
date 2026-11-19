package judge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/correlate"
)

// Lanes a judge can run in.
const (
	LaneDeciding   = "deciding"
	LaneBackground = "background"
)

// Tiers of an agent token. A judge never gets a higher one.
const (
	TierViewer    = "viewer"
	TierDeveloper = "developer"
)

// Placeholders of AgentRuntime.URLTemplate.
const (
	placeholderOrganization = "{organization}"
	placeholderAgent        = "{agent}"
)

var (
	agentPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	organizationPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,31}$`)
)

// Policy is the judge policy: the values block "judges" of the service's chart. Every field is
// required unless its comment says otherwise.
type Policy struct {
	// OperationsOrganization receives the signals of platform alerts.
	OperationsOrganization string `json:"operationsOrganization"`
	// AgentRuntime says where the agents answer.
	AgentRuntime AgentRuntime `json:"agentRuntime"`
	// Judges by name. The name is the one questions use.
	Judges map[string]Judge `json:"judges"`
	// Lanes holds the number of turns each lane runs at once.
	Lanes map[string]int `json:"lanes"`
	// BatchSeconds and UrgentBatchSeconds are the waits of a batch of new signals.
	BatchSeconds       int `json:"batchSeconds"`
	UrgentBatchSeconds int `json:"urgentBatchSeconds"`
	// TurnBudget limits the turns of each Organization and judge.
	TurnBudget TurnBudget `json:"turnBudget"`
}

// AgentRuntime holds the A2A address of the agents.
type AgentRuntime struct {
	// URLTemplate is the A2A address of one agent of one Organization's install, with the
	// placeholders {organization} and {agent}.
	URLTemplate string `json:"urlTemplate"`
}

// Judge is the policy of one judge.
type Judge struct {
	Agent                 string `json:"agent"`                 // the agent's name in the Organization's install
	Lane                  string `json:"lane"`                  // LaneDeciding or LaneBackground
	DeadlineSeconds       int    `json:"deadlineSeconds"`       // 10..1800; after it the rule result is written
	UrgentDeadlineSeconds int    `json:"urgentDeadlineSeconds"` // optional (0 for none); 10..DeadlineSeconds
	TokenTier             string `json:"tokenTier"`             // TierViewer or TierDeveloper
	TokenTTLSeconds       int    `json:"tokenTTLSeconds"`       // 60..3600, not below DeadlineSeconds; the life of a turn
}

// TurnBudget allows Turns turns per Organization and judge in each window of WindowSeconds.
type TurnBudget struct {
	Turns         int `json:"turns"`         // 1..10000
	WindowSeconds int `json:"windowSeconds"` // 60..86400
}

// ParsePolicy reads a policy from JSON and validates it. An unknown field is refused.
func ParsePolicy(data []byte) (Policy, error) {
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("judge: policy: %w", err)
	}
	if dec.More() {
		return Policy{}, fmt.Errorf("judge: policy: data after the document")
	}
	return p, p.Validate()
}

// Validate returns an error naming the first rule the policy breaks.
func (p Policy) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("judge: policy: "+format, args...)
	}
	if !organizationPattern.MatchString(p.OperationsOrganization) {
		return bad("operationsOrganization %q is not an Organization slug", p.OperationsOrganization)
	}
	if err := checkTemplate(p.AgentRuntime.URLTemplate); err != nil {
		return bad("agentRuntime.urlTemplate: %v", err)
	}
	if len(p.Judges) == 0 {
		return bad("judges is empty")
	}
	used := map[string]bool{}
	for _, name := range sortedKeys(p.Judges) {
		j := p.Judges[name]
		switch {
		case !agentPattern.MatchString(name):
			return bad("judge name %q", name)
		case !agentPattern.MatchString(j.Agent):
			return bad("judge %s: agent %q", name, j.Agent)
		case j.Lane != LaneDeciding && j.Lane != LaneBackground:
			return bad("judge %s: lane %q is not %s or %s", name, j.Lane, LaneDeciding, LaneBackground)
		case p.Lanes[j.Lane] < 1:
			return bad("judge %s: lane %s has no turns in lanes", name, j.Lane)
		case j.DeadlineSeconds < 10 || j.DeadlineSeconds > 1800:
			return bad("judge %s: deadlineSeconds %d is not 10..1800", name, j.DeadlineSeconds)
		case j.UrgentDeadlineSeconds != 0 && (j.UrgentDeadlineSeconds < 10 || j.UrgentDeadlineSeconds > j.DeadlineSeconds):
			return bad("judge %s: urgentDeadlineSeconds %d is not 10..%d", name, j.UrgentDeadlineSeconds, j.DeadlineSeconds)
		case j.TokenTier != TierViewer && j.TokenTier != TierDeveloper:
			return bad("judge %s: tokenTier %q is not %s or %s", name, j.TokenTier, TierViewer, TierDeveloper)
		case j.TokenTTLSeconds < 60 || j.TokenTTLSeconds > 3600:
			return bad("judge %s: tokenTTLSeconds %d is not 60..3600", name, j.TokenTTLSeconds)
		case j.TokenTTLSeconds < j.DeadlineSeconds:
			return bad("judge %s: tokenTTLSeconds %d is below deadlineSeconds %d", name, j.TokenTTLSeconds, j.DeadlineSeconds)
		}
		used[j.Lane] = true
	}
	for _, lane := range sortedKeys(p.Lanes) {
		switch {
		case lane != LaneDeciding && lane != LaneBackground:
			return bad("lane %q is not %s or %s", lane, LaneDeciding, LaneBackground)
		case p.Lanes[lane] < 1:
			return bad("lane %s has %d turns", lane, p.Lanes[lane])
		case !used[lane]:
			return bad("lane %s has no judge", lane)
		}
	}
	if err := p.Batches().Validate(); err != nil {
		return bad("batchSeconds and urgentBatchSeconds: %v", err)
	}
	if p.TurnBudget.Turns < 1 || p.TurnBudget.Turns > 10000 {
		return bad("turnBudget.turns %d is not 1..10000", p.TurnBudget.Turns)
	}
	if p.TurnBudget.WindowSeconds < 60 || p.TurnBudget.WindowSeconds > 86400 {
		return bad("turnBudget.windowSeconds %d is not 60..86400", p.TurnBudget.WindowSeconds)
	}
	return nil
}

// Batches returns the waits of the batches of new signals.
func (p Policy) Batches() correlate.Policy {
	return correlate.Policy{Batch: time.Duration(p.BatchSeconds) * time.Second,
		UrgentBatch: time.Duration(p.UrgentBatchSeconds) * time.Second}
}

// URL returns the A2A address of one agent of one Organization.
func (r AgentRuntime) URL(organization, agent string) string {
	return strings.NewReplacer(placeholderOrganization, organization, placeholderAgent, agent).Replace(r.URLTemplate)
}

func checkTemplate(t string) error {
	if !strings.Contains(t, placeholderOrganization) || !strings.Contains(t, placeholderAgent) {
		return fmt.Errorf("%q needs both %s and %s", t, placeholderOrganization, placeholderAgent)
	}
	u, err := url.Parse(AgentRuntime{URLTemplate: t}.URL("acme", "correlator"))
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Contains(u.Host, "{") {
		return fmt.Errorf("%q is not an http or https address", t)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
