package nntp

// Fetcher group routing (plan fetcher-group-routing): which providers nntp-fetcher may fetch a
// newsgroup from. Two inputs, both in the pattern syntax of peers.in_patterns (comma or white
// space separated, read left to right, the last matching pattern decides, "!" excludes):
//
//	providers.routing   a provider with patterns is used only for the groups they want;
//	                    empty = no restriction
//	group_routes        global pins: a group an enabled pin wants is fetched only from the
//	                    pinned provider(s), never from any other provider
//
// Precedence for a group G (Candidates):
//  1. enabled pins wanting G: their enabled providers in (route priority, provider priority)
//     order, pinned; a pin beats the pinned provider's own routing; a disabled or unknown pinned
//     provider gives no candidate and there is no fallback;
//  2. else the enabled providers whose routing wants G (provider priority order), then the
//     primary: the first enabled provider (priority order) with empty routing;
//  3. else none.
//
// Without routing and pins this is the fetcher's behaviour before routing: every group goes to
// the first enabled provider. Enabled providers with empty routing other than the primary are
// not used unless a pin names them.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
)

// ValidateRoutingPatterns checks a providers.routing or group_routes.patterns value. Empty (or
// only white space) is valid and means no restriction. Each pattern follows the RFC 3977 wildmat
// character rules (no "\", "[", "]", controls; "!" only as the leading exclusion), "@" (the
// newsfeeds poison prefix) is refused anywhere, and a list of only "!" patterns is refused
// because it can never want a group.
func ValidateRoutingPatterns(s string) error {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	positive := false
	for _, f := range fields {
		if strings.ContainsRune(f, '@') {
			return fmt.Errorf("routing pattern %q: '@' is not supported (use '!' to exclude)", f)
		}
		body := f
		if strings.HasPrefix(body, "!") {
			body = body[1:]
		} else {
			positive = true
		}
		if body == "" {
			return fmt.Errorf("routing pattern %q: empty pattern after '!'", f)
		}
		if _, neg, ok := wildmatSplit(body); !ok || len(neg) != 1 || neg[0] {
			return fmt.Errorf("routing pattern %q: invalid wildmat (no '!' inside, no '\\', '[', ']' or control characters)", f)
		}
	}
	if len(fields) > 0 && !positive {
		return errors.New("routing patterns contain only '!' exclusions and can never want a group")
	}
	return nil
}

// routingWants reports whether a parsed pattern list wants group (last match wins, decision 1).
func routingWants(pats []string, group string) bool {
	decision, _ := matchPeerPatterns(pats, group)
	return decision > 0
}

type routedProvider struct {
	id   int64
	pats []string // nil: empty routing
}

type compiledPin struct {
	routeID          int64
	routePriority    int
	providerID       int64
	providerPriority int
	providerEnabled  bool // false also for an unknown provider id
	pats             []string
}

// GroupRouter answers which providers may serve a newsgroup. It is built once per fetcher run
// and is read-only afterwards (safe for concurrent use).
type GroupRouter struct {
	byID    map[int64]*routedProvider // enabled providers
	routed  []*routedProvider         // enabled providers with routing, priority order
	primary *routedProvider           // first enabled provider with empty routing; nil when none
	pins    []*compiledPin            // enabled pins, (route priority, provider priority, route id) order
	partic  []int64
}

