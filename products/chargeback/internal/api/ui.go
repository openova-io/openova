package api

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// uiHandler serves the embedded UI build: real files as-is, every other path
// falls back to index.html so a browser router can own the URL space.
//
// index.html is not served from the file server: it is read once and STAMPED
// with this build (see build.go), so the page that comes back knows which
// bundle it is. Everything else — including the immutable /assets/* caching —
// is unchanged.
func (h *Handler) uiHandler() http.Handler {
	if h.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not bundled", http.StatusNotFound)
		})
	}
	files := http.FS(h.UI)
	fileServer := http.FileServer(files)
	shell := h.shellHTML()
	serveShell := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if shell == nil {
			// No readable index.html to stamp; serve whatever the file
			// server makes of "/" rather than answering nothing.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(shell)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write(shell)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p != "/" && p != "/index.html" {
			if f, err := h.UI.Open(strings.TrimPrefix(p, "/")); err == nil {
				st, serr := f.Stat()
				f.Close()
				if serr == nil && !st.IsDir() {
					if strings.HasPrefix(p, "/assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		serveShell(w, r)
	})
}

// shellHTML reads index.html once and substitutes this build into the
// placeholder ui/index.html carries. An empty Version substitutes nothing,
// which leaves the meta empty and the console's check dormant — a binary that
// does not know its own version must not tell a user their page is stale.
func (h *Handler) shellHTML() []byte {
	b, err := fs.ReadFile(h.UI, "index.html")
	if err != nil {
		return nil
	}
	if h.Version == "" {
		return b
	}
	return bytes.ReplaceAll(b, []byte(buildPlaceholder), []byte(html.EscapeString(h.Version)))
}

// UIFromDist returns the "dist" subtree of an embedded FS, or nil when the
// build is absent.
func UIFromDist(root fs.FS) fs.FS {
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
