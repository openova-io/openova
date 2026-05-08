// gitea/contents.go — minimal "contents API" surface for writing a file
// at a known path via the Gitea HTTP API. Used by organization-controller
// to materialize the vCluster HelmRelease into the per-Org Gitea repo.
//
// We deliberately avoid `git clone/push` here: organization-controller
// is a single-purpose binary, the repo content is one tiny HelmRelease
// per Org, and the Gitea contents endpoint is idempotent (PUT with the
// existing SHA succeeds with no diff). This keeps the controller image
// minimal — alpine + the binary, no git/openssh.
//
// Endpoints used:
//
//	GET    /api/v1/repos/{owner}/{repo}/contents/{path}
//	POST   /api/v1/repos/{owner}/{repo}/contents/{path}    (create file)
//	PUT    /api/v1/repos/{owner}/{repo}/contents/{path}    (update file)
//
// Per Gitea API: the response includes a `sha` field on read; the same
// `sha` must be passed on update for optimistic locking. Create has no
// `sha`. We surface a single PutFile that picks POST/PUT based on
// whether the file exists.

package gitea

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// FileContent is the relevant subset of Gitea's ContentsResponse.
type FileContent struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Type    string `json:"type"` // "file" | "dir" | "symlink" | "submodule"
	Content string `json:"content,omitempty"` // base64 when Type=file
}

type contentsCreateUpdate struct {
	Message       string `json:"message"`
	Content       string `json:"content"` // base64
	Branch        string `json:"branch,omitempty"`
	NewBranch     string `json:"new_branch,omitempty"`
	SHA           string `json:"sha,omitempty"`
	CommitterName string `json:"-"`
}

// GetFile fetches the file at path on the given branch. Returns
// ErrFileNotFound on 404. Decodes the base64 content for the caller.
func (c *Client) GetFile(ctx context.Context, owner, repo, path, branch string) (FileContent, []byte, error) {
	u := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s",
		c.addr, owner, repo, strings.TrimLeft(path, "/"))
	if branch != "" {
		u += "?ref=" + branch
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return FileContent{}, nil, err
	}
	c.auth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return FileContent{}, nil, fmt.Errorf("gitea: GET contents: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var f FileContent
		if err := json.Unmarshal(body, &f); err != nil {
			return FileContent{}, nil, fmt.Errorf("gitea: decode contents: %w", err)
		}
		if f.Type != "file" {
			return f, nil, fmt.Errorf("gitea: GET contents: path is %q not file", f.Type)
		}
		raw, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return f, nil, fmt.Errorf("gitea: decode base64 contents: %w", err)
		}
		return f, raw, nil
	case http.StatusNotFound:
		return FileContent{}, nil, ErrFileNotFound
	default:
		return FileContent{}, nil, fmt.Errorf("gitea: GET contents %d: %s", resp.StatusCode, body)
	}
}

// ErrFileNotFound is returned by GetFile when the path does not exist
// in the repo's branch.
var ErrFileNotFound = errors.New("gitea: file not found")

// PutFile creates the file if absent or updates it if present. Returns
// the new FileContent (with the post-write SHA). If the existing
// content matches `data` byte-for-byte, the call returns the existing
// FileContent without issuing a write — this is what makes the
// reconciler idempotent on a steady-state CR.
func (c *Client) PutFile(ctx context.Context, owner, repo, path, branch string, data []byte, message string) (FileContent, error) {
	existing, currentBytes, err := c.GetFile(ctx, owner, repo, path, branch)
	if err != nil && !errors.Is(err, ErrFileNotFound) {
		return FileContent{}, fmt.Errorf("gitea.PutFile: get: %w", err)
	}

	if err == nil {
		// File exists. If contents match exactly, no-op.
		if bytes.Equal(currentBytes, data) {
			return existing, nil
		}
		// Update via PUT with the existing SHA.
		return c.contentsWrite(ctx, http.MethodPut, owner, repo, path, branch, existing.SHA, data, message)
	}
	// File absent — create via POST.
	return c.contentsWrite(ctx, http.MethodPost, owner, repo, path, branch, "", data, message)
}

func (c *Client) contentsWrite(ctx context.Context, method, owner, repo, path, branch, sha string, data []byte, message string) (FileContent, error) {
	if message == "" {
		message = fmt.Sprintf("controller: write %s", path)
	}
	body, err := json.Marshal(contentsCreateUpdate{
		Message: message,
		Content: base64.StdEncoding.EncodeToString(data),
		Branch:  branch,
		SHA:     sha,
	})
	if err != nil {
		return FileContent{}, err
	}
	u := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s",
		c.addr, owner, repo, strings.TrimLeft(path, "/"))
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return FileContent{}, err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return FileContent{}, fmt.Errorf("gitea: %s contents: %w", method, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		// Gitea wraps the file inside `content` on write responses.
		var wrapped struct {
			Content FileContent `json:"content"`
		}
		if err := json.Unmarshal(respBody, &wrapped); err != nil {
			// Some Gitea versions return the FileContent directly.
			var direct FileContent
			if jerr := json.Unmarshal(respBody, &direct); jerr == nil {
				return direct, nil
			}
			return FileContent{}, fmt.Errorf("gitea: decode write response: %w", err)
		}
		return wrapped.Content, nil
	default:
		return FileContent{}, fmt.Errorf("gitea: %s contents %d: %s", method, resp.StatusCode, respBody)
	}
}
