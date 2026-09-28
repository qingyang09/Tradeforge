package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS 是 PWA 需要的固定路径静态资源（manifest.json/sw.js/图标）——一个独立于
// templatesFS 的 embed.FS，不并进去：templatesFS 专门服务于 html/template.ParseFS，
// 把非 HTML 的原始字节文件混进去既别扭又容易踩坑（要么被误当模板解析，要么要在
// glob 模式上做取舍），两个各司其职的 embed.FS 更符合这个项目一贯"小而专一"的风格。
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
