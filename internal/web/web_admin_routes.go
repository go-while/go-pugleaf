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
// enabled, notes) and validates it. On a bad field it returns the message for the error flash.
func (s *WebServer) groupRouteFromForm(c *Context, r *models.GroupRoute) (string, bool) {
	patterns, err := cleanRoutingPatterns(c.PostForm("patterns"))
	if err != nil {
		return "Invalid route patterns: " + err.Error(), false
	}
	providerID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("provider_id")), 10, 64)
	if err != nil || providerID <= 0 {
		return "Select a provider for the route", false
	}
	priority := 0
	if v := strings.TrimSpace(c.PostForm("priority")); v != "" {
		priority, err = strconv.Atoi(v)
		if err != nil || priority < 0 || priority > 1000000 {
			return "Invalid route priority", false
		}
	}
	enabled := c.PostForm("enabled")

	r.Patterns = patterns
	r.ProviderID = providerID
	r.Priority = priority
	r.Enabled = enabled == "on" || enabled == "true"
	r.Notes = c.PostForm("notes")
	if err := database.ValidateGroupRoute(r); err != nil {
		return "Invalid route: " + err.Error(), false
	}

	// Insert/Update check this too, but their error does not tell a missing provider apart.
	p, err := s.DB.GetProviderByID(providerID)
	if err != nil {
		log.Printf("[WEB]: Failed to load provider %d for a group route: %v", providerID, err)
		return "Failed to check the route's provider", false
	}
	if p == nil {
		return fmt.Sprintf("Provider not found (ID %d)", providerID), false
	}
	return "", true
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
	if msg, ok := s.groupRouteFromForm(c, route); !ok {
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
	session.SetSuccess("Route added: " + route.Patterns)
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
	if msg, ok := s.groupRouteFromForm(c, route); !ok {
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
	session.SetSuccess("Route updated: " + route.Patterns)
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
