package web

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// templateDir is the cwd-relative directory all page templates are loaded from.
const templateDir = "web/templates/"

// htmlContentType is the Content-Type of every rendered page.
const htmlContentType = "text/html; charset=utf-8"

// maxPooledBufferSize: buffers that grew beyond this are dropped instead of returned to bufPool,
// so one huge page does not pin its memory for the lifetime of the process.
const maxPooledBufferSize = 1 << 20

// tmplCache holds parsed template sets.
// Key: [funcMapKey] + name + "\x00" + strings.Join(files, "\x00").
var tmplCache sync.Map

// publicErrorDetail is the error detail visitors are shown. Internal text (SQLite errors, file
// paths, template errors) never reaches the page; it goes to the log instead.
const publicErrorDetail = "Please try again later."

// bufPool provides the buffers pages are rendered into before they are written.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// devTemplates reports whether the template cache is disabled (templates are re-parsed on
// every request). It is read on every call so it can be toggled without a restart.
func devTemplates() bool { return os.Getenv("PUGLEAF_DEV_TEMPLATES") == "1" }

// funcMapKey identifies a FuncMap by the address of its map header, so two call sites with
// different FuncMaps get different cache entries even with the same name and files.
// Every FuncMap passed to a cached load must therefore be a package-level map (today only
// adminTemplateFuncs): one built per call would fill the cache, and a later map could land on
// the address of a collected one and hit its entry.
func funcMapKey(funcs template.FuncMap) string {
	return fmt.Sprintf("funcs@%x\x01", reflect.ValueOf(funcs).Pointer())
}

// tmplCacheKey builds the cache key of a template set. Sets parsed with a FuncMap get a
// separate key space per FuncMap, so a call site never shares an entry with one that uses
// another FuncMap or none.
func tmplCacheKey(name string, funcs template.FuncMap, files []string) string {
	var b strings.Builder
	if funcs != nil {
		b.WriteString(funcMapKey(funcs))
	}
	b.WriteString(name)
	b.WriteByte(0)
	b.WriteString(strings.Join(files, "\x00"))
	return b.String()
}

// loadTemplates returns the template set parsed from files, parsing it only once unless
// PUGLEAF_DEV_TEMPLATES=1. name is the name of the (empty) root template and part of the
// cache key; templates are executed by file base name. Parse errors are returned, never cached.
func loadTemplates(name string, funcs template.FuncMap, files ...string) (*template.Template, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("loadTemplates %q: no files", name)
	}
	dev := devTemplates()
	key := tmplCacheKey(name, funcs, files)
	if !dev {
		if v, ok := tmplCache.Load(key); ok {
			return v.(*template.Template), nil
		}
	}
	t := template.New(name)
	if funcs != nil {
		t = t.Funcs(funcs)
	}
	t, err := t.ParseFiles(files...)
	if err != nil {
		return nil, err
	}
	if dev {
		return t, nil
	}
	// Concurrent first requests may parse the same set; all of them use the stored one.
	v, _ := tmplCache.LoadOrStore(key, t)
	return v.(*template.Template), nil
}

// templatePaths prefixes each template file name with templateDir.
func templatePaths(names []string) []string {
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = templateDir + n
	}
	return paths
}

// writeTemplateSet loads the set (name, funcs, files under web/templates), executes exec into a
// pooled buffer and, only when that succeeded, writes the page with status. On error nothing has
// been written to the response.
func writeTemplateSet(c *gin.Context, status int, name string, funcs template.FuncMap, exec string, data any, files ...string) error {
	tmpl, err := loadTemplates(name, funcs, templatePaths(files)...)
	if err != nil {
		return fmt.Errorf("load templates %v: %w", files, err)
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= maxPooledBufferSize {
			buf.Reset()
			bufPool.Put(buf)
		}
	}()
	if err := tmpl.ExecuteTemplate(buf, exec, data); err != nil {
		return fmt.Errorf("execute %s %v: %w", exec, files, err)
	}
	// c.Data copies the bytes to the connection before returning, so the buffer can be reused.
	c.Data(status, htmlContentType, buf.Bytes())
	return nil
}

// renderPage renders "base.html" with the given template files (names inside web/templates,
// base.html is added first) and writes it with status. A load or execute error is logged and
// answered with the 500 error page; no partial page is ever written.
func (s *WebServer) renderPage(c *gin.Context, status int, data any, files ...string) {
	s.renderTemplateSet(c, status, "page", nil, "base.html", data, append([]string{"base.html"}, files...)...)
}

// renderTemplateSet is renderPage for sets with another root template or a FuncMap. files are
// names inside web/templates and must include the file that defines exec.
func (s *WebServer) renderTemplateSet(c *gin.Context, status int, name string, funcs template.FuncMap, exec string, data any, files ...string) {
	if err := writeTemplateSet(c, status, name, funcs, exec, data, files...); err != nil {
		log.Printf("[WEB]: renderTemplateSet: template error: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Template error", publicErrorDetail)
	}
}
