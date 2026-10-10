package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
)

// The showcase ladder's ICONS AND BRANDING (DESIGN.md §22.10). The SVGs are
// vendored in icons/ with their sources in icons/LICENSES.md, and
// icons/manifest.json maps each feature, floor item, group and package to
// its icon and colours — the same file scripts/apply-package-icons.py reads,
// so the seeder and the script cannot disagree. Everything goes through the
// product's API: the icons are uploaded by POST /icons (content-addressed, so
// a second upload of the same bytes writes nothing), a feature's icon rides
// on its create or PATCH, a package's on its settings, a group's on PUT
// /feature-groups/{key} — each written only when it differs.

//go:embed icons/*.svg icons/manifest.json
var showcaseIconFS embed.FS

// iconManifest is icons/manifest.json.
type iconManifest struct {
	Features map[string]struct {
		Icon string `json:"icon"`
		BG   string `json:"bg"`
	} `json:"features"`
	Groups   map[string]string `json:"groups"`
	Packages map[string]struct {
		Icon   string `json:"icon"`
		Accent string `json:"accent"`
		Badge  string `json:"badge"`
	} `json:"packages"`
}

func loadIconManifest() (iconManifest, error) {
	var m iconManifest
	b, err := showcaseIconFS.ReadFile("icons/manifest.json")
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("icons/manifest.json: %w", err)
	}
	return m, nil
}

// files lists every icon file the manifest names, once, sorted.
func (m iconManifest) files() []string {
	seen := map[string]bool{}
	for _, f := range m.Features {
		seen[f.Icon] = true
	}
	for _, f := range m.Groups {
		seen[f] = true
	}
	for _, p := range m.Packages {
		if p.Icon != "" {
			seen[p.Icon] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		if f != "" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// showcaseIconIDs is the content address of every vendored icon, keyed by
// file name — what the purge matches the icons this tool uploaded by.
func showcaseIconIDs() (map[string]string, error) {
	entries, err := showcaseIconFS.ReadDir("icons")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) < 5 || e.Name()[len(e.Name())-4:] != ".svg" {
			continue
		}
		b, err := showcaseIconFS.ReadFile("icons/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out[e.Name()] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// uploadIcon is POST /api/v1/icons with the raw bytes.
func (c *client) uploadIcon(contentType string, b []byte) (string, bool, error) {
	req, err := http.NewRequest("POST", c.base+"/api/v1/icons", bytes.NewReader(b))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.email != "" && c.header != "" {
		req.Header.Set(c.header, c.email)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cb_session", Value: c.cookie})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("POST /api/v1/icons: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", false, &errStatus{Code: resp.StatusCode, Method: "POST", Path: "/api/v1/icons", Body: string(raw)}
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", false, fmt.Errorf("POST /api/v1/icons: decode response: %w", err)
	}
	return out.ID, resp.StatusCode == http.StatusCreated, nil
}

func (c *client) putFeatureGroup(key, iconID string) error {
	return c.do("PUT", "/api/v1/feature-groups/"+url.PathEscape(key), map[string]any{"icon_id": iconID}, nil)
}

// ensureShowcaseIcons uploads every icon the manifest names that BSS does not
// hold yet and returns the manifest with the id of each file.
func (s *seeder) ensureShowcaseIcons() (iconManifest, map[string]string, error) {
	m, err := loadIconManifest()
	if err != nil {
		return m, nil, err
	}
	var have struct {
		Icons []struct {
			ID string `json:"id"`
		} `json:"icons"`
	}
	if err := s.api.do("GET", "/api/v1/icons", nil, &have); err != nil {
		return m, nil, fmt.Errorf("list icons: %w", err)
	}
	stored := map[string]bool{}
	for _, ic := range have.Icons {
		stored[ic.ID] = true
	}
	ids := map[string]string{}
	uploaded := 0
	for _, f := range m.files() {
		b, err := showcaseIconFS.ReadFile("icons/" + f)
		if err != nil {
			return m, nil, fmt.Errorf("icons/%s: %w", f, err)
		}
		sum := sha256.Sum256(b)
		id := hex.EncodeToString(sum[:])
		ids[f] = id
		if stored[id] {
			continue
		}
		got, _, err := s.api.uploadIcon("image/svg+xml", b)
		if err != nil {
			return m, nil, fmt.Errorf("upload icons/%s: %w", f, err)
		}
		if got != id {
			return m, nil, fmt.Errorf("upload icons/%s: the product stored it as %s, not its sha256 %s", f, got, id)
		}
		uploaded++
	}
	s.infof("packages: %d icon(s) uploaded, %d already stored", uploaded, len(ids)-uploaded)
	return m, ids, nil
}

// ensureGroupIcons gives each group the manifest's icon, only where it differs.
func (s *seeder) ensureGroupIcons(m iconManifest, ids map[string]string) error {
	var list struct {
		Groups []struct {
			Key    string `json:"key"`
			IconID string `json:"icon_id"`
		} `json:"groups"`
	}
	if err := s.api.do("GET", "/api/v1/features", nil, &list); err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	written := 0
	for _, g := range list.Groups {
		file, ok := m.Groups[g.Key]
		if !ok || ids[file] == g.IconID {
			continue
		}
		if err := s.api.putFeatureGroup(g.Key, ids[file]); err != nil {
			return fmt.Errorf("icon of group %s: %w", g.Key, err)
		}
		written++
	}
	s.infof("packages: %d group icon(s) written, the rest already as the manifest has them", written)
	return nil
}
