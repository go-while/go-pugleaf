package web

// The router of the web server: two net/http ServeMuxes (main and fallback) behind a global
// middleware chain. A request runs the global chain; its last step looks the request up in the
// main mux, then in the fallback mux, and runs the matched route's chain (group middleware and
// handlers). A path whose slash-less form has a main route is redirected there before the
// fallback mux is tried. On a miss, inside the global chain: trailing-slash redirect, then 405
// with Allow, then 404.

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Router dispatches requests to the routes registered with GET/POST/HEAD/Handle, Group and
// Fallback. Routes use ServeMux pattern syntax ({name}, {name...}, {$}). Registration is allowed
// after the router served requests; a duplicate or conflicting pattern panics.
type Router struct {
	// RedirectTrailingSlash redirects a path without a route to the same path with the trailing
	// slash toggled when that one has a route (301 for GET, 307 otherwise). On by default.
	RedirectTrailingSlash bool

	main     *http.ServeMux
	fallback *http.ServeMux
	maxBody  atomic.Int64 // request body cap in bytes, see SetMaxBodyBytes

	mu         sync.RWMutex
	routes     map[string]*routeHandler // method + " " + pattern, for SetRouteMaxBodyBytes
	middleware []HandlerFunc
	proxies    []*net.IPNet
	ipHeader   string
}

// RouteGroup registers routes with a common path prefix and middleware in one of the muxes.
type RouteGroup struct {
	router     *Router
	mux        *http.ServeMux
	prefix     string
	middleware []HandlerFunc
}

// routeHandler is what the muxes hold for a route: the route's chain.
type routeHandler struct {
	chain   []HandlerFunc
	maxBody atomic.Int64 // this route's body cap; 0 = the router's
}

// httpxCtxKey keys the request's *Context in the request context.
type httpxCtxKey struct{}

// NewRouter returns a router without middleware, routes or trusted proxies.
func NewRouter() *Router {
	r := &Router{
		RedirectTrailingSlash: true,
		main:                  http.NewServeMux(),
		fallback:              http.NewServeMux(),
		routes:                map[string]*routeHandler{},
	}
	r.maxBody.Store(httpxDefaultMaxBodyBytes)
	return r
}

// SetMaxBodyBytes caps the body of every request at n bytes (http.MaxBytesReader; the default
// is httpxDefaultMaxBodyBytes). A urlencoded form past the cap is answered 413 before the route
// runs; a JSON body past it fails in ShouldBindJSON with *http.MaxBytesError. n <= 0 keeps the
// current cap. Takes effect for requests that start afterwards.
func (r *Router) SetMaxBodyBytes(n int64) {
	if n > 0 {
		r.maxBody.Store(n)
	}
}

// MaxBodyBytes returns the request body cap.
func (r *Router) MaxBodyBytes() int64 { return r.maxBody.Load() }

// SetRouteMaxBodyBytes gives one registered route (method and pattern as registered, prefix
// included) its own body cap instead of the router's; n <= 0 returns it to the router's. The route
// may raise the cap only while nothing read the body yet. Panics for an unknown route.
func (r *Router) SetRouteMaxBodyBytes(method, pattern string, n int64) {
	r.mu.RLock()
	h := r.routes[method+" "+pattern]
	r.mu.RUnlock()
	if h == nil {
		panic("web: SetRouteMaxBodyBytes: no route " + method + " " + pattern)
	}
	h.maxBody.Store(max(n, 0))
}

// Use appends middleware to the global chain, which runs for every request (also for redirects,
// 405 and 404). The chain is read when a request starts.
func (r *Router) Use(mw ...HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware[:len(r.middleware):len(r.middleware)], mw...)
}

// GET registers a route for GET (the mux also serves HEAD from it).
func (r *Router) GET(path string, h ...HandlerFunc) { r.Handle(http.MethodGet, path, h...) }

// POST registers a route for POST.
func (r *Router) POST(path string, h ...HandlerFunc) { r.Handle(http.MethodPost, path, h...) }

// HEAD registers a route for HEAD.
func (r *Router) HEAD(path string, h ...HandlerFunc) { r.Handle(http.MethodHead, path, h...) }

// Handle registers a route in the main mux; the pattern is method + " " + path.
func (r *Router) Handle(method, path string, h ...HandlerFunc) {
	r.register(r.main, method, path, nil, h)
}

// Group returns a group in the main mux: its routes get prefix + path and run mw first.
func (r *Router) Group(prefix string, mw ...HandlerFunc) *RouteGroup {
	return &RouteGroup{router: r, mux: r.main, prefix: prefix, middleware: slices.Clone(mw)}
}

