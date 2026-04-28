package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client provides methods to commit files to a GitHub repository via the Git Data API.
type Client struct {
	Token string
	Owner string
	Repo  string
}

// NewClient creates a GitHub API client.
func NewClient(token, owner, repo string) *Client {
	return &Client{Token: token, Owner: owner, Repo: repo}
}

// commitAttemptsMax is how many times CommitFilesWithPrune retries a full
// rebuild (getRef → tree → commit → updateRef) when updateRef returns
// "Update is not a fast forward". Concurrent day-2 installs race at the
// branch-ref level; a clean rebuild against the new HEAD succeeds on the
// next try. 5 attempts handles bursts of ~5 parallel commits.
const commitAttemptsMax = 5

// CommitFiles creates an atomic commit with multiple files on the given branch.
// files maps path (e.g. "clusters/contabo-mkt/tenants/slug/namespace.yaml") to content.
func (c *Client) CommitFiles(ctx context.Context, branch, message string, files map[string]string) error {
	return c.CommitFilesWithPrune(ctx, branch, message, files, nil)
}

// CommitFilesWithPrune is like CommitFiles but deletes any blobs under the given
// prefixes in the existing tree that are NOT present in files. This turns the
// commit into a mirror operation for the managed prefixes — callers use it to
// ensure uninstalled apps no longer have orphan app-*.yaml files hanging around.
//
// prunePrefixes must not be empty strings; each must end with "/" (e.g.,
// "clusters/contabo-mkt/tenants/haty/apps/"). Paths outside the prefixes are
// preserved via base_tree as before.
func (c *Client) CommitFilesWithPrune(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	if len(files) == 0 && len(prunePrefixes) == 0 {
		return fmt.Errorf("no files to commit")
	}

	var lastErr error
	for attempt := 1; attempt <= commitAttemptsMax; attempt++ {
		err := c.commitOnce(ctx, branch, message, files, prunePrefixes)
		if err == nil {
			return nil
		}
		lastErr = err
		// Retry on ref-race: updateRef failed because the branch moved between
		// our getRef and our updateRef. Rebuild the whole tree against the new
		// HEAD on the next attempt. Everything else is fatal.
		if !isFastForwardRejection(err) {
			return err
		}
		slog.Warn("commit: ref moved under us — retrying with fresh HEAD",
			"attempt", attempt, "branch", branch, "error", err)
	}
	return fmt.Errorf("commit: ref-race persisted after %d attempts: %w", commitAttemptsMax, lastErr)
}

