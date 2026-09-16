package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed static/*
var EmbeddedStaticFS embed.FS

// UseEmbeddedStatic returns true if embedded static files are available
func UseEmbeddedStatic() bool {
	return EmbeddedStaticFS != (embed.FS{})
}

// ListEmbeddedFiles returns a list of all embedded static files for debugging
func ListEmbeddedFiles() ([]string, error) {
	var files []string
	err := fs.WalkDir(EmbeddedStaticFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
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

// EmbeddedFileHandler returns a Gin handler for serving a single embedded file
func EmbeddedFileHandler(filePath string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if EmbeddedStaticFS == (embed.FS{}) {
			// Fall back to regular file serving
			c.File(filePath)
			return
		}

		// Try to read the file from embedded filesystem
		content, err := fs.ReadFile(EmbeddedStaticFS, filePath)
		if err != nil {
			// Fall back to regular file serving
			c.File(filePath)
			return
		}

		// Determine content type based on file extension
		contentType := staticContentType(filePath)
		c.Header("Content-Type", contentType)
		c.Data(http.StatusOK, contentType, content)
	}
}

// staticContentType returns the MIME type for a static file path. The types of the file kinds this
// project ships are fixed here, so they do not depend on the system MIME database (which maps .ico
// to text/plain on some hosts); anything else falls back to mime.TypeByExtension and finally to
// application/octet-stream. The old implementation compared the last 4 bytes of the path, so .js
// never matched and .json/.map/.webp were unknown.
func staticContentType(filePath string) string {
	ext := strings.ToLower(path.Ext(filePath))
	switch ext {
	case ".ico":
		return "image/x-icon"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