// Fallback returns a group in the fallback mux, which is consulted only when the main mux has no
// route for the request. It holds patterns that would conflict with the main ones.
func (r *Router) Fallback(mw ...HandlerFunc) *RouteGroup {
	return &RouteGroup{router: r, mux: r.fallback, middleware: slices.Clone(mw)}
}

// Use appends middleware to the group; it applies to routes registered afterwards.
func (g *RouteGroup) Use(mw ...HandlerFunc) {
	g.middleware = append(g.middleware[:len(g.middleware):len(g.middleware)], mw...)
}

// GET registers a GET route in the group.
func (g *RouteGroup) GET(path string, h ...HandlerFunc) { g.Handle(http.MethodGet, path, h...) }

// POST registers a POST route in the group.
func (g *RouteGroup) POST(path string, h ...HandlerFunc) { g.Handle(http.MethodPost, path, h...) }

// HEAD registers a HEAD route in the group.
func (g *RouteGroup) HEAD(path string, h ...HandlerFunc) { g.Handle(http.MethodHead, path, h...) }

// Handle registers a route in the group.
func (g *RouteGroup) Handle(method, path string, h ...HandlerFunc) {
	g.router.register(g.mux, method, httpxJoinPath(g.prefix, path), g.middleware, h)
}

// register adds method + " " + path with the chain mw + h to mux. The mux panics on a duplicate
// or conflicting pattern.
func (r *Router) register(mux *http.ServeMux, method, path string, mw, h []HandlerFunc) {
	if len(h) == 0 {
		panic("web: route " + method + " " + path + " has no handler")
	}
	chain := make([]HandlerFunc, 0, len(mw)+len(h))
	chain = append(chain, mw...)
	chain = append(chain, h...)
	rh := &routeHandler{chain: chain}
	mux.Handle(method+" "+path, rh)
	r.mu.Lock()
	r.routes[method+" "+path] = rh
	r.mu.Unlock()
}

// httpxJoinPath joins a group prefix and a route path with exactly one slash between them.
func httpxJoinPath(prefix, p string) string {
	if prefix == "" {
		return p
	}
	if p == "" {
		return prefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(p, "/")
}

// SetTrustedProxies sets the peers whose client IP header is believed, and that header.
func (r *Router) SetTrustedProxies(nets []*net.IPNet, header string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proxies = slices.Clone(nets)
	r.ipHeader = header
}

// RemoteIPHeader returns the client IP header set with SetTrustedProxies.
func (r *Router) RemoteIPHeader() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ipHeader
}

// trust returns the trusted proxies and the client IP header.
func (r *Router) trust() ([]*net.IPNet, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.proxies, r.ipHeader
}

// ServeHTTP runs the global chain and the dispatch for one request.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c := &Context{Writer: newResponseWriter(w), router: r, index: -1}
	// One request pointer for the whole request: the mux stamps the pattern and the path values
	// on it, and the route handler finds the Context through its context.
	c.Request = req.WithContext(context.WithValue(req.Context(), httpxCtxKey{}, c))
	// The body cap goes on the copy; w (not the wrapper) lets net/http close the connection
	// after a response to an oversized request instead of reading the rest.
	if req.Body != nil && req.Body != http.NoBody {
		c.rawBody = req.Body
		c.Request.Body = http.MaxBytesReader(w, req.Body, r.maxBody.Load())
	}

	r.mu.RLock()
	chain := make([]HandlerFunc, 0, len(r.middleware)+1)
	chain = append(chain, r.middleware...)
	r.mu.RUnlock()
	c.handlers = append(chain, r.dispatch)

	c.Next()
	c.Writer.WriteHeaderNow()
}

// ServeHTTP runs the route chain on the request's Context.
func (h *routeHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c, ok := req.Context().Value(httpxCtxKey{}).(*Context)
	if !ok {
		// Not reached through Router.ServeHTTP: give the route its own Context.
		rw, isOurs := w.(*ResponseWriter)
		if !isOurs {
			rw = newResponseWriter(w)
		}
		c = &Context{Writer: rw, index: -1}
		defer c.Writer.WriteHeaderNow()
	}
	c.Request = req
	// The route's own cap replaces the router's, on the untouched body only: a form read by
	// global middleware stays within the router's cap.
	if n := h.maxBody.Load(); n > 0 && c.rawBody != nil && !c.formParsed {
		c.Request.Body = http.MaxBytesReader(c.Writer.Unwrap(), c.rawBody, n)
	}
	if !httpxAcceptBody(c) {
		return
	}
	c.runChain(h.chain)
}

