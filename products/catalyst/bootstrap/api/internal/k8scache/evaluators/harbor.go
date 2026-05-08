// harbor.go — image-via-Harbor-proxy evaluator.
//
// EPIC-1 (#1096) §4.3 row "Images via Harbor proxy".
//
// Logic (per `02-W-watcher-extension.md` brief):
//
//   - For each container in the Pod (containers + initContainers +
//     ephemeralContainers), parse the image reference.
//   - Pass if image starts with `<HarborDomain>/proxy-ghcr/`,
//     `<HarborDomain>/<org>/`, or any of the operator-supplied
//     HarborAllowedPrefixes (e.g. internal mirrors).
//   - Fail if the image references docker.io, ghcr.io, quay.io, etc.
//     directly — every image must traverse the Sovereign's Harbor
//     proxy for cosign verification + air-gap fallback.
//   - When Config.HarborDomain is empty (Sovereign without Harbor
//     enabled) the evaluator skips every Pod.
//
// Per `feedback_never_hardcode_urls.md` the Harbor domain comes from
// runtime config — the Sovereign provisions Harbor at
// `harbor.<sovereign-domain>` and stamps the same value into the
// catalyst-api's CATALYST_HARBOR_DOMAIN env. Tests inject directly
// via Config.HarborDomain.
package evaluators

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HarborEvaluator implements `policy=harbor-proxy-pull`.
type HarborEvaluator struct {
	Domain          string
	AllowedPrefixes []string
}

// NewHarborEvaluator builds a HarborEvaluator from cfg.
func NewHarborEvaluator(cfg Config) *HarborEvaluator {
	return &HarborEvaluator{
		Domain:          cfg.HarborDomain,
		AllowedPrefixes: append([]string(nil), cfg.HarborAllowedPrefixes...),
	}
}

func (HarborEvaluator) Name() string { return "harbor-proxy-pull" }

func (h *HarborEvaluator) Evaluate(ctx context.Context, _ Snapshot, target *unstructured.Unstructured) []SyntheticReport {
	if !isPod(target) {
		return nil
	}
	if h.Domain == "" {
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultSkip,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Harbor not enabled on this Sovereign — evaluator skipped",
		}}
	}

	imgs := containerImages(target)
	if len(imgs) == 0 {
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultSkip,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Pod has no containers — nothing to check",
		}}
	}

	domainPrefix := h.Domain + "/"
	rejected := []string{}
	for _, img := range imgs {
		if h.imageAccepted(img, domainPrefix) {
			continue
		}
		rejected = append(rejected, img)
	}
	if len(rejected) == 0 {
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultPass,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "all container images via Harbor proxy",
		}}
	}
	return []SyntheticReport{{
		Policy:    h.Name(),
		Rule:      h.Name(),
		Result:    ResultFail,
		Resource:  resourceFor(target),
		Namespace: target.GetNamespace(),
		Message:   "container images bypass Harbor proxy: " + strings.Join(rejected, ", "),
		Properties: map[string]string{
			"rejectedImages": strings.Join(rejected, ","),
			"harborDomain":   h.Domain,
		},
	}}
}

// imageAccepted returns true when the image string starts with the
// Harbor domain prefix or any of the allowed-prefix entries.
//
// Acceptance is BYTE-PREFIX, not substring — `harbor.evil.com/`
// must not match `harbor.openova.io/` because of a trailing-slash
// rule. The check is case-sensitive (image refs are case-sensitive
// in OCI).
func (h *HarborEvaluator) imageAccepted(img, harborPrefix string) bool {
	if strings.HasPrefix(img, harborPrefix) {
		return true
	}
	for _, p := range h.AllowedPrefixes {
		if p != "" && strings.HasPrefix(img, p) {
			return true
		}
	}
	return false
}
