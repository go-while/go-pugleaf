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
		contentType := getContentType(filePath)
		c.Header("Content-Type", contentType)
		c.Data(http.StatusOK, contentType, content)
	}
}

// getContentType returns the appropriate MIME type for common file extensions
func getContentType(filePath string) string {
	if len(filePath) < 4 {
		return "application/octet-stream"
	}

	ext := filePath[len(filePath)-4:]
	switch ext {
	case ".ico":
		return "image/x-icon"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".png":
		return "image/png"
	case ".jpg", "jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case "woff":
		return "font/woff"
	case "off2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case "html":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}