// httpxAcceptBody answers a request whose form body the handlers must not see: a multipart body
// (415: nothing here takes uploads, and net/http would spool large parts to disk) or a
// urlencoded one past the cap (413). It parses the urlencoded form, so the handlers
// never see a partial form. Other bodies (JSON, none) are left to the handlers.
func httpxAcceptBody(c *Context) bool {
	switch c.Request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return true
	}
	ct := c.Request.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		c.Abort()
		c.String(http.StatusUnsupportedMediaType, "415 unsupported media type: multipart bodies are not accepted\n")
		return false
	case ct == "application/x-www-form-urlencoded":
		c.initFormCache()
		var tooLarge *http.MaxBytesError
		if errors.As(c.formErr, &tooLarge) {
			c.Abort()
			c.String(http.StatusRequestEntityTooLarge, "413 request body too large: at most %d bytes\n", tooLarge.Limit)
			return false
		}
	}
	return true
}

// httpxMatch is the result of looking a request up in one mux.
type httpxMatch int

const (
	httpxMatchNone     httpxMatch = iota // no route (also: the mux's own trailing-slash redirect)
	httpxMatchRoute                      // a registered route
	httpxMatchRedirect                   // the mux's redirect of an unclean path to the cleaned one
)

// httpxLookup classifies what mux would do with req without running it.
func httpxLookup(mux *http.ServeMux, req *http.Request) httpxMatch {
	h, pattern := mux.Handler(req)
	if pattern == "" {
		return httpxMatchNone
	}
	if _, ok := h.(*routeHandler); ok {
		return httpxMatchRoute
	}
	// A redirect from the mux. For a clean path it is the mux's own "/x" -> "/x/" redirect
	// (307 for every method); that one is left to RedirectTrailingSlash, which answers like
	// gin (301 for GET) and only when no route matches in either mux.
	if esc := req.URL.EscapedPath(); httpxCleanPath(esc) == esc {
		return httpxMatchNone
	}
	return httpxMatchRedirect
}

// dispatch is the last step of the global chain: run the matching route, or answer the miss.
// A main miss whose path minus its trailing slash has a main route is redirected before the
// fallback mux is consulted: the fallback's catch-all section patterns (/{section}/{$}) would
// otherwise take /login/, /admin/, ... which gin redirected to /login, /admin.
func (r *Router) dispatch(c *Context) {
	req := c.Request
	if httpxLookup(r.main, req) != httpxMatchNone {
		r.main.ServeHTTP(c.Writer, req)
		return
	}
	if r.RedirectTrailingSlash && r.redirectSlashRemoval(c) {
		return
	}
	if httpxLookup(r.fallback, req) != httpxMatchNone {
		r.fallback.ServeHTTP(c.Writer, req)
		return
	}
	if r.RedirectTrailingSlash && r.redirectTrailingSlash(c) {
		return
	}
	if r.methodNotAllowed(c) {
		return
	}
	http.NotFound(c.Writer, req)
}

// matches reports whether a route in either mux matches req.
func (r *Router) matches(req *http.Request) bool {
	return httpxLookup(r.main, req) == httpxMatchRoute || httpxLookup(r.fallback, req) == httpxMatchRoute
}

// redirectTrailingSlash answers a clean path other than "/" with a redirect to the path with the
// trailing slash toggled, when that one has a route for the same method: 301 for GET, 307 for
// the other methods (they keep their body). The query is kept.
func (r *Router) redirectTrailingSlash(c *Context) bool {
	req := c.Request
	esc := req.URL.EscapedPath()
	if esc == "" || esc == "/" || httpxCleanPath(esc) != esc {
		return false
	}
	toggled := esc + "/"
	if strings.HasSuffix(esc, "/") {
		toggled = esc[:len(esc)-1]
	}
	probe, ok := httpxProbe(req, req.Method, toggled)
	if !ok || !r.matches(probe) {
		return false
	}
	httpxSlashRedirect(c, toggled)
	return true
}

// redirectSlashRemoval answers a clean path ending in "/" (other than "/") with a redirect to the
// path without the slash, when that one has a route in the main mux for the same method. It runs
// before the fallback mux is consulted (see dispatch).
func (r *Router) redirectSlashRemoval(c *Context) bool {
	req := c.Request
	esc := req.URL.EscapedPath()
	if esc == "/" || !strings.HasSuffix(esc, "/") || httpxCleanPath(esc) != esc {
		return false
	}
	trimmed := esc[:len(esc)-1]
	probe, ok := httpxProbe(req, req.Method, trimmed)
	if !ok || httpxLookup(r.main, probe) != httpxMatchRoute {
		return false
	}
	httpxSlashRedirect(c, trimmed)
	return true
}

