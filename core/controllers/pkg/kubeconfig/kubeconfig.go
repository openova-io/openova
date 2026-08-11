// Package kubeconfig holds the ONE usability contract for a secondary
// region's kubeconfig — the single question "can these bytes produce a working
// client?" — shared by every component that either writes such a document or
// reads one back.
//
// WHY THIS IS A SHARED PACKAGE AND NOT TWO COPIES
// -----------------------------------------------
// A secondary region's kubeconfig crosses three hops, and each hop had its own
// idea of what "delivered" meant:
//
//	catalyst-api  export  →  waitForSecondaryKubeconfig   len(raw) > 0
//	catalyst-api  receive →  HandleSovereignSecondaryKubeconfig   non-empty
//	org-controller read   →  consoleRegionTargets   len(v) != 0
//
// All three measured PRESENCE. On hw293 (dep a0077ba47e3720e5) that produced a
// 95-byte credential-less document — valid YAML, one `clusters[]` entry with a
// server URL, no contexts, no users, no CA data — which every hop happily
// passed along. #6054 fixed the receive hop, #6015/#6112 the export hop, #6107
// the read hop. Three fixes to one question is exactly the shape that drifts:
// the moment two of them disagree about what "usable" means, a document is
// accepted by one component and rejected by the next, and the region is
// credential-less again with nothing reporting it.
//
// So the question is answered in ONE place and imported by both sides. The
// dependency direction already exists — products/catalyst/bootstrap/api
// requires core/controllers (see its go.mod replace) — so this introduces no
// new module edge, and the org-controller is in this module already.
//
// THE DECISIVE CHECK IS THE CONSUMER'S OWN PARSER
// -----------------------------------------------
// `clientcmd.RESTConfigFromKubeConfig` is the EXACT call
// k8scache.AddCluster makes (internal/k8scache/factory.go) and the exact call
// the org-controller's newRegionClient makes. Binding to it is deliberate: a
// bespoke structural re-implementation could drift from what actually builds a
// client, and then this gate would pass documents the consumers still reject.
//
// The structural walk exists ONLY to name the missing sections for an
// operator — it never overrides the parser's verdict.
//
// Refs #6015 #6054 #6104 #6107 #6112.
package kubeconfig

import (
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// Defects reports why raw cannot produce a working client, as a stable, sorted
// list of section names suitable for a log line or an operator-facing
// condition message. An empty slice means the document is usable.
//
// The returned names are deliberately the kubeconfig's own top-level keys
// ("clusters", "contexts", "users", "current-context") so an operator reading
// the message can diff the rejected document against a healthy one without
// needing this source file.
func Defects(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"empty"}
	}

	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil || cfg == nil {
		return []string{"unparseable"}
	}

	missing := map[string]struct{}{}

	// A cluster entry with no server URL is the shape the hw293 stub had AFTER
	// its truncation point removed everything else: the section is present, so
	// a "has clusters?" check passes, yet nothing dialable is declared. Assert
	// on the VALUE, not the presence of the KEY.
	hasServer := false
	for _, c := range cfg.Clusters {
		if c != nil && strings.TrimSpace(c.Server) != "" {
			hasServer = true
			break
		}
	}
	if !hasServer {
		missing["clusters"] = struct{}{}
	}
	if len(cfg.Contexts) == 0 {
		missing["contexts"] = struct{}{}
	}
	if len(cfg.AuthInfos) == 0 {
		missing["users"] = struct{}{}
	}
	if strings.TrimSpace(cfg.CurrentContext) == "" {
		missing["current-context"] = struct{}{}
	}

	// Decisive gate — the consumer's own parser. A document that satisfies
	// every structural check above but still cannot build a rest.Config
	// (unresolvable current-context, a context naming a cluster or user that
	// does not exist, a user carrying no credential at all) is rejected here
	// rather than later, at the point of use.
	if _, rerr := clientcmd.RESTConfigFromKubeConfig([]byte(raw)); rerr != nil {
		if len(missing) == 0 {
			missing["client-unbuildable"] = struct{}{}
		}
	} else if len(missing) == 0 {
		return nil
	}

	out := make([]string, 0, len(missing))
	for k := range missing {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Usable reports whether raw can produce a client — the boolean form of
// Defects for call sites that do not need to name the gap.
func Usable(raw string) bool {
	return len(Defects(raw)) == 0
}

// DescribeDefects renders a defect list for an operator-facing message,
// naming the empty case explicitly rather than rendering "[]".
func DescribeDefects(defects []string) string {
	if len(defects) == 0 {
		return "none"
	}
	return strings.Join(defects, ",")
}
