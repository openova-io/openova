// Package upstream reads, writes and checks contract C5, the variables
// document: the data of ConfigMap flux-system/sovereign-variables, one per
// region cluster. The contract is published as two files,
// docs/contracts/c5-upstream-variables.schema.json and
// docs/contracts/c5-kit-keys.txt. go:embed cannot reach outside this Go
// module, so the folder contract/ of this package holds a byte-for-byte copy
// of both. scripts/check-variables-documents.sh fails when a copy differs.
// Change a contract file and its copy in the same pull request.
//
// Kit keys are the keys the bootstrap kit substitutes. A document may carry
// any of them; FromMap ignores them. Every other key is a contract key and
// is a field of Variables.
//
// The cross-field rules of the contract:
//
//  1. SOVEREIGN_CONSUME local requires REGISTRY_HOST == LOCAL_REGISTRY_HOST
//     and REGISTRY_SCHEME https.
//  2. SOVEREIGN_CONSUME origin requires REGISTRY_HOST == ORIGIN_REGISTRY_HOST,
//     REGISTRY_SCHEME == ORIGIN_REGISTRY_SCHEME and a non-empty
//     ORIGIN_REGISTRY_HOST.
//  3. MAIL_MODE relay requires non-empty MAIL_RELAY_HOST, MAIL_FROM and
//     MAIL_RELAY_SECRET.
//  4. DNS_MODE upstream-managed requires a non-empty DNS_UPSTREAM_API_URL.
//  5. No key holds a secret value: no key name ends in _KEY, _TOKEN,
//     _PASSWORD or _SECRET_VALUE, except the names in SecretNameExceptions.
//  6. HANDOVER_HOLD_SECONDS is between 30 and 3600.
//  7. No value of an ORIGIN_* key names this Sovereign's own FQDN or a host
//     beneath it.
//
// Rule 5 is a rule on key names, and Variables holds no key names, so FromMap
// enforces it. Validate enforces the other six.
//
// The package reads no environment variable, and no default names a host
// outside the Sovereign.
package upstream

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Namespace and Name of the ConfigMap that holds the variables document.
const (
	Namespace = "flux-system"
	Name      = "sovereign-variables"
)

// SecretNameExceptions are the key names that rule 5 allows although they end
// in a suffix it refuses. Neither holds a secret: RELEASE_PUBLIC_KEY_B64 is a
// public key and SOVEREIGN_REGION_KEY is the region's identifier.
var SecretNameExceptions = []string{"RELEASE_PUBLIC_KEY_B64", "SOVEREIGN_REGION_KEY"}

var secretSuffixes = []string{"_KEY", "_TOKEN", "_PASSWORD", "_SECRET_VALUE"}

//go:embed contract/c5-upstream-variables.schema.json
var schemaJSON []byte

//go:embed contract/c5-kit-keys.txt
var kitKeysText string

// Variables is the contract part of the variables document, one field per key of contract C5.
type Variables struct {
	Mode, Consume                                                                              string
	HandoverAuto                                                                               bool
	HandoverHoldSeconds                                                                        int
	RegistryHost, RegistryScheme                                                               string
	LocalRegistryHost                                                                          string
	OriginRegistryHost, OriginRegistryScheme, OriginRegistryCAB64                              string
	OriginProxyEndpoints                                                                       map[string]string
	PullSecret                                                                                 string
	PathCharts, PathRelease, PathProviderPacks, PathXpkg, PathTofuProviders, PathNodeArtifacts string
	ProxyProjects                                                                              map[string]string
	ReleasePublicKeyB64, ReleaseChannel, ReleaseVersion                                        string
	ReleaseAuto                                                                                bool
	ReleaseSyncSchedule                                                                        string
	ParentFQDN, ParentAPIURL                                                                   string
	DNSMode, DNSUpstreamAPIURL                                                                 string
	MailMode, MailRelayHost, MailRelayPort, MailFrom, MailRelaySecret                          string
	K3sVersion, K3sUpgradeConcurrency                                                          string
	CloudCredentialMode, CloudCredentialRotationSchedule, CloudCredentialRotationNonce         string
	GitopsRepoURL                                                                              string
}

// field binds one contract key to its field of Variables.
type field struct {
	key  string
	str  func(v *Variables) *string
	flag func(v *Variables) *bool
	num  func(v *Variables) *int
	obj  func(v *Variables) *map[string]string
}

