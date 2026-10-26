// Package web holds the files the webserver serves from the repository's web/ directory,
// embedded into every binary that imports internal/web: the page templates and the favicon.
// A binary therefore renders the templates it was built with, from any working directory.
// robots.txt is deliberately not embedded: operators edit web/robots.txt on disk, and the
// webserver falls back to an inline version when it is missing.
package web

import "embed"

// FS holds templates/*.html and favicon.ico.
//
//go:embed templates/*.html favicon.ico
var FS embed.FS
