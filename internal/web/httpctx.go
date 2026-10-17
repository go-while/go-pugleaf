package web

// The request context of the web server: a thin layer over net/http that keeps the method names
// (and the semantics the handlers rely on) of the gin context it replaces, so handlers convert by
// text substitution. See httprouter.go for the router and httpclientip.go for ClientIP.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// H is a shortcut for map[string]any, used for JSON answers and template data.
type H map[string]any

// HandlerFunc is a handler or a middleware of the router.
type HandlerFunc func(*Context)

const (
	// abortIndex is the chain index Abort sets; Next never runs a handler at or after it.
	abortIndex = math.MaxInt >> 1
	// httpxNoWritten is the size of a ResponseWriter whose header was not written yet.
	httpxNoWritten = -1
	// httpxMaxMultipartMemory is the memory limit of ParseMultipartForm (gin's default).
	httpxMaxMultipartMemory = 32 << 20
)

// ResponseWriter wraps the http.ResponseWriter of a request. Like gin's writer it defers the
// status line: WriteHeader only records the code, the header goes out with the first Write, with
// WriteHeaderNow, or when the router finishes the request. It records the status and the body size.
type ResponseWriter struct {
	http.ResponseWriter
	status   int
	size     int
	hijacked bool
}

// errHttpxHijackWritten is Hijack's error once the header was written.
var errHttpxHijackWritten = errors.New("web: response already written, cannot hijack")

func newResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{ResponseWriter: w, status: http.StatusOK, size: httpxNoWritten}
}

// WriteHeader records the status code. It is ignored once the header was written.
func (w *ResponseWriter) WriteHeader(code int) {
	if code > 0 && w.status != code {
		if w.Written() {
			return
		}
		w.status = code
	}
}

// WriteHeaderNow writes the recorded status code, once; never on a hijacked connection.
func (w *ResponseWriter) WriteHeaderNow() {
	if !w.Written() && !w.hijacked {
		w.size = 0
		w.ResponseWriter.WriteHeader(w.status)
	}
}

// Write writes the header if needed, then the data.
func (w *ResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	n, err := w.ResponseWriter.Write(data)
	w.size += n
	return n, err
}

// WriteString writes the header if needed, then s.
func (w *ResponseWriter) WriteString(s string) (int, error) {
	w.WriteHeaderNow()
	n, err := io.WriteString(w.ResponseWriter, s)
	w.size += n
	return n, err
}

// Status returns the recorded status code.
func (w *ResponseWriter) Status() int { return w.status }

// Size returns the number of body bytes written, -1 before the header was written.
func (w *ResponseWriter) Size() int { return w.size }

// Written reports whether the header was written.
func (w *ResponseWriter) Written() bool { return w.size != httpxNoWritten }

// Unwrap returns the wrapped writer, for http.ResponseController.
func (w *ResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush writes the header if needed and flushes the wrapped writer when it supports it.
func (w *ResponseWriter) Flush() {
	w.WriteHeaderNow()
	_ = http.NewResponseController(w.ResponseWriter).Flush() // not every writer can flush
}

// Hijack takes over the connection (like gin: an error once the header was written). After a
// successful Hijack the writer counts as written and the router writes no status line.
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.Written() {
		return nil, nil, errHttpxHijackWritten
	}
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.size = 0
	w.hijacked = true
	return conn, rw, nil
}

// Context carries one request through the middleware and handler chain.
type Context struct {
	Writer  *ResponseWriter
	Request *http.Request

	router   *Router // nil for NewTestContext: ClientIP is then the peer
	handlers []HandlerFunc
	index    int

	mu   sync.RWMutex
	keys map[string]any

	queryCache url.Values
	formParsed bool
	formCache  url.Values
}

// NewTestContext returns a Context for calling a handler directly in a test. It has no router:
// ClientIP is the peer address and FullPath comes from r.Pattern. r is used as is (not copied).
// The recorded status reaches w with the first body write or c.Writer.WriteHeaderNow().
func NewTestContext(w http.ResponseWriter, r *http.Request) *Context {
	return &Context{Writer: newResponseWriter(w), Request: r, index: -1}
}