// fields lists every contract key once, in the order of the contract.
var fields = []field{
	{key: "SOVEREIGN_MODE", str: func(v *Variables) *string { return &v.Mode }},
	{key: "SOVEREIGN_CONSUME", str: func(v *Variables) *string { return &v.Consume }},
	{key: "HANDOVER_AUTO", flag: func(v *Variables) *bool { return &v.HandoverAuto }},
	{key: "HANDOVER_HOLD_SECONDS", num: func(v *Variables) *int { return &v.HandoverHoldSeconds }},
	{key: "REGISTRY_HOST", str: func(v *Variables) *string { return &v.RegistryHost }},
	{key: "REGISTRY_SCHEME", str: func(v *Variables) *string { return &v.RegistryScheme }},
	{key: "LOCAL_REGISTRY_HOST", str: func(v *Variables) *string { return &v.LocalRegistryHost }},
	{key: "ORIGIN_REGISTRY_HOST", str: func(v *Variables) *string { return &v.OriginRegistryHost }},
	{key: "ORIGIN_REGISTRY_SCHEME", str: func(v *Variables) *string { return &v.OriginRegistryScheme }},
	{key: "ORIGIN_REGISTRY_CA_B64", str: func(v *Variables) *string { return &v.OriginRegistryCAB64 }},
	{key: "ORIGIN_PROXY_ENDPOINTS_JSON", obj: func(v *Variables) *map[string]string { return &v.OriginProxyEndpoints }},
	{key: "REGISTRY_PULL_SECRET", str: func(v *Variables) *string { return &v.PullSecret }},
	{key: "REGISTRY_PATH_CHARTS", str: func(v *Variables) *string { return &v.PathCharts }},
	{key: "REGISTRY_PATH_RELEASE", str: func(v *Variables) *string { return &v.PathRelease }},
	{key: "REGISTRY_PATH_PROVIDER_PACKS", str: func(v *Variables) *string { return &v.PathProviderPacks }},
	{key: "REGISTRY_PATH_XPKG", str: func(v *Variables) *string { return &v.PathXpkg }},
	{key: "REGISTRY_PATH_TOFU_PROVIDERS", str: func(v *Variables) *string { return &v.PathTofuProviders }},
	{key: "REGISTRY_PATH_NODE_ARTIFACTS", str: func(v *Variables) *string { return &v.PathNodeArtifacts }},
	{key: "REGISTRY_PROXY_PROJECTS_JSON", obj: func(v *Variables) *map[string]string { return &v.ProxyProjects }},
	{key: "RELEASE_PUBLIC_KEY_B64", str: func(v *Variables) *string { return &v.ReleasePublicKeyB64 }},
	{key: "RELEASE_CHANNEL", str: func(v *Variables) *string { return &v.ReleaseChannel }},
	{key: "RELEASE_VERSION", str: func(v *Variables) *string { return &v.ReleaseVersion }},
	{key: "RELEASE_AUTO", flag: func(v *Variables) *bool { return &v.ReleaseAuto }},
	{key: "RELEASE_SYNC_SCHEDULE", str: func(v *Variables) *string { return &v.ReleaseSyncSchedule }},
	{key: "ORIGIN_PARENT_FQDN", str: func(v *Variables) *string { return &v.ParentFQDN }},
	{key: "ORIGIN_PARENT_API_URL", str: func(v *Variables) *string { return &v.ParentAPIURL }},
	{key: "DNS_MODE", str: func(v *Variables) *string { return &v.DNSMode }},
	{key: "DNS_UPSTREAM_API_URL", str: func(v *Variables) *string { return &v.DNSUpstreamAPIURL }},
	{key: "MAIL_MODE", str: func(v *Variables) *string { return &v.MailMode }},
	{key: "MAIL_RELAY_HOST", str: func(v *Variables) *string { return &v.MailRelayHost }},
	{key: "MAIL_RELAY_PORT", str: func(v *Variables) *string { return &v.MailRelayPort }},
	{key: "MAIL_FROM", str: func(v *Variables) *string { return &v.MailFrom }},
	{key: "MAIL_RELAY_SECRET", str: func(v *Variables) *string { return &v.MailRelaySecret }},
	{key: "K3S_VERSION", str: func(v *Variables) *string { return &v.K3sVersion }},
	{key: "K3S_UPGRADE_CONCURRENCY", str: func(v *Variables) *string { return &v.K3sUpgradeConcurrency }},
	{key: "CLOUD_CREDENTIAL_MODE", str: func(v *Variables) *string { return &v.CloudCredentialMode }},
	{key: "CLOUD_CREDENTIAL_ROTATION_SCHEDULE", str: func(v *Variables) *string { return &v.CloudCredentialRotationSchedule }},
	{key: "CLOUD_CREDENTIAL_ROTATION_NONCE", str: func(v *Variables) *string { return &v.CloudCredentialRotationNonce }},
	{key: "GITOPS_REPO_URL", str: func(v *Variables) *string { return &v.GitopsRepoURL }},
}

