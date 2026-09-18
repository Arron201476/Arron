package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type spaHandler struct {
	root string
}

func newSPAHandler(root string) http.Handler {
	return spaHandler{root: filepath.Clean(root)}
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
		http.NotFound(w, r)
		return
	}
	relative := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	target := filepath.Join(h.root, filepath.FromSlash(relative))
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		target = filepath.Join(h.root, "index.html")
	}
	if _, err := os.Stat(target); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, target)
}
