// secondary_kubeconfig_completeness.go — refuse to PERSIST a secondary
// kubeconfig that cannot produce a client.
//
// Root cause (measured live on hw293, dep a0077ba47e3720e5, region
// me-east-215-b-1):
//
//	The chroot's PVC held
//	`/var/lib/catalyst/kubeconfigs/a0077ba47e3720e5-me-east-215-b-1.yaml`
//	at **95 bytes** — `apiVersion`, `kind: Config`, one `clusters[]` entry
//	carrying only a `server:` URL, and then nothing: the document ends
//	mid-token on `  name: c` with no trailing newline. No `contexts`, no
//	`users`, no `current-context`, no `certificate-authority-data`. Beside
//	it sat the healthy region-a file at **1109 bytes** carrying all five
//	top-level keys. A credential-less shell had taken region B's slot.
//
//	HandleSovereignSecondaryKubeconfig produced that file. Its ONLY content
//	check was `strings.TrimSpace(body.KubeconfigYAML) == ""`, so a 95-byte
//	shell passed as readily as a complete 2959-byte kubeconfig. The bytes
//	were written to disk FIRST; the parse that would have caught them —
//	k8sCache.AddCluster's `clientcmd.RESTConfigFromKubeConfig` — ran 21
//	lines LATER. When it failed, the handler returned HTTP 500 and left the
//	unusable file exactly where it had put it. Twice, on the record:
//
//	  18:03:41  AddCluster failed … invalid configuration: no configuration
//	            has been provided, try setting KUBERNETES_MASTER …
//	  18:03:41  POST /api/v1/sovereign/secondary-kubeconfig status=500 elapsedMs=6002
//	  18:04:19  POST /api/v1/sovereign/secondary-kubeconfig status=500 elapsedMs=6001
//
//	Two 500s, and the 95-byte file still on disk after both. A 500 that
//	leaves its own failure durable is not a rejection — it is a write with
//	a complaint attached. Every later reader (buildRegionSlots, the jobs
//	informer, the placement resolver, onDiskSecondaryKubeconfigKeys) then
//	sees region B as DELIVERED, because delivery was being measured by the
//	presence of a file rather than by the usability of its contents.
//
//	The 6002ms/6001ms is the second cost: `elapsedMs` is two 3-second
//	`secondaryDialTimeout` probes — hostReachable + apiserverCertPrivateSANs
//	— which the #4000 self-heal spent dialling the stub's server host. Six
//	seconds of request-path latency burned on a document that could never
//	register no matter which host it pointed at.
//
// This file adds the gate that was missing: validate BEFORE persisting,
// and name what is absent. Two properties matter and are pinned by
// TestSecondaryKubeconfig* :
//
//  1. An unusable document is REFUSED (422) and never reaches disk — so it
//     cannot displace a good file that a previous delivery already landed.
//     Any sender may be buggy; the region-B slot stays credible regardless.
//  2. A complete kubeconfig is still accepted unchanged (the vacuity arm) —
//     a gate that cannot pass is as useless as one that cannot fail.
//
// The decisive check is `clientcmd.RESTConfigFromKubeConfig`, which is the
// EXACT call k8scache.AddCluster makes (internal/k8scache/factory.go:710).
// Binding to the consumer's own parser is deliberate: a bespoke structural
// re-implementation could drift from what actually builds a client, and
// then this gate would pass documents the cache still rejects. The
// structural walk exists only to NAME the missing sections for the operator
// — it never overrides the parser's verdict.
//
// Refs #6015, #6040.

package handler

import (
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// secondaryKubeconfigDefects reports why raw cannot produce a working
// client, as a stable, sorted list of section names for the log + the HTTP
// response. An empty slice means the document is usable.
//
// The returned names are deliberately the kubeconfig's own top-level keys
// ("clusters", "contexts", "users", "current-context") so an operator
// reading the 422 can diff the rejected document against a healthy one
// without needing this source file.
func secondaryKubeconfigDefects(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"empty"}
	}

	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil || cfg == nil {
		return []string{"unparseable"}
	}

	missing := map[string]struct{}{}

	// A cluster entry with no server URL is the shape the hw293 stub had
	// AFTER its truncation point removed everything else: the section is
	// present, so a "has clusters?" check passes, yet nothing dialable is
	// declared. Assert on the VALUE, not the presence of the KEY.
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
	// (unresolvable current-context, a context naming a cluster or user
	// that does not exist, a user carrying no credential at all) is
	// rejected here rather than at AddCluster time, which is what the
	// pre-fix ordering deferred until after the write.
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

// secondaryKubeconfigUsable reports whether raw can produce a client — the
// boolean form of secondaryKubeconfigDefects for call sites that do not
// need to name the gap.
func secondaryKubeconfigUsable(raw string) bool {
	return len(secondaryKubeconfigDefects(raw)) == 0
}
