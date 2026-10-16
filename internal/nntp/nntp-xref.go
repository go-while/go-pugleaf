package nntp

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
)

// XrefLocation is one place an article is stored, as served in an Xref: header
// (RFC 5536 §3.2.14): newsgroup name and article number in that group.
type XrefLocation struct {
	Group      string
	ArticleNum int64
}

// XrefMode selects how the server builds the Xref: header (cmd/nntp-server -xref).
type XrefMode int32

const (
	// XrefOff serves no Xref: header, no Xref field in OVER/XOVER and no "Xref:full" overview line.
	XrefOff XrefMode = iota
	// XrefPartial serves "Xref: <host> <group>:<num>" for the serving group only (no lookups).
	XrefPartial
	// XrefFull adds the other groups from the history index (one lookup per served article).
	XrefFull
)

// String returns the flag spelling of m.
func (m XrefMode) String() string {
	switch m {
	case XrefOff:
		return "off"
	case XrefPartial:
		return "partial"
	case XrefFull:
		return "full"
	}
	return fmt.Sprintf("XrefMode(%d)", int32(m))
}

// ParseXrefMode parses the -xref flag value (off, partial, full; case-insensitive).
func ParseXrefMode(s string) (XrefMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return XrefOff, nil
	case "partial":
		return XrefPartial, nil
	case "full":
		return XrefFull, nil
	}
	return XrefOff, fmt.Errorf("invalid -xref value %q (off, partial or full)", s)
}

// xrefModeStore holds the mode of a server; read on every served article.
type xrefModeStore struct {
	v atomic.Int32
	// errLogged is set after the first failed XrefLocations lookup was logged: later
	// failures fall back to partial silently (one log line per server, not per article).
	errLogged atomic.Bool
	// batched: in XrefFull the range commands (OVER/XOVER, HDR/XHDR) look up the locations of
	// a chunk with one Processor.XrefLocationsBatch call instead of one XrefLocations per line.
	batched atomic.Bool
}

// SetXrefBatchedLookup enables (the default of a new server) or disables the batched history
// lookup of XrefFull ranges (-xref-batched-lookup). It may be called while serving; the served
// output is the same either way.
func (s *NNTPServer) SetXrefBatchedLookup(on bool) {
	if s == nil {
		return
	}
	s.xref.batched.Store(on)
}

// XrefBatchedLookup reports whether XrefFull ranges use the batched lookup.
func (s *NNTPServer) XrefBatchedLookup() bool {
	if s == nil {
		return false
	}
	return s.xref.batched.Load()
}

// SetXrefMode sets how Xref: headers are built. The default of a new server is XrefPartial.
// It may be called while serving.
func (s *NNTPServer) SetXrefMode(m XrefMode) {
	if s == nil {
		return
	}
	s.xref.v.Store(int32(m))
}

// XrefMode returns the mode in effect.
func (s *NNTPServer) XrefMode() XrefMode {
	if s == nil {
		return XrefOff
	}
	return XrefMode(s.xref.v.Load())
}

// xrefDefaultHostname is the Xref: hostname when Config.Hostname is empty (as the greeting).
const xrefDefaultHostname = "go-pugleaf"

// xrefHostname returns the hostname of the Xref: header: Config.Hostname, else "go-pugleaf".
func (s *NNTPServer) xrefHostname() string {
	if s != nil && s.Config != nil && s.Config.Hostname != "" {
		return s.Config.Hostname
	}
	return xrefDefaultHostname
}

// xrefValue returns the Xref: value without the "Xref: " prefix (RFC 5536 §3.2.14):
// "<host> <group>:<num> <g2>:<n2> ...". The serving group comes first when group is set and
// num > 0; others follow in their order, skipping entries with an empty group, a number
// <= 0, the serving group itself and a group already listed (the first entry wins). With no
// location at all it returns "".
func xrefValue(hostname, group string, num int64, others []XrefLocation) string {
	var b strings.Builder
	n := 0
	add := func(g string, a int64) {
		b.WriteByte(' ')
		b.WriteString(g)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(a, 10))
		n++
	}
	var seen map[string]struct{}
	if serving := group != "" && num > 0; serving {
		add(group, num)
		seen = map[string]struct{}{group: {}}
	}
	for _, o := range others {
		if o.Group == "" || o.ArticleNum <= 0 {
			continue
		}
		if _, dup := seen[o.Group]; dup {
			continue // the serving group, or a group listed before (first wins)
		}
		if seen == nil {
			seen = make(map[string]struct{}, len(others))
		}
		seen[o.Group] = struct{}{}
		add(o.Group, o.ArticleNum)
	}
	if n == 0 {
		return ""
	}
	return hostname + b.String()
}

// xrefLine builds the Xref: header line "Xref: <host> <group>:<num> <g2>:<n2> ..." (see
// xrefValue for the order and the skipped entries); "" when there is no location.
func xrefLine(hostname, group string, num int64, others []XrefLocation) string {
	v := xrefValue(hostname, group, num, others)
	if v == "" {
		return ""
	}
	return "Xref: " + v
}

// isXrefField reports whether field names the Xref header (case-insensitive).
func isXrefField(field string) bool {
	return strings.EqualFold(field, "xref")
}

// xrefFor returns the local Xref value (without the "Xref: " prefix) of the article
// messageID in mode; "" in XrefOff. group and num are the serving group and the article's
// number there; num <= 0 (found by message-id outside the current group) means there is no
// serving group, so XrefPartial gives "". XrefFull adds Processor.XrefLocations; without a
// processor, or when the lookup fails, it behaves like XrefPartial (the first failure per
// server is logged).
func (c *ClientConnection) xrefFor(mode XrefMode, messageID, group string, num int64) string {
	if mode == XrefOff || c == nil || c.server == nil {
		return ""
	}
	if num <= 0 {
		group = ""
	}
	return xrefValue(c.server.xrefHostname(), group, num, c.xrefOthers(mode, messageID))
}

// xrefHeaderLine is xrefFor as a header line built by xrefLine ("Xref: ..."); "" in XrefOff
// or when there is no location.
func (c *ClientConnection) xrefHeaderLine(mode XrefMode, messageID, group string, num int64) string {
	if mode == XrefOff || c == nil || c.server == nil {
		return ""
	}
	if num <= 0 {
		group = ""
	}
	return xrefLine(c.server.xrefHostname(), group, num, c.xrefOthers(mode, messageID))
}

// xrefOthers returns the other locations of messageID for XrefFull from
// Processor.XrefLocations; nil in the other modes, without a processor or when the lookup
// fails (the first failure per server is logged, later ones fall back silently).
func (c *ClientConnection) xrefOthers(mode XrefMode, messageID string) []XrefLocation {
	if mode != XrefFull || c.server.Processor == nil || messageID == "" {
		return nil
	}
	locs, err := c.server.Processor.XrefLocations(messageID)
	if err != nil {
		if c.server.xref.errLogged.CompareAndSwap(false, true) {
			log.Printf("[NNTP] Xref: location lookup of %s failed, serving the current group only (logged once): %v", messageID, err)
		}
		return nil
	}
	return locs
}
