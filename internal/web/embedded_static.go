package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed static/*
var EmbeddedStaticFS embed.FS

// UseEmbeddedStatic returns true if embedded static files are available
func UseEmbeddedStatic() bool {
	return EmbeddedStaticFS != (embed.FS{})
}

// EmbeddedStaticHandler returns a Gin handler for serving embedded static files
func EmbeddedStaticHandler(prefix string) gin.HandlerFunc {
	// Create a sub-filesystem for the static files
	staticFS, err := fs.Sub(EmbeddedStaticFS, "static")
	if err != nil {
		panic("Failed to create embedded static filesystem: " + err.Error())
	}

	// Create an HTTP filesystem handler
	fileServer := http.FileServer(http.FS(staticFS))

	return func(c *gin.Context) {
		// Strip the URL path prefix to get the file path
		path := strings.TrimPrefix(c.Request.URL.Path, prefix)
		if path == "" || path == "/" {
			// Static directory has no index file, return 404
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		// Update the request URL path for the file server
		c.Request.URL.Path = path

		// Set some cache headers for static content
		c.Header("Cache-Control", "public, max-age=3600") // browser caches an hour

		// Serve the file
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
}
