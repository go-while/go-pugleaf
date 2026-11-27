package web

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Fetcher routing on the admin providers tab (plan fetcher-group-routing): the routing field of
// a provider and the "Global routing" pins (table group_routes). nntp-fetcher reads both once
// per run; the web only stores them.

// adminProvidersRedirect is where the provider and route handlers send the browser back to.
const adminProvidersRedirect = "/admin?tab=providers"

// maxRoutingLen bounds a providers.routing or group_routes.patterns value (bytes, normalized).
const maxRoutingLen = 8192

// routingShortMax is how many characters of a routing list the providers table shows.
const routingShortMax = 40

// GroupRouteView is one row of the "Global routing" table.
type GroupRouteView struct {
	*models.GroupRoute
	ProviderName    string // "" when the provider row is gone
	ProviderEnabled bool
}

// cleanRoutingPatterns validates a routing pattern list from a form with
// nntp.ValidateRoutingPatterns and returns it normalized: the patterns joined by ", " (the form
// accepts commas, blanks and new lines). Empty input returns "".
func cleanRoutingPatterns(s string) (string, error) {
	if err := nntp.ValidateRoutingPatterns(s); err != nil {
		return "", err
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	out := strings.Join(fields, ", ")
	if len(out) > maxRoutingLen {
		return "", fmt.Errorf("routing patterns longer than %d bytes", maxRoutingLen)
	}
	return out, nil
}

// routingShort is a routing list for the providers table, cut at routingShortMax characters
// (the full list is in the cell's title and the edit form).
func routingShort(s string) string {
	if r := []rune(s); len(r) > routingShortMax {
		return string(r[:routingShortMax]) + "…"
	}
	return s
}

// routingBacklogCount returns how many of groups hold articles (message_count > 0) and are
// wanted by patterns, matched as the fetcher's router does (nntp.RoutingMatcher). Empty
// patterns match nothing.
func routingBacklogCount(groups []*models.Newsgroup, patterns string) int {
	if strings.TrimSpace(patterns) == "" {
		return 0
	}
	wants := nntp.RoutingMatcher(patterns)
	n := 0
	for _, g := range groups {
		if g != nil && g.MessageCount > 0 && wants(g.Name) {
			n++
		}
	}
	return n
}

// routingBacklogNotice returns the sentence added to the success message after a provider's
// routing or a global route is saved, when its patterns want groups that already hold
// articles: the fetcher starts such a group on that provider at its newest local article (when
// the provider has no progress for it), so the provider's older articles are not fetched
// unless the admin runs -download-start-date with -provider. "" when no such group exists or
// the groups cannot be read (logged). One read of every newsgroup per save.
func (s *WebServer) routingBacklogNotice(patterns, providerName string) string {
	if strings.TrimSpace(patterns) == "" {
		return ""
	}
	groups, err := s.DB.MainDBGetAllNewsgroups()
	if err != nil {
		log.Printf("[WEB]: Failed to read the newsgroups for the routing notice: %v", err)
		return ""
	}
	n := routingBacklogCount(groups, patterns)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" Note: the patterns match %d existing groups that already have articles. Older articles of these groups on provider '%s' are not fetched automatically (use pugleaf-fetcher -download-start-date YYYY-MM-DD -group <groups> -provider '%s').",
		n, providerName, providerName)
}

// groupRouteViews joins the routes with their provider names.
func groupRouteViews(routes []*models.GroupRoute, providers []*models.Provider) []*GroupRouteView {
	byID := make(map[int64]*models.Provider, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}
	views := make([]*GroupRouteView, 0, len(routes))
	for _, r := range routes {
		v := &GroupRouteView{GroupRoute: r}
		if p := byID[r.ProviderID]; p != nil {
			v.ProviderName = p.Name
			v.ProviderEnabled = p.Enabled
		}
		views = append(views, v)
	}
	return views
}

