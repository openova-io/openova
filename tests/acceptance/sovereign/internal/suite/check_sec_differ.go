package suite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G06, no Keycloak client secret is computable. bp-keycloak
// (bootstrap-kit slot 09, release keycloak in namespace keycloak) derives each
// client secret of its realms as the SHA-256 of a seed. Without a salt the
// seed is the release name, the namespace and a constant, so it is the same
// on every Sovereign and anyone who reads the chart computes it. Since
// SEC-060 every seed ends in the random salt of the Sovereign (key
// KEYCLOAK_CLIENT_SECRET_SALT of Secret flux-system/bootstrap-seed).
//
// In every region the check lists the ConfigMaps of namespace keycloak and
// reads each realm import of bp-keycloak in them (a data value that is a JSON
// realm): the secret of every client and the clientSecret of every identity
// provider. A region with realm imports gets one step, and it fails when the
// imports hold no secret, or when a secret equals a seed without the salt:
// the SHA-256 of <release>|keycloak|<form> for the forms tier1-sso|<client>,
// tier2-sso|<client>, catalyst-api-server-sa, catalyst-pin-broker and
// tenant-broker|<org>, over every client identifier and every Organization
// (label catalyst.openova.io/org-slug) the imports name. When no region holds
// a realm import, one step fails: nothing was measured. A placeholder that the
// import resolves later (a value starting with "$(" or "${") is counted, not
// judged. The evidence holds, per secret, the first 16 hexadecimal characters
// of its SHA-256, never the secret, so two records of one pass can be
// compared.
const secDifferID = "SEC-G06"

const (
	secDifferNS      = "keycloak"                     // targetNamespace of slot 09
	secDifferSuffix  = "-sovereign-realm-config"      // <release> + this is the realm import of the realm sovereign
	secDifferOrgSlug = "catalyst.openova.io/org-slug" // label of a per-Organization realm import
	secDifferPrefix  = 16                             // hexadecimal characters of a secret's hash kept as evidence
)

type secDifferCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(secDifferCheck{kubectl: kube.Region.Run}) }

func (secDifferCheck) ID() string { return secDifferID }

// realmSecret is one client secret of a realm import.
type realmSecret struct {
	where string // <configmap>/<realm>/client:<id> or .../idp:<alias>
	value string
}

// realmImports is what the realm imports of one region hold.
type realmImports struct {
	release string   // the release name, from the name of the import of realm sovereign
	maps    []string // the ConfigMaps that hold a realm
	ids     []string // every client identifier and identity provider alias
	orgs    []string // every Organization slug
	secrets []realmSecret
}

func (c secDifferCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	measured := 0
	for _, region := range cc.Regions {
		imp, err := c.read(ctx, region)
		if err == nil && len(imp.maps) == 0 {
			continue
		}
		measured++
		n++
		checkStep(rec, secDifferID, n, "no Keycloak client secret of region "+region.ID+" is derived from constants", func(ev Evidence) error {
			if err != nil {
				return err
			}
			return judgeRealmSecrets(imp, ev)
		})
	}
	if measured == 0 {
		checkStep(rec, secDifferID, 1, "a region holds the realm imports of bp-keycloak", func(ev Evidence) error {
			ev["regions"] = len(cc.Regions)
			return fmt.Errorf("no region has a realm import in namespace %s: nothing was measured", secDifferNS)
		})
	}
	return nil
}

