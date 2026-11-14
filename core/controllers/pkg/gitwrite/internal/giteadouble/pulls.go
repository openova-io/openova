package giteadouble

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// URLBase starts the address of every pull request the double opens:
// URLBase + "<owner>/<repo>/pulls/<number>".
const URLBase = "https://gitea.double.invalid/"

// PullRequest is one pull request the double opened. It is never merged.
type PullRequest struct {
	Owner, Repo, Title, Body string
	Head, Base, URL, Token   string // Token is the token the request carried
	Number                   int64
}

// PullRequests returns a copy of the pull requests opened, oldest first.
func (s *Server) PullRequests() []PullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PullRequest(nil), s.pulls...)
}

// newBranch answers POST branches as Gitea does: new_branch_name starts at
// the head of old_branch_name ("main" when empty) with the same files. An
// existing name is HTTP 409; a missing old branch other than main is 404.
func (s *Server) newBranch(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var req struct {
		New string `json:"new_branch_name"`
		Old string `json:"old_branch_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.New == "" {
		message(w, http.StatusUnprocessableEntity, "new_branch_name is required")
		return
	}
	if req.Old == "" {
		req.Old = "main"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.branches[key(owner, repo, req.New)]; ok {
		message(w, http.StatusConflict, "branch already exists [name: "+req.New+"]")
		return
	}
	src, ok := s.branches[key(owner, repo, req.Old)]
	if !ok && req.Old == "main" {
		src, ok = s.get(owner, repo, req.Old), true
	}
	if !ok {
		message(w, http.StatusNotFound, "branch does not exist [name: "+req.Old+"]")
		return
	}
	nb := &branch{head: src.head, files: map[string][]byte{}}
	for p, c := range src.files {
		nb.files[p] = append([]byte(nil), c...)
	}
	s.branches[key(owner, repo, req.New)] = nb
	reply(w, http.StatusCreated, map[string]any{"name": req.New, "commit": map[string]string{"id": nb.head}})
}

// pull answers the pull-request calls as Gitea does. GET lists the open
// pull requests of the repository. POST opens one from head into base; a
// missing head branch is HTTP 404, an open one with the same head and base
// is HTTP 409.
func (s *Server) pull(w http.ResponseWriter, r *http.Request, owner, repo, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet {
		list := []map[string]any{}
		for _, p := range s.pulls {
			if p.Owner == owner && p.Repo == repo {
				list = append(list, pullJSON(p))
			}
		}
		reply(w, http.StatusOK, list)
		return
	}
	var p PullRequest
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Title == "" || p.Head == "" || p.Base == "" {
		message(w, http.StatusUnprocessableEntity, "title, head and base are required")
		return
	}
	if _, ok := s.branches[key(owner, repo, p.Head)]; !ok {
		message(w, http.StatusNotFound, "branch does not exist [name: "+p.Head+"]")
		return
	}
	for _, o := range s.pulls {
		if o.Owner == owner && o.Repo == repo && o.Head == p.Head && o.Base == p.Base {
			message(w, http.StatusConflict, "pull request already exists for these targets")
			return
		}
	}
	p.Owner, p.Repo, p.Token, p.Number = owner, repo, token, int64(len(s.pulls)+1)
	p.URL = fmt.Sprintf("%s%s/%s/pulls/%d", URLBase, owner, repo, p.Number)
	s.pulls = append(s.pulls, p)
	reply(w, http.StatusCreated, pullJSON(p))
}

// pullJSON is a pull request as the Gitea API shows it.
func pullJSON(p PullRequest) map[string]any {
	return map[string]any{
		"number": p.Number, "html_url": p.URL, "state": "open", "title": p.Title, "body": p.Body,
		"head": map[string]string{"ref": p.Head}, "base": map[string]string{"ref": p.Base},
	}
}