// groupRouteFromForm fills r from the route form fields (patterns, provider_id, priority,
// enabled, notes) and validates it, returning the route's provider. On a bad field it returns
// the message for the error flash.
func (s *WebServer) groupRouteFromForm(c *Context, r *models.GroupRoute) (*models.Provider, string, bool) {
	patterns, err := cleanRoutingPatterns(c.PostForm("patterns"))
	if err != nil {
		return nil, "Invalid route patterns: " + err.Error(), false
	}
	providerID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("provider_id")), 10, 64)
	if err != nil || providerID <= 0 {
		return nil, "Select a provider for the route", false
	}
	priority := 0
	if v := strings.TrimSpace(c.PostForm("priority")); v != "" {
		priority, err = strconv.Atoi(v)
		if err != nil || priority < 0 || priority > 1000000 {
			return nil, "Invalid route priority", false
		}
	}
	enabled := c.PostForm("enabled")

	r.Patterns = patterns
	r.ProviderID = providerID
	r.Priority = priority
	r.Enabled = enabled == "on" || enabled == "true"
	r.Notes = c.PostForm("notes")
	if err := database.ValidateGroupRoute(r); err != nil {
		return nil, "Invalid route: " + err.Error(), false
	}

	// Insert/Update check this too, but their error does not tell a missing provider apart.
	p, err := s.DB.GetProviderByID(providerID)
	if err != nil {
		log.Printf("[WEB]: Failed to load provider %d for a group route: %v", providerID, err)
		return nil, "Failed to check the route's provider", false
	}
	if p == nil {
		return nil, fmt.Sprintf("Provider not found (ID %d)", providerID), false
	}
	return p, "", true
}

// groupRouteIDFromForm parses the "id" form field. On a missing or invalid id it sets the error
// flash, redirects and returns false, so the caller just returns.
func groupRouteIDFromForm(c *Context, session *SessionData) (int64, bool) {
	idStr := strings.TrimSpace(c.PostForm("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if idStr == "" || err != nil || id <= 0 {
		session.SetError("Invalid route ID")
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return 0, false
	}
	return id, true
}

// adminCreateGroupRoute handles POST /admin/routes: add a global routing pin.
func (s *WebServer) adminCreateGroupRoute(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	route := &models.GroupRoute{}
	p, msg, ok := s.groupRouteFromForm(c, route)
	if !ok {
		session.SetError(msg)
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	id, err := s.DB.InsertGroupRoute(route)
	if err != nil {
		log.Printf("[WEB]: Failed to add group route %q: %v", route.Patterns, err)
		session.SetError("Failed to add the route")
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	log.Printf("[WEB]: Admin added group route %d %q -> provider %d", id, route.Patterns, route.ProviderID)
	session.SetSuccess("Route added: " + route.Patterns + s.routingBacklogNotice(route.Patterns, p.Name))
	c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
}

// adminUpdateGroupRoute handles POST /admin/routes/update: change every field of a route.
func (s *WebServer) adminUpdateGroupRoute(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	id, ok := groupRouteIDFromForm(c, session)
	if !ok {
		return
	}
	route, err := s.DB.GetGroupRouteByID(id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[WEB]: Failed to load group route %d: %v", id, err)
		}
		session.SetError("Route not found")
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	p, msg, ok := s.groupRouteFromForm(c, route)
	if !ok {
		session.SetError(msg)
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	if err := s.DB.UpdateGroupRoute(route); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			session.SetError("Route not found")
		} else {
			log.Printf("[WEB]: Failed to update group route %d: %v", id, err)
			session.SetError("Failed to update the route")
		}
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	log.Printf("[WEB]: Admin updated group route %d %q -> provider %d", id, route.Patterns, route.ProviderID)
	session.SetSuccess("Route updated: " + route.Patterns + s.routingBacklogNotice(route.Patterns, p.Name))
	c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
}

// adminDeleteGroupRoute handles POST /admin/routes/delete.
func (s *WebServer) adminDeleteGroupRoute(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	id, ok := groupRouteIDFromForm(c, session)
	if !ok {
		return
	}
	if err := s.DB.DeleteGroupRoute(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			session.SetError("Route not found")
		} else {
			log.Printf("[WEB]: Failed to delete group route %d: %v", id, err)
			session.SetError("Failed to delete the route")
		}
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	log.Printf("[WEB]: Admin deleted group route %d", id)
	session.SetSuccess("Route deleted")
	c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
}

// adminToggleGroupRoute handles POST /admin/routes/toggle: flip the enabled flag of a route.
func (s *WebServer) adminToggleGroupRoute(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	id, ok := groupRouteIDFromForm(c, session)
	if !ok {
		return
	}
	if err := s.DB.ToggleGroupRoute(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			session.SetError("Route not found")
		} else {
			log.Printf("[WEB]: Failed to toggle group route %d: %v", id, err)
			session.SetError("Failed to change the route")
		}
		c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
		return
	}
	log.Printf("[WEB]: Admin toggled group route %d", id)
	session.SetSuccess("Route changed")
	c.Redirect(http.StatusSeeOther, adminProvidersRedirect)
}