// read lists the ConfigMaps of namespace keycloak in region and collects their realm imports.
func (c secDifferCheck) read(ctx context.Context, region kube.Region) (realmImports, error) {
	out, err := c.kubectl(region, ctx, nil, "get", "configmaps", "-n", secDifferNS, "-o", "json")
	if err != nil {
		return realmImports{}, fmt.Errorf("listing the ConfigMaps of namespace %s: %v", secDifferNS, err)
	}
	var l struct {
		Items []kube.Object `json:"items"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		return realmImports{}, fmt.Errorf("the ConfigMaps of namespace %s are not JSON: %v", secDifferNS, err)
	}
	return collectRealmImports(l.Items), nil
}

// collectRealmImports reads every data value of items that is a JSON realm.
func collectRealmImports(items []kube.Object) realmImports {
	var imp realmImports
	ids, orgs := map[string]bool{}, map[string]bool{}
	for _, cm := range items {
		name := cm.Name()
		if strings.HasSuffix(name, secDifferSuffix) {
			imp.release = strings.TrimSuffix(name, secDifferSuffix)
		}
		data, _ := cm.Get("data").(map[string]any)
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		held := false
		for _, k := range keys {
			text, _ := data[k].(string)
			var realm kube.Object
			if json.Unmarshal([]byte(text), &realm) != nil || realm.Str("realm") == "" {
				continue
			}
			held = true
			at := name + "/" + realm.Str("realm")
			for _, cl := range realm.Objects("clients") {
				id := cl.Str("clientId")
				ids[id] = true
				if s := cl.Str("secret"); s != "" {
					imp.secrets = append(imp.secrets, realmSecret{at + "/client:" + id, s})
				}
			}
			for _, idp := range realm.Objects("identityProviders") {
				alias := idp.Str("alias")
				ids[alias], ids[idp.Str("config", "clientId")] = true, true
				if s := idp.Str("config", "clientSecret"); s != "" {
					imp.secrets = append(imp.secrets, realmSecret{at + "/idp:" + alias, s})
				}
			}
		}
		if held {
			imp.maps = append(imp.maps, name)
			if slug := cm.Str("metadata", "labels", secDifferOrgSlug); slug != "" {
				orgs[slug] = true
			}
		}
	}
	imp.ids, imp.orgs = sortedSet(ids), sortedSet(orgs)
	return imp
}

func sortedSet(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// unsaltedSeeds maps the SHA-256 of every seed without a salt to the seed.
func unsaltedSeeds(release string, ids, orgs []string) map[string]string {
	base := release + "|" + secDifferNS + "|"
	seeds := []string{base + "catalyst-api-server-sa", base + "catalyst-pin-broker"}
	for _, id := range ids {
		seeds = append(seeds, base+"tier1-sso|"+id, base+"tier2-sso|"+id)
	}
	for _, o := range orgs {
		seeds = append(seeds, base+"tenant-broker|"+o)
	}
	out := map[string]string{}
	for _, s := range seeds {
		out[sha256Hex(s)] = s
	}
	return out
}

// judgeRealmSecrets passes when the imports hold a secret and none is a seed without the salt.
func judgeRealmSecrets(imp realmImports, ev Evidence) error {
	ev["realm_imports"] = imp.maps
	if imp.release == "" {
		return fmt.Errorf("no ConfigMap <release>%s in namespace %s: the release name is unknown", secDifferSuffix, secDifferNS)
	}
	ev["release"] = imp.release
	if len(imp.secrets) == 0 {
		return fmt.Errorf("the realm imports %s hold no client secret", strings.Join(imp.maps, ", "))
	}
	known := unsaltedSeeds(imp.release, imp.ids, imp.orgs)
	hashes := map[string]string{}
	derived, placeholders := []string{}, 0
	for _, s := range imp.secrets {
		if strings.HasPrefix(s.value, "$(") || strings.HasPrefix(s.value, "${") {
			placeholders++
			continue
		}
		hashes[s.where] = sha256Hex(s.value)[:secDifferPrefix]
		if seed, ok := known[s.value]; ok {
			derived = append(derived, s.where+" is the SHA-256 of "+seed)
		}
	}
	ev["secrets"], ev["placeholders"], ev["secret_sha256_prefix"] = len(imp.secrets), placeholders, hashes
	if len(hashes) == 0 {
		return fmt.Errorf("every client secret of the realm imports is a placeholder (%d): nothing was measured", placeholders)
	}
	if len(derived) > 0 {
		sort.Strings(derived)
		return errors.New("client secrets are derived from constants, without the salt of the Sovereign: " + strings.Join(derived, "; "))
	}
	return nil
}
