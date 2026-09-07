package huawei

import (
	"encoding/json"
	"sort"
	"strings"
)

// Resource tags — the cost-allocation dimension (EPIC #6867 follow-up).
//
// Every Huawei list API that returns tags returns them in a DIFFERENT shape,
// measured on kom4dc 2026-09-07:
//
//	ECS  cloudservers/detail  tags: ["key=value", ...]  (also "key" alone)
//	EVS  cloudvolumes/detail  tags: {"key": "value"}
//	EIP  publicips            tags: ["key=value", ...]
//	ELB  v3 loadbalancers     tags: [{"key": "...", "value": "..."}]
//	NAT  v2 nat_gateways      (no tags field)
//
// normalizeTags folds all three into one map so the rest of the app has one
// dimension, `tag:<key>`, whatever the service. Keys are case-sensitive on
// this cloud and are kept verbatim; nothing is lower-cased.

const (
	maxTags      = 50
	maxTagKeyLen = 128
)

// normalizeTags accepts the decoded JSON of a `tags` field in any of the
// three shapes and returns key → value. Empty keys and keys longer than 128
// characters are dropped; a "key" entry with no "=" keeps the key with an
// empty value (the tag exists — it has no value, which is a valid Huawei
// tag). At most 50 tags are kept, the first 50 in key order so the cap is
// deterministic across collections. nil/unknown shapes return an empty map.
func normalizeTags(v any) map[string]string {
	out := map[string]string{}
	put := func(k, val string) {
		if k == "" || len(k) > maxTagKeyLen {
			return
		}
		out[k] = val
	}
	switch t := v.(type) {
	case map[string]any:
		for k, raw := range t {
			put(k, tagString(raw))
		}
	case map[string]string:
		for k, val := range t {
			put(k, val)
		}
	case []any:
		for _, e := range t {
			switch item := e.(type) {
			case string:
				k, val, _ := strings.Cut(item, "=")
				put(k, val)
			case map[string]any:
				put(tagString(item["key"]), tagString(item["value"]))
			}
		}
	case []string:
		for _, item := range t {
			k, val, _ := strings.Cut(item, "=")
			put(k, val)
		}
	}
	if len(out) > maxTags {
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[maxTags:] {
			delete(out, k)
		}
	}
	return out
}

// tagString renders a tag value: strings verbatim, numbers/bools as JSON
// text, nil/objects as "" (a tag value is a scalar on every service).
func tagString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64, bool, json.Number:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

// tagsFromRaw decodes a raw `tags` JSON field and normalizes it. Absent or
// malformed fields yield an empty map so a lister never fails on tags.
func tagsFromRaw(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]string{}
	}
	return normalizeTags(v)
}

// putTagAttrs stores the normalized tags (only when there are any) and the
// enterprise project id (only when present) on a resource's attrs. One
// function so every lister stores the same attr names the usage emitter and
// the explorer read (`tags`, `enterprise_project_id`).
func putTagAttrs(attrs map[string]any, rawTags json.RawMessage, enterpriseProjectID string) {
	if tags := tagsFromRaw(rawTags); len(tags) > 0 {
		attrs["tags"] = tags
	}
	if ep := strings.TrimSpace(enterpriseProjectID); ep != "" {
		attrs["enterprise_project_id"] = ep
	}
}
