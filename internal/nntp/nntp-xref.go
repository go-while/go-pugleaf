package nntp

import (
	"fmt"
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
