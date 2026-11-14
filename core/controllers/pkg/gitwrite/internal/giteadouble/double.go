// Package giteadouble is an in-memory stand-in for the Gitea API calls
// that gitwrite makes: read a file, list a directory, read a branch head,
// commit a batch of files, and, for the pull-request mode, create a branch
// and open or list pull requests (pulls.go). It answers as Gitea 1.22 does: the
// batch is checked whole before any file is applied, a wrong SHA is HTTP
// 422 "sha does not match", a create of an existing file is HTTP 422
// "repository file already exists". Every repository and branch exists
// and starts empty, except that the branch-head call answers 404 for a
// branch other than main that nothing has read, written or created yet.
// It is used by the tests of gitwrite and by
// gitwrite/fake; it is not a production server.
package giteadouble

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
)

// Commit is one commit the double made through the batch call.
type Commit struct {
	Owner, Repo, Branch string
	SHA                 string
	Message             string
	AuthorName          string
	AuthorEmail         string
	CommitterName       string
	CommitterEmail      string
	Token               string   // the token the request carried
	Paths               []string // the paths of the batch, in its order
}

type branch struct {
	head  string
	files map[string][]byte
}

// Server is the double. Its zero value is not usable; call New.
type Server struct {
	mu       sync.Mutex
	branches map[string]*branch
	commits  []Commit
	calls    map[string]int
	pulls    []PullRequest

	// Intercept, when set, is called for every request with its method
	// and kind ("file", "branch", "change", "new-branch" or "pull"; a
	// directory listing is kind "file"). A return value
	// other than 0 is sent as the HTTP status and nothing changes.
	Intercept func(method, kind string) int
	// BeforeChange, when set, runs at the start of every batch call,
	// before the batch is checked. It may call Put to play a writer that
	// commits between gitwrite's read and its write.
	BeforeChange func()
}

// New returns an empty double.
func New() *Server {
	return &Server{branches: map[string]*branch{}, calls: map[string]int{}}
}

// BlobSHA is the SHA the double gives content. It is not Git's SHA-1.
func BlobSHA(content []byte) string {
	sum := sha256.Sum256(append([]byte("blob\x00"), content...))
	return hex.EncodeToString(sum[:])[:40]
}

func key(owner, repo, br string) string {
	return owner + "/" + repo + "/" + br
}

// get returns the branch, created empty on first use. s.mu is held.
func (s *Server) get(owner, repo, br string) *branch {
	k := key(owner, repo, br)
	b, ok := s.branches[k]
	if !ok {
		b = &branch{head: BlobSHA([]byte("empty " + k)), files: map[string][]byte{}}
		s.branches[k] = b
	}
	return b
}

// move gives the branch a new head after a change. s.mu is held.
func move(b *branch, what string) {
	b.head = BlobSHA([]byte(b.head + "\x00" + what))
}

// Put writes a file outside the batch call, as another writer would, and
// moves the head. It records no Commit.
func (s *Server) Put(owner, repo, br, p string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(owner, repo, br)
	b.files[p] = append([]byte(nil), content...)
	move(b, "put "+p)
}

// Files returns a copy of the files of a branch.
func (s *Server) Files(owner, repo, br string) map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for p, c := range s.get(owner, repo, br).files {
		out[p] = append([]byte(nil), c...)
	}
	return out
}

// Head returns the head of a branch.
func (s *Server) Head(owner, repo, br string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(owner, repo, br).head
}

// Commits returns a copy of the commits made through the batch call.
func (s *Server) Commits() []Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Commit(nil), s.commits...)
}

// Calls returns how many requests of a kind were answered, refused ones
// included. Kind "" counts every request.
func (s *Server) Calls(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "" {
		n := 0
		for _, c := range s.calls {
			n += c
		}
		return n
	}
	return s.calls[kind]
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func message(w http.ResponseWriter, status int, text string) {
	reply(w, status, map[string]string{"message": text})
}

// ServeHTTP answers the calls under /api/v1/repos/{owner}/{repo}/.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v1/repos/")
	parts := strings.SplitN(rest, "/", 4)
	if !ok || len(parts) < 3 {
		message(w, http.StatusNotFound, "no such route")
		return
	}
	owner, err1 := url.PathUnescape(parts[0])
	repo, err2 := url.PathUnescape(parts[1])
	tail := ""
	if len(parts) == 4 {
		var err error
		if tail, err = url.PathUnescape(parts[3]); err != nil {
			message(w, http.StatusBadRequest, "bad path")
			return
		}
	}
	if err1 != nil || err2 != nil {
		message(w, http.StatusBadRequest, "bad path")
		return
	}
	kind := ""
	switch {
	case parts[2] == "contents" && len(parts) == 3 && r.Method == http.MethodPost:
		kind = "change"
	case parts[2] == "contents" && r.Method == http.MethodGet:
		kind = "file"
	case parts[2] == "branches" && len(parts) == 4 && r.Method == http.MethodGet:
		kind = "branch"
	case parts[2] == "branches" && len(parts) == 3 && r.Method == http.MethodPost:
		kind = "new-branch"
	case parts[2] == "pulls" && len(parts) == 3 && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		kind = "pull"
	default:
		message(w, http.StatusNotFound, "no such route")
		return
	}
	s.mu.Lock()
	s.calls[kind]++
	s.mu.Unlock()
	if s.Intercept != nil {
		if status := s.Intercept(r.Method, kind); status != 0 {
			message(w, status, fmt.Sprintf("intercepted with %d", status))
			return
		}
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "token ")
	if !ok || token == "" {
		message(w, http.StatusUnauthorized, "token is required")
		return
	}
	br := r.URL.Query().Get("ref")
	if br == "" {
		br = "main"
	}
	switch kind {
	case "change":
		s.change(w, r, owner, repo, token)
	case "file":
		s.file(w, owner, repo, br, tail)
	case "branch":
		s.mu.Lock()
		b, ok := s.branches[key(owner, repo, tail)]
		if tail == "main" {
			b, ok = s.get(owner, repo, tail), true
		}
		head := ""
		if ok {
			head = b.head
		}
		s.mu.Unlock()
		if !ok {
			message(w, http.StatusNotFound, "branch does not exist [name: "+tail+"]")
			return
		}
		reply(w, http.StatusOK, map[string]any{"name": tail, "commit": map[string]string{"id": head}})
	case "new-branch":
		s.newBranch(w, r, owner, repo)
	case "pull":
		s.pull(w, r, owner, repo, token)
	}
}

