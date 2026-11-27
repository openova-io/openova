// Kit keys set by commit (SOV-760).
//
// PUT /api/v1/sovereign/kit-keys/{key} with the body {"value": "<string>",
// "regions": ["<region-id>"]} sets one kit key in the variables document of
// the named regions, or of every region when regions is absent or empty: the
// file clusters/<fqdn>/regions/<region-id>/platform/root/sovereign-variables.yaml
// of the fleet repository (ConfigMap flux-system/sovereign-variables), which
// seed-repo commits once (SOV-170) and Flux applies from then on. All files
// change in one commit of the commit library (contract C4, scope
// RegionPlatform, module root); nothing is written to the cluster. Only the
// keys of docs/contracts/c5-kit-keys-settable.txt are accepted.
//
// It needs a session of tier owner (see SovereignOwner) and
// answers 202 {"commit","paths"}; 400 {"error","field"} for a refused key,
// value or region; 409 when a region holds no seeded document or a file
// changed since it was read (the caller sends again); 503 when the repository
// cannot be reached or the commit library is not configured.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// FleetRepo is the fleet repository of contract C4, section 2.1.
var FleetRepo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions", Branch: "main"}

// VariablesFile is the variables document inside a region folder, the file
// seed-repo writes for ConfigMap sovereign-variables.
const VariablesFile = "platform/root/sovereign-variables.yaml"

const maxKitKeyBody = 64 << 10