/* flow control */

// Next runs the pending handlers of the chain inside the calling middleware.
func (c *Context) Next() {
	c.index++
	for c.index < len(c.handlers) {
		if h := c.handlers[c.index]; h != nil {
			h(c)
		}
		c.index++
	}
}

// runChain runs a nested chain (a route's middleware and handlers) and restores the outer one.
// An Abort inside the nested chain stays visible to the outer chain.
func (c *Context) runChain(chain []HandlerFunc) {
	savedHandlers, savedIndex := c.handlers, c.index
	c.handlers, c.index = chain, -1
	c.Next()
	aborted := c.IsAborted()
	c.handlers, c.index = savedHandlers, savedIndex
	if aborted {
		c.index = abortIndex
	}
}

// Abort stops the pending handlers; the current one keeps running.
func (c *Context) Abort() { c.index = abortIndex }

// AbortWithStatus writes the status header and aborts.
func (c *Context) AbortWithStatus(code int) {
	c.Status(code)
	c.Writer.WriteHeaderNow()
	c.Abort()
}

// IsAborted reports whether Abort was called.
func (c *Context) IsAborted() bool { return c.index >= abortIndex }

/* request data */

// Param returns the value of the path wildcard name (already path-unescaped by the mux).
func (c *Context) Param(name string) string { return c.Request.PathValue(name) }

// FullPath returns the matched route pattern without its method, "" when no route matched.
func (c *Context) FullPath() string {
	p := c.Request.Pattern
	if i := strings.IndexByte(p, ' '); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (c *Context) initQueryCache() {
	if c.queryCache == nil {
		if c.Request != nil && c.Request.URL != nil {
			c.queryCache = c.Request.URL.Query()
		} else {
			c.queryCache = url.Values{}
		}
	}
}

// Query returns the first value of the URL query key, "" when absent.
func (c *Context) Query(key string) string {
	v, _ := c.getQuery(key)
	return v
}

// DefaultQuery returns the first value of the URL query key, def when the key is absent.
// A present key with an empty value returns "".
func (c *Context) DefaultQuery(key, def string) string {
	if v, ok := c.getQuery(key); ok {
		return v
	}
	return def
}

func (c *Context) getQuery(key string) (string, bool) {
	c.initQueryCache()
	if values := c.queryCache[key]; len(values) > 0 {
		return values[0], true
	}
	return "", false
}

// initFormCache parses the urlencoded or multipart body once. The URL query is never merged in.
func (c *Context) initFormCache() {
	if c.formParsed {
		return
	}
	c.formParsed = true
	// A body that is not multipart is parsed as urlencoded; any other parse error leaves the
	// form empty or partial, like gin (which only logged it in debug mode).
	_ = c.Request.ParseMultipartForm(httpxMaxMultipartMemory)
	c.formCache = c.Request.PostForm
}

// PostForm returns the first value of the body form key, "" when absent.
func (c *Context) PostForm(key string) string {
	v, _ := c.GetPostForm(key)
	return v
}

// PostFormArray returns the values of the body form key.
func (c *Context) PostFormArray(key string) []string {
	values, _ := c.GetPostFormArray(key)
	return values
}

// GetPostForm returns the first value of the body form key; ok is false when the key is absent.
func (c *Context) GetPostForm(key string) (string, bool) {
	if values, ok := c.GetPostFormArray(key); ok {
		return values[0], true
	}
	return "", false
}

// GetPostFormArray returns the values of the body form key; ok is true iff there is one.
func (c *Context) GetPostFormArray(key string) ([]string, bool) {
	c.initFormCache()
	values := c.formCache[key]
	return values, len(values) > 0
}

// ShouldBindJSON decodes the JSON body into obj. Reader errors (e.g. *http.MaxBytesError) are
// returned unwrapped.
func (c *Context) ShouldBindJSON(obj any) error {
	if c.Request == nil || c.Request.Body == nil {
		return errors.New("invalid request")
	}
	return json.NewDecoder(c.Request.Body).Decode(obj)
}

// GetHeader returns the request header key.
func (c *Context) GetHeader(key string) string { return c.Request.Header.Get(key) }

// Cookie returns the query-unescaped value of the named request cookie.
func (c *Context) Cookie(name string) (string, error) {
	cookie, err := c.Request.Cookie(name)
	if err != nil {
		return "", err
	}
	val, _ := url.QueryUnescape(cookie.Value) // gin ignores the error too: val is "" then
	return val, nil
}

/* keys */

// Set stores a value for this request.
func (c *Context) Set(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil {
		c.keys = make(map[string]any)
	}
	c.keys[key] = v
}

// Get returns the value stored for key.
func (c *Context) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.keys[key]
	return v, ok
}

