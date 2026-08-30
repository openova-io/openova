package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiHandler serves the embedded UI build: real files as-is, every other path
// falls back to index.html so a browser router can own the URL space.
func (h *Handler) uiHandler() http.Handler {
	if h.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not bundled", http.StatusNotFound)
		})
	}
	files := http.FS(h.UI)
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p != "/" {
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
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
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