// isFastForwardRejection returns true iff the wrapped error is GitHub's
// "Update is not a fast forward" 422 from the update-a-reference endpoint.
// Keeping the check on the textual payload is intentional — the REST API's
// response body is the canonical signal here, not a distinct HTTP status.
func isFastForwardRejection(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return (contains(s, "not a fast forward") || contains(s, "Reference cannot be updated")) &&
		contains(s, "update ref")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	// naive; strings.Contains would work but avoids importing strings twice.
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// commitOnce runs a single getRef → tree → commit → updateRef cycle.
// Separated from CommitFilesWithPrune so the retry loop above can drive it.
func (c *Client) commitOnce(ctx context.Context, branch, message string, files map[string]string, prunePrefixes []string) error {
	// 1. Get the current branch ref SHA.
	refSHA, err := c.getRef(ctx, branch)
	if err != nil {
		return fmt.Errorf("get ref: %w", err)
	}
	slog.Debug("got branch ref", "branch", branch, "sha", refSHA)

	// 2. Get the commit's tree SHA.
	treeSHA, err := c.getCommitTree(ctx, refSHA)
	if err != nil {
		return fmt.Errorf("get commit tree: %w", err)
	}

	// 3. Create blobs for each file and build tree entries.
	var treeEntries []treeEntry
	for path, content := range files {
		blobSHA, blobErr := c.createBlob(ctx, content)
		if blobErr != nil {
			return fmt.Errorf("create blob for %s: %w", path, blobErr)
		}
		treeEntries = append(treeEntries, treeEntry{
			Path: path,
			Mode: "100644",
			Type: "blob",
			SHA:  blobSHA,
		})
	}

	// 3b. Walk the existing tree for each prune prefix and mark orphan paths
	// for deletion. A path is orphan when it's under a managed prefix but not
	// in `files`. Delete entries use sha=null (encoded as omitempty null).
	if len(prunePrefixes) > 0 {
		existing, listErr := c.listTreeBlobs(ctx, refSHA, prunePrefixes)
		if listErr != nil {
			return fmt.Errorf("list existing tree: %w", listErr)
		}
		for _, path := range existing {
			if _, keep := files[path]; keep {
				continue
			}
			treeEntries = append(treeEntries, treeEntry{
				Path:   path,
				Mode:   "100644",
				Type:   "blob",
				Delete: true,
			})
		}
	}

	// 4. Create a new tree.
	newTreeSHA, err := c.createTree(ctx, treeSHA, treeEntries)
	if err != nil {
		return fmt.Errorf("create tree: %w", err)
	}

	// 5. Create the commit.
	commitSHA, err := c.createCommit(ctx, message, newTreeSHA, refSHA)
	if err != nil {
		return fmt.Errorf("create commit: %w", err)
	}

	// 6. Update the branch ref.
	if err := c.updateRef(ctx, branch, commitSHA); err != nil {
		return fmt.Errorf("update ref: %w", err)
	}

	slog.Info("committed files to GitHub",
		"branch", branch,
		"commit", commitSHA,
		"files", len(files),
		"prune_prefixes", len(prunePrefixes),
	)
	return nil
}

// listTreeBlobs returns all blob paths in commitSHA's tree that begin with any
// of the given prefixes. Uses the recursive tree API for efficiency.
func (c *Client) listTreeBlobs(ctx context.Context, commitSHA string, prefixes []string) ([]string, error) {
	treeSHA, err := c.getCommitTree(ctx, commitSHA)
	if err != nil {
		return nil, err
	}
	body, err := c.doRequest(ctx, http.MethodGet,
		c.apiURL("/git/trees/"+treeSHA+"?recursive=1"), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Truncated {
		slog.Warn("git tree listing was truncated — some orphan files may survive", "commit", commitSHA)
	}
	var out []string
	for _, entry := range resp.Tree {
		if entry.Type != "blob" {
			continue
		}
		for _, pfx := range prefixes {
			if strings.HasPrefix(entry.Path, pfx) {
				out = append(out, entry.Path)
				break
			}
		}
	}
	return out, nil
}

// --- GitHub Git Data API types ---

type treeEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Type   string `json:"type"`
	SHA    string `json:"-"`
	Delete bool   `json:"-"`
}

// MarshalJSON emits sha:null for delete entries (GitHub's Git Data API uses
// sha=null to indicate "remove this path from the tree") and a string SHA
// otherwise. Using the default struct marshaler would emit empty strings,
// which GitHub rejects.
func (t treeEntry) MarshalJSON() ([]byte, error) {
	base := map[string]any{
		"path": t.Path,
		"mode": t.Mode,
		"type": t.Type,
	}
	if t.Delete {
		base["sha"] = nil
	} else {
		base["sha"] = t.SHA
	}
	return json.Marshal(base)
}

func (c *Client) apiURL(path string) string {
	return fmt.Sprintf("https://api.github.com/repos/%s/%s%s", c.Owner, c.Repo, path)
}

func (c *Client) doRequest(ctx context.Context, method, url string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API %s %s: %d %s", method, url, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func (c *Client) getRef(ctx context.Context, branch string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodGet, c.apiURL("/git/ref/heads/"+branch), nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.Object.SHA, nil
}

func (c *Client) getCommitTree(ctx context.Context, commitSHA string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodGet, c.apiURL("/git/commits/"+commitSHA), nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.Tree.SHA, nil
}

func (c *Client) createBlob(ctx context.Context, content string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/blobs"), map[string]string{
		"content":  content,
		"encoding": "utf-8",
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) createTree(ctx context.Context, baseTreeSHA string, entries []treeEntry) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/trees"), map[string]any{
		"base_tree": baseTreeSHA,
		"tree":      entries,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) createCommit(ctx context.Context, message, treeSHA, parentSHA string) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, c.apiURL("/git/commits"), map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.SHA, nil
}

func (c *Client) updateRef(ctx context.Context, branch, commitSHA string) error {
	_, err := c.doRequest(ctx, http.MethodPatch, c.apiURL("/git/refs/heads/"+branch), map[string]any{
		"sha":   commitSHA,
		"force": false,
	})
	return err
}

// ReadFile reads a file's content from the repository at the given branch.
func (c *Client) ReadFile(ctx context.Context, branch, path string) (string, error) {
	url := c.apiURL(fmt.Sprintf("/contents/%s?ref=%s", path, branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github.raw+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("file not found: %s", path)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API: %d %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
