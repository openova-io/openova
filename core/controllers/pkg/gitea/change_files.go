package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// CommitIdentity is the author or the committer of a ChangeFiles commit.
type CommitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ChangeFile is one file operation of ChangeFiles.
type ChangeFile struct {
	// Operation is "create", "update" or "delete".
	Operation string `json:"operation"`
	// Path is repository-relative, with no leading slash.
	Path string `json:"path"`
	// ContentBase64 is the new content, base64 encoded. Empty for "delete".
	ContentBase64 string `json:"content,omitempty"`
	// SHA is the blob SHA the file has now. Required for "update" and
	// "delete": Gitea refuses the whole commit when it differs.
	SHA string `json:"sha,omitempty"`
}

// ChangeFilesRequest is the body of POST /repos/{owner}/{repo}/contents.
type ChangeFilesRequest struct {
	Branch    string         `json:"branch,omitempty"`
	Message   string         `json:"message"`
	Author    CommitIdentity `json:"author"`
	Committer CommitIdentity `json:"committer"`
	Files     []ChangeFile   `json:"files"`
}

// ChangedFile is one file that a ChangeFiles commit created or updated.
type ChangedFile struct {
	Path string
	SHA  string
}

// ChangeFilesResponse is the answer of ChangeFiles.
type ChangeFilesResponse struct {
	// CommitSHA is the SHA of the new commit.
	CommitSHA string
	// Files holds one entry per created or updated file. A deleted file
	// has no entry.
	Files []ChangedFile
}

// ChangeFiles applies every operation of req in one commit, or none of
// them (POST /repos/{owner}/{repo}/contents, Gitea 1.21 and later).
// Gitea checks each SHA on the server; one mismatch refuses the whole
// commit with HTTP 422. Any answer other than 2xx is returned as
// *HTTPError.
func (c *Client) ChangeFiles(ctx context.Context, org, repo string, req ChangeFilesRequest) (ChangeFilesResponse, error) {
	if org == "" || repo == "" || len(req.Files) == 0 {
		return ChangeFilesResponse{}, errors.New("gitea: ChangeFiles requires non-empty org, repo and files")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/contents", url.PathEscape(org), url.PathEscape(repo))
	var raw struct {
		Files []*struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
		} `json:"files"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if _, _, err := c.do(ctx, http.MethodPost, endpoint, req, &raw); err != nil {
		return ChangeFilesResponse{}, err
	}
	if raw.Commit.SHA == "" {
		return ChangeFilesResponse{}, errors.New("gitea: ChangeFiles: the answer names no commit")
	}
	out := ChangeFilesResponse{CommitSHA: raw.Commit.SHA}
	for _, f := range raw.Files {
		if f != nil && f.Path != "" {
			out.Files = append(out.Files, ChangedFile{Path: f.Path, SHA: f.SHA})
		}
	}
	return out, nil
}

// BranchHead returns the SHA of the commit at the head of branch
// (GET /repos/{owner}/{repo}/branches/{branch}). A missing repository or
// branch returns *HTTPError with Status 404.
func (c *Client) BranchHead(ctx context.Context, org, repo, branch string) (string, error) {
	if org == "" || repo == "" || branch == "" {
		return "", errors.New("gitea: BranchHead requires non-empty org, repo and branch")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/branches/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(branch))
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &out); err != nil {
		return "", err
	}
	if out.Commit.ID == "" {
		return "", errors.New("gitea: BranchHead: the answer names no commit")
	}
	return out.Commit.ID, nil
}