// httpxSlashRedirect redirects to the escaped path target, keeping the query: 301 for GET, 307
// for the other methods (they keep their body).
func httpxSlashRedirect(c *Context, target string) {
	req := c.Request
	code := http.StatusTemporaryRedirect
	if req.Method == http.MethodGet {
		code = http.StatusMovedPermanently
	}
	location := target
	if req.URL.RawQuery != "" {
		location += "?" + req.URL.RawQuery
	}
	http.Redirect(c.Writer, req, location, code)
}

// httpxProbeMethods are the methods the 405 check tries.
var httpxProbeMethods = [...]string{http.MethodGet, http.MethodHead, http.MethodPost}

// methodNotAllowed answers 405 with an Allow header when the path has a route for another
// method of GET, HEAD and POST (a GET route also allows HEAD).
func (r *Router) methodNotAllowed(c *Context) bool {
	req := c.Request
	var allowed []string
	for _, m := range httpxProbeMethods {
		if m == req.Method {
			continue
		}
		probe, ok := httpxProbe(req, m, req.URL.EscapedPath())
		if !ok || !r.matches(probe) {
			continue
		}
		allowed = append(allowed, m)
		if m == http.MethodGet {
			allowed = append(allowed, http.MethodHead)
		}
	}
	if len(allowed) == 0 {
		return false
	}
	slices.Sort(allowed)
	allowed = slices.Compact(allowed)
	c.Writer.Header().Set("Allow", strings.Join(allowed, ", "))
	http.Error(c.Writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	return true
}

// httpxProbe returns a shallow copy of req with another method and escaped path, for lookups.
func httpxProbe(req *http.Request, method, escPath string) (*http.Request, bool) {
	p, err := url.PathUnescape(escPath)
	if err != nil {
		return nil, false
	}
	probe := req.WithContext(req.Context())
	u := *req.URL
	u.Path, u.RawPath = p, escPath
	probe.URL = &u
	probe.Method = method
	return probe, true
}

// httpxCleanPath is net/http's cleanPath: the canonical path, trailing slash kept.
func httpxCleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}
	return np
}

/* middleware */

// Recovery turns a panic in a later handler into a 500 (when nothing was written yet) and a log
// line with the stack. http.ErrAbortHandler is re-panicked for net/http to handle.
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			log.Printf("[WEB]: panic serving %s %s: %v\n%s", c.Request.Method, c.Request.URL.Path, v, debug.Stack())
			if !c.Writer.Written() {
				c.Writer.WriteHeader(http.StatusInternalServerError)
				c.Writer.WriteHeaderNow()
			}
			c.Abort()
		}()
		c.Next()
	}
}

// AccessLog writes one line per request after the chain:
// [WEB]: <client IP> "<method> <request URI>" <status> <size>B <latency>.
// The request URI is taken before the handlers run, as some rewrite the request URL. The line is
// written in a defer, so a panicking request is logged too. Register it before Recovery,
// router.Use(AccessLog(), Recovery()), so that line shows the 500 Recovery answered.
func AccessLog() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		uri := c.Request.RequestURI
		if uri == "" {
			uri = c.Request.URL.RequestURI()
		}
		method := c.Request.Method
		defer func() {
			log.Printf("[WEB]: %s \"%s %s\" %d %dB %s", c.ClientIP(), method, uri,
				c.Writer.Status(), max(c.Writer.Size(), 0), time.Since(start))
		}()
		c.Next()
	}
}

// SecureConfig selects the TLS parts of SecureHeaders.
type SecureConfig struct {
	SSLRedirect bool // 301 plain-HTTP requests to https://host/uri
	STS         bool // send Strict-Transport-Security
}

// SecureHeaders sets the security headers on every response before the later handlers run, so
// redirects, 405 and 404 carry them too.
func SecureHeaders(cfg SecureConfig) HandlerFunc {
	return func(c *Context) {
		h := c.Writer.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Xss-Protection", "1; mode=block")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if cfg.STS {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		req := c.Request
		if cfg.SSLRedirect && req.TLS == nil && !strings.EqualFold(req.URL.Scheme, "https") &&
			!strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https") {
			// URL.RequestURI is the request line's path and query also for an absolute-form
			// request line (whose RequestURI holds the whole URL).
			http.Redirect(c.Writer, req, "https://"+req.Host+req.URL.RequestURI(), http.StatusMovedPermanently)
			c.Abort()
			return
		}
		c.Next()
	}
}
