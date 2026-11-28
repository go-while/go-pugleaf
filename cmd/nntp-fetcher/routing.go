package main

// Fetcher group routing (plan fetcher-group-routing): which provider fetches which group in one
// run, the -group selection and the -provider choice. The precedence itself lives in
// nntp.GroupRouter; this file adds what the fetcher needs around it: the usable-provider filter,
// the connection caps, the per-run assignment against each provider's cached LIST, and the
// log and dry-run output.

import (
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// routeLogFirst is how many group names a skip log line lists.
const routeLogFirst = 10

// capProviderConns returns the connection limit the fetcher uses for p: the hardcoded caps of
// eternal-september (3) and blueworldhosting (16), p.MaxConns for every other provider. The
// caps must not change (the user, 2026-10-15).
func capProviderConns(p *models.Provider) int {
	if (strings.Contains(p.Host, "eternal-september") || p.Host == ET_V4 || p.Host == ET_V6) && p.MaxConns > 3 {
		return 3
	} else if (strings.Contains(p.Host, "blueworldhosting") || strings.HasPrefix(p.Host, BW_V4_Prefix)) && p.MaxConns > 16 {
		return 16
	}
	return p.MaxConns
}

// fetchUsable reports whether an enabled provider can serve a fetch run (the checks the fetcher
// made before routing when it picked the first enabled provider).
func fetchUsable(p *models.Provider) bool {
	return p.Host != "" && p.Port > 0 && p.MaxConns > 0
}

// newFetchRouter builds the router of a fetch run and of a newsgroup list sync. Both use
// fetchUsable: the list sync adds the groups the fetcher would fetch from the provider (via
// router.Allowed), so its primary, its -provider check and the pins must be the fetch run's. An
// enabled provider without host, port or max conns is passed as disabled.
func newFetchRouter(providers []*models.Provider, routes []*models.GroupRoute) (*nntp.GroupRouter, error) {
	return nntp.NewGroupRouter(routerProviders(providers, fetchUsable), routes)
}

// checkFlagCombos refuses flag combinations that cannot mean what they say: -routing-dry-run
// fetches and stores nothing, so it does not go with -reset-progress (which writes the
// progress) or -fetch-newsgroups-from-remote (a list sync, which writes with
// -fetch-newsgroups-force). main exits with code 2 on an error, like the flag package.
func checkFlagCombos(routingDryRun bool, resetProgress int64, updateList string) error {
	if !routingDryRun {
		return nil
	}
	if resetProgress > 0 {
		return fmt.Errorf("-routing-dry-run and -reset-progress do not go together: the dry run changes nothing, run -reset-progress on its own")
	}
	if updateList != "" {
		return fmt.Errorf("-routing-dry-run and -fetch-newsgroups-from-remote do not go together: the list sync is a dry run without -fetch-newsgroups-force")
	}
	return nil
}

// routerProviders returns providers for nntp.NewGroupRouter: an enabled provider that usable
// refuses is passed as a disabled copy, so it is neither the primary nor a candidate (a pin to
// it skips its groups). The caller's providers are not changed.
func routerProviders(providers []*models.Provider, usable func(*models.Provider) bool) []*models.Provider {
	out := make([]*models.Provider, 0, len(providers))
	for _, p := range providers {
		if p == nil {
			continue
		}
		if p.Enabled && !usable(p) {
			cp := *p
			cp.Enabled = false
			out = append(out, &cp)
			continue
		}
		out = append(out, p)
	}
	return out
}

// findProviderByName returns the first provider named name (exact match), nil when none.
func findProviderByName(providers []*models.Provider, name string) *models.Provider {
	for _, p := range providers {
		if p != nil && p.Name == name {
			return p
		}
	}
	return nil
}

// isParticipant reports whether id is one of the providers a run uses (router.Participants).
func isParticipant(router *nntp.GroupRouter, id int64) bool {
	return slices.Contains(router.Participants(), id)
}

// resolveProviderFlag returns the provider -provider names for a fetch run or a list sync. It
// must exist and take part in the routing (the primary, a provider with routing or the target
// of an enabled global route, and usable): any other provider would get no group.
func resolveProviderFlag(providers []*models.Provider, router *nntp.GroupRouter, name string) (*models.Provider, error) {
	p := findProviderByName(providers, name)
	if p == nil {
		return nil, fmt.Errorf("-provider %q: no provider with that name", name)
	}
	if !p.Enabled {
		return nil, fmt.Errorf("-provider %q: provider is disabled", name)
	}
	if !isParticipant(router, p.ID) {
		return nil, fmt.Errorf("-provider %q: provider gets no groups (not the primary, no routing, no enabled global route, or host/port/max conns not set)", name)
	}
	return p, nil
}

// primaryProvider returns the router's primary (first enabled usable provider with empty
// routing): without routing this is the provider the fetcher always used.
func primaryProvider(providers []*models.Provider, router *nntp.GroupRouter) (*models.Provider, error) {
	id, ok := router.Primary()
	if !ok {
		return nil, fmt.Errorf("no enabled provider without routing (the primary): name one with -provider")
	}
	for _, p := range providers {
		if p != nil && p.ID == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("primary provider id %d not found", id)
}

// resetProgressProvider returns the provider whose progress -reset-progress sets: the one
// -provider names (any provider, the progress is only a stored number), else the primary.
func resetProgressProvider(providers []*models.Provider, router *nntp.GroupRouter, name string) (*models.Provider, error) {
	if name == "" {
		return primaryProvider(providers, router)
	}
	p := findProviderByName(providers, name)
	if p == nil {
		return nil, fmt.Errorf("-provider %q: no provider with that name", name)
	}
	return p, nil
}

// groupSelection is the parsed -group flag: several patterns separated by white space, ',' or
// ';' (none of them is a valid newsgroup character). A pattern ending in '*' is a name prefix
// ('alt.*' = prefix 'alt.', 'alt*' = prefix 'alt'), '$all', '*' or an empty flag select every
// group, anything else is an exact name.
type groupSelection struct {
	all      bool
	prefixes []string
	exact    []string
	patterns []string // as given, for the log
}

// parseGroupSelection parses the -group flag value.
func parseGroupSelection(s string) groupSelection {
	var sel groupSelection
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	if len(fields) == 0 {
		sel.all = true
		return sel
	}
	seenPrefix := make(map[string]bool)
	seenExact := make(map[string]bool)
	for _, f := range fields {
		sel.patterns = append(sel.patterns, f)
		switch {
		case f == "$all":
			sel.all = true
		case strings.HasSuffix(f, "*"):
			prefix := strings.TrimSuffix(f, "*")
			if prefix == "" {
				sel.all = true
			} else if !seenPrefix[prefix] {
				seenPrefix[prefix] = true
				sel.prefixes = append(sel.prefixes, prefix)
			}
		default:
			if !seenExact[f] {
				seenExact[f] = true
				sel.exact = append(sel.exact, f)
			}
		}
	}
	return sel
}

// needsDB reports whether the selection needs the database's group list (every selection but
// a list of exact names).
func (sel groupSelection) needsDB() bool {
	return sel.all || len(sel.prefixes) > 0
}

// match reports whether name is selected.
func (sel groupSelection) match(name string) bool {
	if sel.all {
		return true
	}
	for _, p := range sel.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return slices.Contains(sel.exact, name)
}

// String describes the selection for the log.
func (sel groupSelection) String() string {
	if sel.all {
		return "all groups"
	}
	return fmt.Sprintf("%d pattern(s): %s", len(sel.patterns), strings.Join(sel.patterns, ", "))
}

// selectNewsgroups returns the groups of dbGroups that sel selects, in dbGroups order, followed
// by the exact names that dbGroups does not hold (as bare entries: the feeder's database lookup
// logs and skips them, as for a single -group name before).
func selectNewsgroups(dbGroups []*models.Newsgroup, sel groupSelection) []*models.Newsgroup {
	if sel.all {
		return dbGroups
	}
	out := make([]*models.Newsgroup, 0, len(sel.exact))
	seen := make(map[string]bool)
	for _, g := range dbGroups {
		if g != nil && !seen[g.Name] && sel.match(g.Name) {
			seen[g.Name] = true
			out = append(out, g)
		}
	}
	for _, name := range sel.exact {
		if !seen[name] {
			seen[name] = true
			out = append(out, &models.Newsgroup{Name: name})
		}
	}
	return out
}

// activeSelection keeps the groups of the -fetch-active-only selection (active ones, or with
// false only the inactive ones), so the routing and its skip logs only count groups a pass
// would fetch. A group loaded from the database (ID > 0) carries its active flag; a bare entry
// is looked up, and one whose lookup fails is kept (the feeder logs and skips it).
func activeSelection(groups []*models.Newsgroup, fetchActiveOnly bool, lookup func(string) (*models.Newsgroup, error)) []*models.Newsgroup {
	out := make([]*models.Newsgroup, 0, len(groups))
	for _, g := range groups {
		active := g.Active
		if g.ID <= 0 {
			nga, err := lookup(g.Name)
			if err != nil || nga == nil {
				out = append(out, g)
				continue
			}
			active = nga.Active
		}
		if active == fetchActiveOnly {
			out = append(out, g)
		}
	}
	return out
}

// routeReason says why a group went to a provider or was skipped.
type routeReason int

const (
	routeAssigned    routeReason = iota // fetched from provider
	routePinned                         // pinned, but no pinned provider is usable and lists it
	routeNotListed                      // no candidate lists it (provider: the last candidate tried)
	routeNoCandidate                    // no provider may serve it (no primary, no routing wants it)
)

// pinMiss says why one pinned provider did not take a pinned group.
type pinMiss struct {
	provider   int64
	listFailed bool // its LIST failed (else the group is not in its LIST)
}

// routeDecision is the routing of one group in one run.
type routeDecision struct {
	group    string
	provider int64 // the assigned provider; for routeNotListed the last candidate tried
	reason   routeReason
	// routePinned: why each usable pinned provider did not take the group, in candidate
	// order; empty when every pinned provider is disabled or unusable
	pinMisses []pinMiss
}

// routeAssignment is the routing of every selected group in one run.
type routeAssignment struct {
	decisions   []routeDecision               // one per group, input order
	byProvider  map[int64][]*models.Newsgroup // groups per assigned provider, input order
	notListed   map[int64][]string            // routeNotListed groups per last candidate
	pinSkipped  []string                      // routePinned groups
	noCandidate []string                      // routeNoCandidate groups
}

// assignGroups gives each group to ONE provider: the first of router.Candidates(group) whose
// LIST (lists[id], nil when the LIST failed) has it. A pinned group without such a candidate is
// skipped (never fetched from another provider); an unpinned one falls back along its
// candidates, the primary last, and is skipped when none of them lists it.
func assignGroups(groups []*models.Newsgroup, router *nntp.GroupRouter, lists map[int64]map[string]bool) *routeAssignment {
	a := &routeAssignment{
		decisions:  make([]routeDecision, 0, len(groups)),
		byProvider: make(map[int64][]*models.Newsgroup),
		notListed:  make(map[int64][]string),
	}
	for _, g := range groups {
		if g == nil {
			continue
		}
		ids, pinned := router.Candidates(g.Name)
		d := routeDecision{group: g.Name, reason: routeNotListed}
		for _, id := range ids {
			if lists[id][g.Name] {
				d.provider, d.reason = id, routeAssigned
				break
			}
		}
		switch {
		case d.reason == routeAssigned:
			a.byProvider[d.provider] = append(a.byProvider[d.provider], g)
		case pinned:
			d.reason = routePinned
			for _, id := range ids {
				d.pinMisses = append(d.pinMisses, pinMiss{provider: id, listFailed: lists[id] == nil})
			}
			a.pinSkipped = append(a.pinSkipped, g.Name)
		case len(ids) == 0:
			d.reason = routeNoCandidate
			a.noCandidate = append(a.noCandidate, g.Name)
		default:
			d.provider = ids[len(ids)-1]
			a.notListed[d.provider] = append(a.notListed[d.provider], g.Name)
		}
		a.decisions = append(a.decisions, d)
	}
	return a
}

// firstNames joins at most routeLogFirst names for a log line.
func firstNames(names []string) string {
	if len(names) <= routeLogFirst {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:routeLogFirst], ", ") + ", ..."
}

// providerLabel names provider id for logs.
func providerLabel(names map[int64]string, id int64) string {
	if n, ok := names[id]; ok && n != "" {
		return n
	}
	return fmt.Sprintf("id %d", id)
}

// logRouteSummary logs the assignment: groups per participant, groups not in the LIST of their
// provider (dropped silently before routing), pinned groups skipped and groups without any
// provider, each with a count and the first routeLogFirst names.
func logRouteSummary(a *routeAssignment, order []int64, names map[int64]string) {
	for _, id := range order {
		nl := a.notListed[id]
		if len(nl) == 0 {
			log.Printf("[FETCHER-ROUTE]: provider '%s': %d groups", providerLabel(names, id), len(a.byProvider[id]))
			continue
		}
		log.Printf("[FETCHER-ROUTE]: provider '%s': %d groups, %d not in its LIST (skipped): %s",
			providerLabel(names, id), len(a.byProvider[id]), len(nl), firstNames(nl))
	}
	if len(a.pinSkipped) > 0 {
		log.Printf("[FETCHER-ROUTE]: %d pinned groups skipped (no pinned provider is usable and lists them): %s",
			len(a.pinSkipped), firstNames(a.pinSkipped))
	}
	if len(a.noCandidate) > 0 {
		log.Printf("[FETCHER-ROUTE]: %d groups skipped (no provider may serve them: no primary and no routing wants them): %s",
			len(a.noCandidate), firstNames(a.noCandidate))
	}
}

// writeRouteDryRun prints the assignment for -routing-dry-run, one line per group:
//
//	ROUTE <group> -> <provider>
//	SKIP <group> pinned: <why> (pinSkipReason)
//	SKIP <group> not in the LIST of <provider>
//	SKIP <group> no provider
//
// With only > 0 (-provider) the ROUTE lines of the other providers are left out.
func writeRouteDryRun(w io.Writer, a *routeAssignment, names map[int64]string, only int64) error {
	var routed, skipped int
	for _, d := range a.decisions {
		var err error
		switch d.reason {
		case routeAssigned:
			if only > 0 && d.provider != only {
				continue
			}
			routed++
			_, err = fmt.Fprintf(w, "ROUTE %s -> %s\n", d.group, providerLabel(names, d.provider))
		case routePinned:
			skipped++
			_, err = fmt.Fprintf(w, "SKIP %s pinned: %s\n", d.group, pinSkipReason(d.pinMisses, names))
		case routeNotListed:
			skipped++
			_, err = fmt.Fprintf(w, "SKIP %s not in the LIST of %s\n", d.group, providerLabel(names, d.provider))
		case routeNoCandidate:
			skipped++
			_, err = fmt.Fprintf(w, "SKIP %s no provider\n", d.group)
		}
		if err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "[FETCHER-ROUTE] dry run: %d groups routed, %d skipped, no articles fetched\n", routed, skipped)
	return err
}

// pinSkipReason says why a pinned group was skipped: "pinned provider disabled ..." when no
// pinned provider is usable, else per pinned provider "provider <name>: its LIST failed" or
// "provider <name>: not in its LIST", joined by "; ".
func pinSkipReason(misses []pinMiss, names map[int64]string) string {
	if len(misses) == 0 {
		return "pinned provider disabled (or host, port or max conns not set)"
	}
	parts := make([]string, 0, len(misses))
	for _, m := range misses {
		why := "not in its LIST"
		if m.listFailed {
			why = "its LIST failed"
		}
		parts = append(parts, fmt.Sprintf("provider %s: %s", providerLabel(names, m.provider), why))
	}
	return strings.Join(parts, "; ")
}

// filterAllowedGroups keeps the remote groups that providerID may serve (router.Allowed), for
// the newsgroup list sync: a provider only adds groups the fetcher would fetch from it.
func filterAllowedGroups(remote []nntp.GroupInfo, router *nntp.GroupRouter, providerID int64) (kept []nntp.GroupInfo, dropped int) {
	kept = make([]nntp.GroupInfo, 0, len(remote))
	for _, g := range remote {
		if router.Allowed(providerID, g.Name) {
			kept = append(kept, g)
		} else {
			dropped++
		}
	}
	return kept, dropped
}

// dateStartLog warns about the groups of one provider pass that start at their newest local
// article date: the provider has no progress row for a group that already holds articles (the
// routing moved it there, or a new provider). The fetcher keeps that start (the user,
// 2026-11-27), so the provider's older articles of the group are not fetched unless the admin
// asks for them with -download-start-date and -provider. The first routeLogFirst groups get a
// line each with that command; summary logs the count. Safe for concurrent use.
type dateStartLog struct {
	provider string
	logf     func(format string, args ...any)
	mu       sync.Mutex
	n        int
}

// newDateStartLog returns the date-start log of provider's pass, writing with logf.
func newDateStartLog(provider string, logf func(format string, args ...any)) *dateStartLog {
	return &dateStartLog{provider: provider, logf: logf}
}

// note records that group starts at start (its newest local article date) and logs it while
// fewer than routeLogFirst groups were logged.
func (l *dateStartLog) note(group string, start time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	if l.n > routeLogFirst {
		return
	}
	day := start.Format("2006-01-02")
	l.logf("[FETCHER-ROUTE]: provider '%s' has no progress for group '%s', which has local articles: starting at the newest local article date %s, older articles of the group on this provider are not fetched; to fetch them run: pugleaf-fetcher -download-start-date YYYY-MM-DD -group %s -provider %s (YYYY-MM-DD: the oldest date wanted, before %s)",
		l.provider, group, day, shellArg(group), shellArg(l.provider), day)
}

// summary logs how many groups of the pass started at their newest local article date (nothing
// when none did).
func (l *dateStartLog) summary() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		return
	}
	more := ""
	if l.n > routeLogFirst {
		more = fmt.Sprintf(", the first %d logged above", routeLogFirst)
	}
	l.logf("[FETCHER-ROUTE]: provider '%s': %d groups started at their newest local article date%s; their older articles on this provider are not fetched (use -download-start-date with -group and -provider %s)",
		l.provider, l.n, more, shellArg(l.provider))
}

// count returns how many groups were noted.
func (l *dateStartLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// shellArg returns s as one shell word for a command in a log line: unchanged when it holds only
// characters a shell leaves alone, else in single quotes.
func shellArg(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-+:/@%=,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