// MustGet returns the value stored for key and panics when there is none.
func (c *Context) MustGet(key string) any {
	if v, ok := c.Get(key); ok {
		return v
	}
	panic(fmt.Sprintf("key %v does not exist", key))
}

/* response */

// Status records the response status code (written with the body or at the end of the request).
func (c *Context) Status(code int) { c.Writer.WriteHeader(code) }

// Header sets a response header; an empty value deletes it.
func (c *Context) Header(key, value string) {
	if value == "" {
		c.Writer.Header().Del(key)
		return
	}
	c.Writer.Header().Set(key, value)
}

// httpxBodyAllowed reports whether a status may carry a body (copy of net/http's rule).
func httpxBodyAllowed(code int) bool {
	switch {
	case code >= 100 && code <= 199:
		return false
	case code == http.StatusNoContent, code == http.StatusNotModified:
		return false
	}
	return true
}

// render sets the status and the Content-Type (unless one is set already), then writes the body.
func (c *Context) render(code int, contentType string, body func(w io.Writer) error) {
	c.Status(code)
	if h := c.Writer.Header(); len(h["Content-Type"]) == 0 {
		h["Content-Type"] = []string{contentType}
	}
	if !httpxBodyAllowed(code) {
		c.Writer.WriteHeaderNow()
		return
	}
	if err := body(c.Writer); err != nil {
		c.Abort()
	}
}

// String writes a text/plain answer. With values the format is applied (fmt.Fprintf), without it
// is written raw, so a lone "%" stays "%".
func (c *Context) String(code int, format string, values ...any) {
	c.render(code, "text/plain; charset=utf-8", func(w io.Writer) error {
		var err error
		if len(values) > 0 {
			_, err = fmt.Fprintf(w, format, values...)
		} else {
			_, err = io.WriteString(w, format)
		}
		return err
	})
}

// JSON writes obj as JSON (encoding/json: HTML characters escaped, map keys sorted, no newline).
func (c *Context) JSON(code int, obj any) {
	data, err := json.Marshal(obj)
	if err != nil {
		log.Printf("[WEB]: JSON encode error for %s: %v", c.Request.URL.Path, err)
	}
	c.render(code, "application/json; charset=utf-8", func(w io.Writer) error {
		if err != nil {
			return err
		}
		_, werr := w.Write(data)
		return werr
	})
}

// Data writes data with the given Content-Type (unless one is set already).
func (c *Context) Data(code int, contentType string, data []byte) {
	c.render(code, contentType, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// File serves the named file (http.ServeFile; a Content-Type set before is kept).
func (c *Context) File(path string) { http.ServeFile(c.Writer, c.Request, path) }

// Redirect answers with a redirect to location (http.Redirect). Like gin it panics on a code
// that is not a redirect. The header is written at once (http.Redirect writes no body for POST).
func (c *Context) Redirect(code int, location string) {
	if (code < http.StatusMultipleChoices || code > http.StatusPermanentRedirect) && code != http.StatusCreated {
		panic(fmt.Sprintf("Cannot redirect with status code %d", code))
	}
	http.Redirect(c.Writer, c.Request, location, code)
	c.Writer.WriteHeaderNow()
}
