// Package handlers serves the static assets.
package handlers

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
)

// assets is embedded rather than read from disk so the binary is the whole
// deployment: no working directory to get right, and no chance of the image
// carrying a binary and the assets of a different build.
//
//go:embed all:files
var assets embed.FS

// Handler serves the static assets.
type Handler struct {
	files http.Handler
}

// New creates the handler.
func New(_ context.Context) (*Handler, error) {
	files, err := fs.Sub(assets, "files")
	if err != nil {
		return nil, err
	}
	return &Handler{files: http.FileServerFS(files)}, nil
}

// Mount registers the asset routes.
//
// The favicon and robots.txt are served from the root as well as from /static,
// because that is where a browser and a crawler look for them.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.StripPrefix("/static/", h.files))
	mux.Handle("GET /favicon.svg", h.files)
	mux.Handle("GET /robots.txt", h.files)
}