// contract is the embedded schema, compiled once.
type contract struct {
	schema   *jsonschema.Schema
	defaults map[string]string
	kitKeys  map[string]bool
}

var (
	loaded    contract
	loadErr   error
	loadOnce  sync.Once
	schemaURL = "c5-upstream-variables.schema.json"
)

func load() (contract, error) {
	loadOnce.Do(func() {
		var doc struct {
			Properties map[string]struct {
				Default *string `json:"default"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(schemaJSON, &doc); err != nil {
			loadErr = fmt.Errorf("upstream: the embedded schema is not JSON: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(schemaURL, bytes.NewReader(schemaJSON)); err != nil {
			loadErr = fmt.Errorf("upstream: the embedded schema: %w", err)
			return
		}
		compiled, err := compiler.Compile(schemaURL)
		if err != nil {
			loadErr = fmt.Errorf("upstream: the embedded schema does not compile: %w", err)
			return
		}
		c := contract{schema: compiled, defaults: map[string]string{}, kitKeys: map[string]bool{}}
		for key, p := range doc.Properties {
			if p.Default != nil {
				c.defaults[key] = *p.Default
			}
		}
		for _, line := range strings.Split(kitKeysText, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				c.kitKeys[line] = true
			}
		}
		if len(doc.Properties) != len(fields) {
			loadErr = fmt.Errorf("upstream: the schema has %d keys and Variables %d", len(doc.Properties), len(fields))
			return
		}
		for _, f := range fields {
			if _, ok := doc.Properties[f.key]; !ok {
				loadErr = fmt.Errorf("upstream: the schema lacks the key %s", f.key)
				return
			}
			if c.kitKeys[f.key] {
				loadErr = fmt.Errorf("upstream: %s is both a contract key and a kit key", f.key)
				return
			}
		}
		loaded = c
	})
	return loaded, loadErr
}

// KitKeys returns the kit keys of the contract, sorted.
func KitKeys() []string {
	c, err := load()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(c.kitKeys))
	for k := range c.kitKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// namesSecret reports whether rule 5 refuses the key name.
func namesSecret(key string) bool {
	for _, e := range SecretNameExceptions {
		if key == e {
			return false
		}
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(key, s) {
			return true
		}
	}
	return false
}

// FromMap parses ConfigMap data. It fails on a missing required key or a value outside its enum or pattern.
// Keys listed in the kit key file are ignored. Any other unknown key is an error.
// It also fails on a key name that rule 5 refuses, a kit key included.
func FromMap(data map[string]string) (Variables, error) {
	c, err := load()
	if err != nil {
		return Variables{}, err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var problems []string
	instance := map[string]interface{}{}
	for _, k := range keys {
		if namesSecret(k) {
			problems = append(problems, fmt.Sprintf("rule 5: the key %s names a secret value; a secret never goes into the variables document", k))
			continue
		}
		if !c.kitKeys[k] {
			instance[k] = data[k]
		}
	}
	if len(problems) > 0 {
		return Variables{}, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	if err := c.schema.Validate(instance); err != nil {
		return Variables{}, fmt.Errorf("schema: %s", schemaMessage(err))
	}
	full := map[string]string{}
	for k, v := range c.defaults {
		full[k] = v
	}
	for k, v := range instance {
		full[k] = v.(string)
	}
	return decode(full)
}

// schemaMessage flattens a validation error to its leaf messages, one per problem.
func schemaMessage(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := strings.TrimPrefix(e.InstanceLocation, "/")
			if loc == "" {
				out = append(out, e.Message)
			} else {
				out = append(out, loc+": "+e.Message)
			}
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(ve)
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// decode turns the values of every contract key into a Variables. A key that
// is absent gives the zero value of its field.
func decode(m map[string]string) (Variables, error) {
	var v Variables
	for _, f := range fields {
		raw := m[f.key]
		switch {
		case f.str != nil:
			*f.str(&v) = raw
		case f.flag != nil:
			switch raw {
			case "true":
				*f.flag(&v) = true
			case "false", "":
				*f.flag(&v) = false
			default:
				return Variables{}, fmt.Errorf("%s: %q is not true or false", f.key, raw)
			}
		case f.num != nil:
			if raw == "" {
				break
			}
			n, err := strconv.Atoi(raw)
			if err != nil {
				return Variables{}, fmt.Errorf("%s: %q is not a whole number", f.key, raw)
			}
			*f.num(&v) = n
		case f.obj != nil:
			out := map[string]string{}
			if raw != "" {
				if err := json.Unmarshal([]byte(raw), &out); err != nil {
					return Variables{}, fmt.Errorf("%s: not a JSON object of strings: %v", f.key, err)
				}
				if out == nil {
					return Variables{}, fmt.Errorf("%s: not a JSON object of strings: null", f.key)
				}
			}
			*f.obj(&v) = out
		}
	}
	return v, nil
}

// ToMap renders every key of contract C5, defaults included. FromMap(ToMap(v)) == v.
func (v Variables) ToMap() map[string]string {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		switch {
		case f.str != nil:
			out[f.key] = *f.str(&v)
		case f.flag != nil:
			out[f.key] = strconv.FormatBool(*f.flag(&v))
		case f.num != nil:
			out[f.key] = strconv.Itoa(*f.num(&v))
		case f.obj != nil:
			m := *f.obj(&v)
			if m == nil {
				m = map[string]string{}
			}
			b, _ := json.Marshal(m) // a map of strings always marshals; the keys come out sorted
			out[f.key] = string(b)
		}
	}
	return out
}

// Validate returns one error per violated cross-field rule of contract C5.
func (v Variables) Validate(ownFQDN string) []error {
	var errs []error
	if v.Consume == "local" && (v.RegistryHost != v.LocalRegistryHost || v.RegistryScheme != "https") {
		errs = append(errs, fmt.Errorf("rule 1: SOVEREIGN_CONSUME is local, so REGISTRY_HOST must equal LOCAL_REGISTRY_HOST (%q) and REGISTRY_SCHEME must be https; they are %q and %q", v.LocalRegistryHost, v.RegistryHost, v.RegistryScheme))
	}
	if v.Consume == "origin" && (v.OriginRegistryHost == "" || v.RegistryHost != v.OriginRegistryHost || v.RegistryScheme != v.OriginRegistryScheme) {
		errs = append(errs, fmt.Errorf("rule 2: SOVEREIGN_CONSUME is origin, so ORIGIN_REGISTRY_HOST must not be empty and REGISTRY_HOST and REGISTRY_SCHEME must equal ORIGIN_REGISTRY_HOST (%q) and ORIGIN_REGISTRY_SCHEME (%q); they are %q and %q", v.OriginRegistryHost, v.OriginRegistryScheme, v.RegistryHost, v.RegistryScheme))
	}
	if v.MailMode == "relay" && (v.MailRelayHost == "" || v.MailFrom == "" || v.MailRelaySecret == "") {
		errs = append(errs, fmt.Errorf("rule 3: MAIL_MODE is relay, so MAIL_RELAY_HOST, MAIL_FROM and MAIL_RELAY_SECRET must not be empty"))
	}
	if v.DNSMode == "upstream-managed" && v.DNSUpstreamAPIURL == "" {
		errs = append(errs, fmt.Errorf("rule 4: DNS_MODE is upstream-managed, so DNS_UPSTREAM_API_URL must not be empty"))
	}
	if v.HandoverHoldSeconds < 30 || v.HandoverHoldSeconds > 3600 {
		errs = append(errs, fmt.Errorf("rule 6: HANDOVER_HOLD_SECONDS is %d; it must be between 30 and 3600", v.HandoverHoldSeconds))
	}
	own := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ownFQDN)), ".")
	if own != "" {
		origin := []struct{ key, value string }{
			{"ORIGIN_REGISTRY_HOST", v.OriginRegistryHost},
			{"ORIGIN_PARENT_FQDN", v.ParentFQDN},
			{"ORIGIN_PARENT_API_URL", v.ParentAPIURL},
		}
		names := make([]string, 0, len(v.OriginProxyEndpoints))
		for n := range v.OriginProxyEndpoints {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			origin = append(origin, struct{ key, value string }{"ORIGIN_PROXY_ENDPOINTS_JSON[" + n + "]", v.OriginProxyEndpoints[n]})
		}
		for _, o := range origin {
			if h := hostOf(o.value); h != "" && (h == own || strings.HasSuffix(h, "."+own)) {
				errs = append(errs, fmt.Errorf("rule 7: %s is %q, which names this Sovereign's own name %s", o.key, o.value, own))
			}
		}
	}
	return errs
}

// hostOf returns the lower-case host of a host, host:port or address value.
func hostOf(value string) string {
	s := strings.TrimSpace(value)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "//" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return strings.ToLower(s)
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// defaults returns the variables that hold the default of every key that has one.
func defaults() Variables {
	c, err := load()
	if err != nil {
		return Variables{}
	}
	v, err := decode(c.defaults)
	if err != nil {
		return Variables{}
	}
	return v
}

// Served returns the variables a parent hands to a child. Consume is origin. The origin keys name the
// parent's own hosts (parent.LocalRegistryHost, https, "https://console.<parentFQDN>/sovereign").
// Paths, proxy projects, channel and release key are copied. The parent's own Origin* values are never copied.
// Per-child keys (LocalRegistryHost, K3sVersion, state and mail keys) are left empty for the caller to fill.
// Every other key takes its default; a key without a default stays empty.
func Served(parentFQDN string, parent Variables) Variables {
	v := defaults()
	v.Consume = "origin"
	v.OriginRegistryHost = parent.LocalRegistryHost
	v.OriginRegistryScheme = "https"
	v.OriginRegistryCAB64 = ""
	v.OriginProxyEndpoints = map[string]string{}
	v.RegistryHost = parent.LocalRegistryHost
	v.RegistryScheme = "https"
	v.ParentFQDN = parentFQDN
	v.ParentAPIURL = "https://console." + parentFQDN + "/sovereign"
	v.PathCharts = parent.PathCharts
	v.PathRelease = parent.PathRelease
	v.PathProviderPacks = parent.PathProviderPacks
	v.PathXpkg = parent.PathXpkg
	v.PathTofuProviders = parent.PathTofuProviders
	v.PathNodeArtifacts = parent.PathNodeArtifacts
	v.ProxyProjects = map[string]string{}
	for k, p := range parent.ProxyProjects {
		v.ProxyProjects[k] = p
	}
	v.ReleaseChannel = parent.ReleaseChannel
	v.ReleasePublicKeyB64 = parent.ReleasePublicKeyB64
	v.LocalRegistryHost = ""
	v.K3sVersion = ""
	v.MailMode, v.MailRelayHost, v.MailFrom, v.MailRelaySecret = "", "", "", ""
	return v
}

// Flipped returns v with Consume local, RegistryHost LocalRegistryHost and RegistryScheme https.
func (v Variables) Flipped() Variables {
	out := v
	out.OriginProxyEndpoints = copyMap(v.OriginProxyEndpoints)
	out.ProxyProjects = copyMap(v.ProxyProjects)
	out.Consume = "local"
	out.RegistryHost = v.LocalRegistryHost
	out.RegistryScheme = "https"
	return out
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Load reads flux-system/sovereign-variables.
func Load(ctx context.Context, c client.Reader) (Variables, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: Name}, &cm); err != nil {
		return Variables{}, fmt.Errorf("upstream: read ConfigMap %s/%s: %w", Namespace, Name, err)
	}
	v, err := FromMap(cm.Data)
	if err != nil {
		return Variables{}, fmt.Errorf("upstream: ConfigMap %s/%s: %w", Namespace, Name, err)
	}
	return v, nil
}

// mirror is one entry of the mirrors map of /etc/rancher/k3s/registries.yaml.
type mirror struct {
	Endpoint []string          `json:"endpoint"`
	Rewrite  map[string]string `json:"rewrite,omitempty"`
}

// RegistriesYAML renders the mirrors map of the node file
// /etc/rancher/k3s/registries.yaml. dragonflyProxy is empty when the slot is
// off.
//
// The configs map of that file (pull logins and certificate authorities)
// carries secrets and is added by the caller. This function renders none of
// it. hostedRewrites is a YAML or JSON object from a registry name
// to a rewrite map; for each name it holds, its rewrite map replaces the
// rewrite this function would write. It carries the rewrite rules of the
// platform's own hosted project, which the provider modules render today.
// Empty means none.
//
// For each name N of ProxyProjects, with project path P, the entry N lists,
// in order: dragonflyProxy when it is not empty; https://<LocalRegistryHost>;
// and, only while Consume is origin, the origin endpoint
// (OriginProxyEndpoints[N] when present, else
// <OriginRegistryScheme>://<OriginRegistryHost>). Its rewrite maps "(.*)" to
// "P/$1" when P is not empty. Three more entries follow: LocalRegistryHost
// with the single endpoint https://<LocalRegistryHost>; while Consume is
// origin, OriginRegistryHost with the local endpoint first and the origin
// endpoint second; and "*" with the single endpoint
// https://<LocalRegistryHost> and no rewrite.
func (v Variables) RegistriesYAML(dragonflyProxy string, hostedRewrites []byte) ([]byte, error) {
	if v.LocalRegistryHost == "" {
		return nil, fmt.Errorf("upstream: LOCAL_REGISTRY_HOST is empty")
	}
	origin := v.Consume == "origin"
	if origin && v.OriginRegistryHost == "" {
		return nil, fmt.Errorf("upstream: SOVEREIGN_CONSUME is origin and ORIGIN_REGISTRY_HOST is empty")
	}
	if v.Consume != "origin" && v.Consume != "local" {
		return nil, fmt.Errorf("upstream: SOVEREIGN_CONSUME is %q, not origin or local", v.Consume)
	}
	rewrites := map[string]map[string]string{}
	if len(bytes.TrimSpace(hostedRewrites)) > 0 {
		if err := yaml.UnmarshalStrict(hostedRewrites, &rewrites); err != nil {
			return nil, fmt.Errorf("upstream: hostedRewrites: %w", err)
		}
	}
	for n := range rewrites {
		if _, ok := v.ProxyProjects[n]; !ok {
			return nil, fmt.Errorf("upstream: hostedRewrites names %q, which is not a key of REGISTRY_PROXY_PROJECTS_JSON", n)
		}
	}
	local := "https://" + v.LocalRegistryHost
	originDefault := v.OriginRegistryScheme + "://" + v.OriginRegistryHost
	mirrors := map[string]mirror{}
	for n, p := range v.ProxyProjects {
		m := mirror{}
		if dragonflyProxy != "" {
			m.Endpoint = append(m.Endpoint, dragonflyProxy)
		}
		m.Endpoint = append(m.Endpoint, local)
		if origin {
			if e, ok := v.OriginProxyEndpoints[n]; ok && e != "" {
				m.Endpoint = append(m.Endpoint, e)
			} else {
				m.Endpoint = append(m.Endpoint, originDefault)
			}
		}
		if r, ok := rewrites[n]; ok {
			m.Rewrite = r
		} else if p != "" {
			m.Rewrite = map[string]string{"(.*)": p + "/$1"}
		}
		mirrors[n] = m
	}
	extra := []struct {
		name string
		m    mirror
	}{{v.LocalRegistryHost, mirror{Endpoint: []string{local}}}}
	if origin {
		extra = append(extra, struct {
			name string
			m    mirror
		}{v.OriginRegistryHost, mirror{Endpoint: []string{local, originDefault}}})
	}
	extra = append(extra, struct {
		name string
		m    mirror
	}{"*", mirror{Endpoint: []string{local}}})
	for _, e := range extra {
		if _, taken := mirrors[e.name]; taken {
			return nil, fmt.Errorf("upstream: the registry name %q would have two mirror entries", e.name)
		}
		mirrors[e.name] = e.m
	}
	return yaml.Marshal(map[string]interface{}{"mirrors": mirrors})
}
