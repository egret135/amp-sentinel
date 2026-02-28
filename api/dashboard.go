package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// dashboardHandler returns an http.Handler that serves the embedded dashboard files.
func dashboardHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("embed web fs: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