// file answers GET contents: a file object, a directory listing, or 404.
func (s *Server) file(w http.ResponseWriter, owner, repo, br, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(owner, repo, br)
	if content, ok := b.files[p]; ok && p != "" {
		reply(w, http.StatusOK, map[string]any{
			"name": path.Base(p), "path": p, "type": "file", "sha": BlobSHA(content),
			"size": len(content), "content": base64.StdEncoding.EncodeToString(content),
		})
		return
	}
	prefix := ""
	if p != "" {
		prefix = p + "/"
	}
	children := map[string]map[string]any{}
	for fp, content := range b.files {
		rest, ok := strings.CutPrefix(fp, prefix)
		if !ok {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		entry := map[string]any{"name": name, "path": prefix + name, "type": "file", "sha": BlobSHA(content)}
		if isDir {
			entry = map[string]any{"name": name, "path": prefix + name, "type": "dir", "sha": BlobSHA([]byte("tree " + prefix + name))}
		}
		children[name] = entry
	}
	if len(children) == 0 && p != "" {
		message(w, http.StatusNotFound, "object does not exist [rel_path: "+p+"]")
		return
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]map[string]any, 0, len(names))
	for _, name := range names {
		list = append(list, children[name])
	}
	reply(w, http.StatusOK, list)
}

type batch struct {
	Branch    string `json:"branch"`
	Message   string `json:"message"`
	Author    struct{ Name, Email string }
	Committer struct{ Name, Email string }
	Files     []struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Content   string `json:"content"`
		SHA       string `json:"sha"`
	} `json:"files"`
}

// change answers the batch call: all files or none.
func (s *Server) change(w http.ResponseWriter, r *http.Request, owner, repo, token string) {
	var req batch
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		message(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if s.BeforeChange != nil {
		s.BeforeChange()
	}
	br := req.Branch
	if br == "" {
		br = "main"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(owner, repo, br)
	decoded := make([][]byte, len(req.Files))
	for i, f := range req.Files {
		content, exists := b.files[f.Path]
		switch f.Operation {
		case "create":
			if exists {
				message(w, http.StatusUnprocessableEntity, "repository file already exists [path: "+f.Path+"]")
				return
			}
		case "update", "delete":
			if f.SHA == "" {
				message(w, http.StatusUnprocessableEntity, "a SHA must be given [path: "+f.Path+"]")
				return
			}
			if !exists {
				message(w, http.StatusNotFound, "repository file does not exist [path: "+f.Path+"]")
				return
			}
			if f.SHA != BlobSHA(content) {
				message(w, http.StatusUnprocessableEntity, "sha does not match [given: "+f.SHA+", expected: "+BlobSHA(content)+"]")
				return
			}
		default:
			message(w, http.StatusUnprocessableEntity, "unknown operation "+f.Operation)
			return
		}
		if f.Operation != "delete" {
			c, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				message(w, http.StatusBadRequest, "content is not base64 [path: "+f.Path+"]")
				return
			}
			decoded[i] = c
		}
	}
	files := make([]any, 0, len(req.Files))
	paths := make([]string, 0, len(req.Files))
	for i, f := range req.Files {
		paths = append(paths, f.Path)
		if f.Operation == "delete" {
			delete(b.files, f.Path)
			files = append(files, nil)
			continue
		}
		b.files[f.Path] = decoded[i]
		files = append(files, map[string]any{"path": f.Path, "type": "file", "sha": BlobSHA(decoded[i])})
	}
	move(b, "commit "+req.Message+"\x00"+strings.Join(paths, "\x00"))
	s.commits = append(s.commits, Commit{
		Owner: owner, Repo: repo, Branch: br, SHA: b.head, Message: req.Message,
		AuthorName: req.Author.Name, AuthorEmail: req.Author.Email,
		CommitterName: req.Committer.Name, CommitterEmail: req.Committer.Email,
		Token: token, Paths: paths,
	})
	reply(w, http.StatusCreated, map[string]any{"files": files, "commit": map[string]string{"sha": b.head}})
}