// NewGroupRouter compiles the routing of the enabled providers and the enabled pins. providers
// is the full list (disabled ones too, so a pin to a disabled provider is known as such) in
// GetProviders order (priority ascending); equal priorities keep that order. It returns an error
// when an enabled provider's routing or an enabled pin's patterns are invalid
// (ValidateRoutingPatterns), or a pin has empty patterns.
func NewGroupRouter(providers []*models.Provider, routes []*models.GroupRoute) (*GroupRouter, error) {
	ps := make([]*models.Provider, 0, len(providers))
	for _, p := range providers {
		if p != nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Priority < ps[j].Priority })

	r := &GroupRouter{byID: make(map[int64]*routedProvider)}
	all := make(map[int64]*models.Provider, len(ps))
	var enabled []*routedProvider
	for _, p := range ps {
		if _, dup := all[p.ID]; !dup {
			all[p.ID] = p
		}
		if !p.Enabled {
			continue
		}
		if _, dup := r.byID[p.ID]; dup {
			continue
		}
		if err := ValidateRoutingPatterns(p.Routing); err != nil {
			return nil, fmt.Errorf("provider %q (id %d): %w", p.Name, p.ID, err)
		}
		rp := &routedProvider{id: p.ID, pats: ParsePeerPatterns(p.Routing)}
		r.byID[p.ID] = rp
		enabled = append(enabled, rp)
		if len(rp.pats) > 0 {
			r.routed = append(r.routed, rp)
		} else if r.primary == nil {
			r.primary = rp
		}
	}

	for _, rt := range routes {
		if rt == nil || !rt.Enabled {
			continue
		}
		if strings.TrimSpace(rt.Patterns) == "" {
			return nil, fmt.Errorf("global route %d: patterns are empty", rt.ID)
		}
		if err := ValidateRoutingPatterns(rt.Patterns); err != nil {
			return nil, fmt.Errorf("global route %d: %w", rt.ID, err)
		}
		pin := &compiledPin{
			routeID:       rt.ID,
			routePriority: rt.Priority,
			providerID:    rt.ProviderID,
			pats:          ParsePeerPatterns(rt.Patterns),
		}
		if p, ok := all[rt.ProviderID]; ok {
			pin.providerPriority = p.Priority
		}
		_, pin.providerEnabled = r.byID[rt.ProviderID]
		r.pins = append(r.pins, pin)
	}
	sort.SliceStable(r.pins, func(i, j int) bool {
		a, b := r.pins[i], r.pins[j]
		if a.routePriority != b.routePriority {
			return a.routePriority < b.routePriority
		}
		if a.providerPriority != b.providerPriority {
			return a.providerPriority < b.providerPriority
		}
		return a.routeID < b.routeID
	})

	// Participants: primary, routed providers and enabled pin targets, in provider priority order.
	use := make(map[int64]bool)
	if r.primary != nil {
		use[r.primary.id] = true
	}
	for _, rp := range r.routed {
		use[rp.id] = true
	}
	for _, pin := range r.pins {
		if pin.providerEnabled {
			use[pin.providerID] = true
		}
	}
	for _, rp := range enabled {
		if use[rp.id] {
			r.partic = append(r.partic, rp.id)
		}
	}
	return r, nil
}

// Candidates returns the providers that may serve group, in the order to try them, and whether a
// pin decided it. pinned with no ids means the group's pinned providers are all disabled (or
// unknown): the group must not be fetched from anywhere. No ids and not pinned: no provider
// (no primary and no routing wants it).
func (r *GroupRouter) Candidates(group string) (ids []int64, pinned bool) {
	if r == nil {
		return nil, false
	}
	seen := make(map[int64]bool)
	for _, pin := range r.pins {
		if !routingWants(pin.pats, group) {
			continue
		}
		pinned = true
		if pin.providerEnabled && !seen[pin.providerID] {
			seen[pin.providerID] = true
			ids = append(ids, pin.providerID)
		}
	}
	if pinned {
		return ids, true
	}
	for _, rp := range r.routed {
		if routingWants(rp.pats, group) {
			ids = append(ids, rp.id)
		}
	}
	if r.primary != nil {
		ids = append(ids, r.primary.id)
	}
	return ids, false
}

// Allowed reports whether providerID may serve group (it is one of Candidates(group)); used to
// filter the groups a LIST sync from that provider may add.
func (r *GroupRouter) Allowed(providerID int64, group string) bool {
	ids, _ := r.Candidates(group)
	for _, id := range ids {
		if id == providerID {
			return true
		}
	}
	return false
}

// Participants returns the providers a fetcher run uses: the primary, the providers with
// routing and the enabled pin targets, each once, in provider priority order.
func (r *GroupRouter) Participants() []int64 {
	if r == nil {
		return nil
	}
	return append([]int64(nil), r.partic...)
}

// Primary returns the first enabled provider with empty routing (the fallback for unpinned
// groups); ok is false when there is none.
func (r *GroupRouter) Primary() (id int64, ok bool) {
	if r == nil || r.primary == nil {
		return 0, false
	}
	return r.primary.id, true
}