// kitKeyName is a key name short enough that the commit summary
// "kit-keys: set <key>" fits the library's 72 characters.
var kitKeyName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,57}$`)

// ParseKitKeyList reads a key list in the form of
// docs/contracts/c5-kit-keys-settable.txt: one key per line; blank lines and
// lines that start with # are skipped. A line that is not a key name is an error.
func ParseKitKeyList(text string) (map[string]bool, error) {
	keys := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !kitKeyName.MatchString(line) {
			return nil, fmt.Errorf("line %d: %q is not a key name", i+1, line)
		}
		keys[line] = true
	}
	return keys, nil
}

// KitKeysRoute serves PUT /api/v1/sovereign/kit-keys/{key}. With Git nil
// (the commit library is not configured) a request that passes its own
// checks is answered 503.
type KitKeysRoute struct {
	Log      *slog.Logger
	Git      gitwrite.Writer
	FQDN     string          // this Sovereign's FQDN, the folder clusters/<fqdn>/
	Settable map[string]bool // the keys a request may set
}

// SovereignOwner reports whether a session is of tier owner: the realm role
// catalyst-owner or the tier owner. No claims and every other tier are refused.
func SovereignOwner(c *auth.Claims) bool {
	return c != nil && (c.HasRealmRole("catalyst-owner") || strings.EqualFold(strings.TrimSpace(c.Tier), "owner"))
}

func kitKeyRefuse(w http.ResponseWriter, code int, errCode, field, detail string) {
	body := map[string]string{"error": errCode, "detail": detail}
	if field != "" {
		body["field"] = field
	}
	writeJSON(w, code, body)
}

// gitRefuse answers a failed call of the commit library.
func gitRefuse(w http.ResponseWriter, what string, err error) {
	switch {
	case gitwrite.IsConflict(err):
		kitKeyRefuse(w, http.StatusConflict, "head-moved", "", what+": the file changed since it was read; send the request again")
	case errors.Is(err, gitwrite.ErrUnavailable):
		kitKeyRefuse(w, http.StatusServiceUnavailable, "git-unavailable", "", what+": "+err.Error())
	default:
		kitKeyRefuse(w, http.StatusInternalServerError, "git-failed", "", what+": "+err.Error())
	}
}

// validKitKeyValue reports whether v is valid UTF-8 without a control
// character: Flux substitutes the value into manifests as it is.
func validKitKeyValue(v string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return utf8.ValidString(v)
}

// Put handles PUT /api/v1/sovereign/kit-keys/{key}.
func (k *KitKeysRoute) Put(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if !SovereignOwner(claims) {
		kitKeyRefuse(w, http.StatusForbidden, "forbidden", "", "a kit key is set by a session of tier owner")
		return
	}
	key := chi.URLParam(r, "key")
	if !k.Settable[key] {
		kitKeyRefuse(w, http.StatusBadRequest, "key-not-settable", "key", fmt.Sprintf("%q is not listed in docs/contracts/c5-kit-keys-settable.txt", key))
		return
	}
	var req struct {
		Value   *string  `json:"value"`
		Regions []string `json:"regions"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKitKeyBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		kitKeyRefuse(w, http.StatusBadRequest, "bad-body", "", err.Error())
		return
	}
	if req.Value == nil || !validKitKeyValue(*req.Value) {
		kitKeyRefuse(w, http.StatusBadRequest, "bad-value", "value", "value is a string without control characters")
		return
	}
	if k.Git == nil {
		kitKeyRefuse(w, http.StatusServiceUnavailable, "git-not-configured", "", "the commit library is not configured on this API")
		return
	}
	ctx := r.Context()
	all, err := gitwrite.Regions(ctx, k.Git, FleetRepo, k.FQDN)
	if err != nil {
		gitRefuse(w, "list the regions", err)
		return
	}
	regions := all
	if len(req.Regions) > 0 {
		regions = nil
		for _, id := range req.Regions {
			if i := sort.SearchStrings(all, id); i == len(all) || all[i] != id {
				kitKeyRefuse(w, http.StatusBadRequest, "unknown-region", "regions", fmt.Sprintf("%q is not a region of %s", id, k.FQDN))
				return
			}
			if i := sort.SearchStrings(regions, id); i == len(regions) || regions[i] != id {
				regions = append(regions[:i], append([]string{id}, regions[i:]...)...)
			}
		}
	}
	if len(regions) == 0 {
		kitKeyRefuse(w, http.StatusConflict, "no-region", "", "the repository holds no region folder of "+k.FQDN)
		return
	}
	scope := gitwrite.RegionPlatform(k.FQDN, []string{"root"}, regions)
	change := gitwrite.Change{Scope: scope, Repo: FleetRepo, Summary: "kit-keys: set " + key, ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaAPI}
	if claims.Email != "" {
		change.Author = gitwrite.Identity{Name: claims.Email, Email: claims.Email}
	}
	paths := []string{}
	for _, id := range regions {
		path := "clusters/" + k.FQDN + "/regions/" + id + "/" + VariablesFile
		content, sha, err := k.Git.Read(ctx, scope, FleetRepo, path)
		if errors.Is(err, gitwrite.ErrNotFound) {
			kitKeyRefuse(w, http.StatusConflict, "region-not-seeded", "", "region "+id+" holds no "+VariablesFile+": its root is not seeded yet")
			return
		} else if err != nil {
			gitRefuse(w, "read "+path, err)
			return
		}
		out, err := setKitKey(content, key, *req.Value, change.ChangeID)
		if err != nil {
			kitKeyRefuse(w, http.StatusConflict, "bad-document", "", path+": "+err.Error())
			return
		}
		change.Files = append(change.Files, gitwrite.FileChange{Path: path, Op: gitwrite.OpUpsert, Content: out, IfMatch: sha})
		paths = append(paths, path)
	}
	res, err := k.Git.Commit(ctx, change)
	if err != nil {
		gitRefuse(w, "commit", err)
		return
	}
	if k.Log != nil {
		k.Log.Info("kit key set by commit", "key", key, "regions", regions, "commit", res.Commit, "committed", res.Committed)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"commit": res.Commit, "paths": paths})
}

// setKitKey returns the document with data[key] set to value and the
// annotation catalyst.openova.io/change-id set to changeID. When data[key]
// already holds value, it returns the document unchanged, so the commit
// library makes no commit.
func setKitKey(content []byte, key, value, changeID string) ([]byte, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	meta, _ := doc["metadata"].(map[string]interface{})
	if doc["apiVersion"] != "v1" || doc["kind"] != "ConfigMap" || meta["name"] != "sovereign-variables" {
		return nil, errors.New("not the ConfigMap sovereign-variables")
	}
	data, _ := doc["data"].(map[string]interface{})
	if current, ok := data[key].(string); ok && current == value {
		return content, nil
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	data[key] = value
	doc["data"] = data
	annotations, _ := meta["annotations"].(map[string]interface{})
	if annotations == nil {
		annotations = map[string]interface{}{}
	}
	annotations["catalyst.openova.io/change-id"] = changeID
	meta["annotations"] = annotations
	return yaml.Marshal(doc)
}
