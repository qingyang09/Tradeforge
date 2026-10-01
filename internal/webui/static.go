package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS holds the PWA's fixed-path static assets (manifest.json/sw.js/
// icons) -- an embed.FS separate from templatesFS, not merged into it:
// templatesFS exists specifically to feed html/template.ParseFS, and mixing
// raw non-HTML byte files into it is both awkward and easy to get wrong
// (either mistakenly parsed as a template, or forcing a compromise on the
// glob pattern). Two single-purpose embed.FS values matches this project's
// consistent "small and focused" style better.
//
//go:embed static
var staticFS embed.FS

func serveEmbedded(w http.ResponseWriter, name string) {
	data, err := fs.ReadFile(staticFS, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	serveEmbedded(w, "static/manifest.json")
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	serveEmbedded(w, "static/sw.js")
}

func (s *Server) handleIcon192(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	serveEmbedded(w, "static/icon-192.png")
}

func (s *Server) handleIcon512(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	serveEmbedded(w, "static/icon-512.png")
}
