package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NewSPAHandler menyajikan aset statis dari distDir dengan fallback ke index.html,
// tanpa mencegat route /api/ atau /metrics.
func NewSPAHandler(distDir string) http.Handler {
	absRoot, err := filepath.Abs(distDir)
	if err != nil {
		absRoot = distDir
	}
	fileServer := http.FileServer(http.Dir(absRoot))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
			WriteError(w, http.StatusNotFound, "Endpoint tidak ditemukan")
			return
		}

		cleanRel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if cleanRel != "" && !strings.Contains(cleanRel, "..") {
			candidate := filepath.Join(absRoot, filepath.FromSlash(cleanRel))
			// Cegah path traversal keluar dari distDir.
			rel, relErr := filepath.Rel(absRoot, candidate)
			if relErr == nil && !strings.HasPrefix(rel, "..") {
				if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}

		indexPath := filepath.Join(absRoot, "index.html")
		if _, err := os.Stat(indexPath); err != nil {
			WriteError(w, http.StatusNotFound, "Frontend SPA belum di-build. Jalankan build di direktori frontend/.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, indexPath)
	})
}

// HasSPABundle memeriksa apakah file index.html hasil build SPA tersedia di distDir.
func HasSPABundle(distDir string) bool {
	if strings.TrimSpace(distDir) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(distDir, "index.html"))
	return err == nil && !info.IsDir()
}
