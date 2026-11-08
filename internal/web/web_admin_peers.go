package web

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/feed"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/mattn/go-sqlite3"
)

// Admin peers page (/admin/peers, plan nntp-peering). A peer is a news server that may feed us
// with IHAVE/CHECK/TAKETHIS and that we may feed; NNTP users cannot feed any more. The page
// lists, adds, edits, deletes and enables/disables peers through the peer functions of
// internal/database. Peer passwords are never shown back: an empty password field keeps the
// stored value, the "clear" checkbox next to it clears it.

// adminPeersRedirect is where every peers form posts back to.
const adminPeersRedirect = "/admin/peers"

// PeerFeedStats holds the outgoing-feed numbers of one peer, as the feed sender last wrote
// them into the queue file. It stays nil when there are no numbers (no sender has run, the
// queue file is not there, or this process cannot read it), and the page then shows "n/a"
// instead of invented numbers.
type PeerFeedStats struct {
	QueueDepth int64
	Offered    int64
	Accepted   int64
	Refused    int64
	Rejected   int64
	Deferred   int64
	LastError  string
}

// FeedQueueReader is what this page needs from the outgoing feed queue file: the per-peer
// counters the sender stored there. The queue of internal/feed implements it.
type FeedQueueReader interface {
	ReadStats() (map[int64]feed.Stats, error)
	Close() error
}

// FeedQueueOpener opens the feed queue file at path for reading.
type FeedQueueOpener func(path string) (FeedQueueReader, error)

var (
	feedQueueMu   sync.RWMutex
	openFeedQueue FeedQueueOpener // nil: the page has no numbers to show
)

// SetFeedQueueOpener installs the opener the peers page uses to read the counters of the
// outgoing feed; cmd/web wires the queue of internal/feed (read-only). The feed queue lives in
// its own SQLite file written by the nntp-server process, so the web process only reads it, for
// one request at a time. Without an opener the page shows "n/a".
func SetFeedQueueOpener(o FeedQueueOpener) {
	feedQueueMu.Lock()
	openFeedQueue = o
	feedQueueMu.Unlock()
}

// feedQueuePath is where the feed queue file of this data root lives.
func feedQueuePath(dataDir string) string {
	return filepath.Join(dataDir, "cfg", feed.DefaultQueueFile)
}

// feedStats returns the stored counters per peer ID, or nil when there are none: no opener
// wired, no queue file yet, or an unreadable one (logged, never an error page).
func (s *WebServer) feedStats() map[int64]feed.Stats {
	feedQueueMu.RLock()
	open := openFeedQueue
	feedQueueMu.RUnlock()
	if open == nil || s.DB == nil {
		return nil
	}
	path := feedQueuePath(s.DB.GetDataDir())
	if _, err := os.Stat(path); err != nil {
		return nil // no feed queue on this host: nothing to show
	}
	q, err := open(path)
	if err != nil {
		log.Printf("[WEB]: Failed to open the feed queue %s: %v", path, err)
		return nil
	}
	if q == nil {
		return nil
	}
	defer func() {
		if err := q.Close(); err != nil {
			log.Printf("[WEB]: Failed to close the feed queue %s: %v", path, err)
		}
	}()
	stats, err := q.ReadStats()
	if err != nil {
		log.Printf("[WEB]: Failed to read the feed counters from %s: %v", path, err)
		return nil
	}
	return stats
}

// peerFeedStats copies one peer's stored counters into the view type.
func peerFeedStats(st feed.Stats) *PeerFeedStats {
	return &PeerFeedStats{
		QueueDepth: st.QueueDepth,
		Offered:    st.Offered,
		Accepted:   st.Accepted,
		Refused:    st.Refused,
		Rejected:   st.Rejected,
		Deferred:   st.Deferred,
		LastError:  st.LastError,
	}
}

// PeerView is one row of the peers page: the peer without its secrets, plus whether each
// password is set.
type PeerView struct {
	*models.Peer
	InPasswordSet  bool
	OutPasswordSet bool
	Stats          *PeerFeedStats // nil: no counters are available (see PeerFeedStats)
}

// AdminPeersPageData is the template data of admin_peers.html.
type AdminPeersPageData struct {
	TemplateData
	Peers              []*PeerView
	DefaultInPatterns  string
	DefaultOutPatterns string
	DefaultOutPort     int
	DefaultMaxConns    int
	Success            string
	Error              string
}

// newPeerView copies a peer without its password hash and its outgoing password, so no template
// can render a secret, and records whether each password is set.
func newPeerView(p *models.Peer) *PeerView {
	safe := *p
	safe.InPasswordHash = ""
	safe.OutPassword = ""
	return &PeerView{
		Peer:           &safe,
		InPasswordSet:  p.InPasswordHash != "",
		OutPasswordSet: p.OutPassword != "",
	}
}

// adminPeersPage renders the peers page.
func (s *WebServer) adminPeersPage(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	peers, err := s.DB.GetPeers()
	if err != nil {
		log.Printf("[WEB]: Failed to load peers: %v", err)
		s.renderError(c, http.StatusInternalServerError, "Database Error", "Failed to load peers")
		return
	}
	stats := s.feedStats()
	views := make([]*PeerView, 0, len(peers))
	for _, p := range peers {
		v := newPeerView(p)
		if st, ok := stats[p.ID]; ok {
			v.Stats = peerFeedStats(st)
		}
		views = append(views, v)
	}

	data := AdminPeersPageData{
		TemplateData:       s.getBaseTemplateData(c, "Admin: NNTP Peers"),
		Peers:              views,
		DefaultInPatterns:  "*",
		DefaultOutPatterns: "*,!control*,!junk",
		DefaultOutPort:     119,
		DefaultMaxConns:    4,
		Success:            session.GetSuccess(),
		Error:              session.GetError(),
	}
	s.renderPage(c, http.StatusOK, data, "admin_peers.html")
}

// adminCreatePeer handles POST /admin/peers.
func (s *WebServer) adminCreatePeer(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	peer := &models.Peer{}
	if err := peerFromForm(c, peer); err != nil {
		session.SetError("Peer not created: " + err.Error())
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	peer.OutPassword = c.PostForm("out_password")

	inPassword := c.PostForm("in_password")
	if err := peerInPasswordLen(inPassword); err != nil {
		session.SetError("Peer not created: " + err.Error())
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	if err := validatePeerForm(peer); err != nil {
		session.SetError("Peer not created: " + err.Error())
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	if msg, err := s.peerConflict(peer); err != nil {
		log.Printf("[WEB]: Failed to check peers for conflicts: %v", err)
		session.SetError("Failed to check the existing peers")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	} else if msg != "" {
		session.SetError("Peer not created: " + msg)
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	if _, err := s.DB.InsertPeer(peer, inPassword); err != nil {
		if peerUniqueConflict(err) {
			session.SetError("Peer not created: the name or the peer login is already in use")
		} else {
			log.Printf("[WEB]: Failed to create peer %q: %v", peer.Name, err)
			session.SetError("Failed to create peer")
		}
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	log.Printf("[WEB]: Admin created peer %q (id %d)", peer.Name, peer.ID)
	session.SetSuccess("Peer " + peer.Name + " created")
	c.Redirect(http.StatusSeeOther, adminPeersRedirect)
}

// adminUpdatePeer handles POST /admin/peers/update. The edit form submits every field and the
// update writes every one of them, so a field the request leaves out is cleared (and an empty
// name or Path token is then refused by ValidatePeer). The two passwords are the exception:
// they are written separately (UpdatePeer never touches them), an empty field keeps the stored
// password and the matching "clear" checkbox clears it.
func (s *WebServer) adminUpdatePeer(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	peer, ok := s.peerFromFormID(c, session)
	if !ok {
		return
	}
	if err := peerFromForm(c, peer); err != nil {
		session.SetError("Peer not updated: " + err.Error())
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	// The password fields are validated before anything is written, so a refused password
	// leaves the whole peer unchanged.
	if !peerFormBool(c, "in_password_clear") {
		if err := peerInPasswordLen(c.PostForm("in_password")); err != nil {
			session.SetError("Peer not updated: " + err.Error())
			c.Redirect(http.StatusSeeOther, adminPeersRedirect)
			return
		}
	}
	if err := validatePeerForm(peer); err != nil {
		session.SetError("Peer not updated: " + err.Error())
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	if msg, err := s.peerConflict(peer); err != nil {
		log.Printf("[WEB]: Failed to check peers for conflicts: %v", err)
		session.SetError("Failed to check the existing peers")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	} else if msg != "" {
		session.SetError("Peer not updated: " + msg)
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	if err := s.DB.UpdatePeer(peer); err != nil {
		if peerUniqueConflict(err) {
			session.SetError("Peer not updated: the name or the peer login is already in use")
		} else {
			log.Printf("[WEB]: Failed to update peer %d: %v", peer.ID, err)
			session.SetError("Failed to update peer")
		}
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	// Passwords: clear wins over a typed password, an empty field changes nothing.
	if err := s.setPeerPassword(peer, c, "in"); err != nil {
		log.Printf("[WEB]: Failed to update the peer login password of peer %d: %v", peer.ID, err)
		session.SetError("Peer updated, but the peer login password was not changed")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	if err := s.setPeerPassword(peer, c, "out"); err != nil {
		log.Printf("[WEB]: Failed to update the outgoing password of peer %d: %v", peer.ID, err)
		session.SetError("Peer updated, but the outgoing password was not changed")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	log.Printf("[WEB]: Admin updated peer %q (id %d)", peer.Name, peer.ID)
	session.SetSuccess("Peer " + peer.Name + " updated")
	c.Redirect(http.StatusSeeOther, adminPeersRedirect)
}

// adminDeletePeer handles POST /admin/peers/delete. Only a disabled peer is deleted: a peer
// that is still feeding or being fed is not removed by one click (the page disables the button
// for an enabled peer, and this is the same rule for every other client).
func (s *WebServer) adminDeletePeer(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	peer, ok := s.peerFromFormID(c, session)
	if !ok {
		return
	}
	if peer.Enabled {
		session.SetError("Peer " + peer.Name + " is enabled: disable it first")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}
	if err := s.DB.DeletePeer(peer.ID); err != nil {
		log.Printf("[WEB]: Failed to delete peer %d: %v", peer.ID, err)
		session.SetError("Failed to delete peer")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	log.Printf("[WEB]: Admin deleted peer %q (id %d)", peer.Name, peer.ID)
	session.SetSuccess("Peer " + peer.Name + " deleted")
	c.Redirect(http.StatusSeeOther, adminPeersRedirect)
}

// adminTogglePeer handles POST /admin/peers/toggle: it enables a disabled peer and disables an
// enabled one. A disabled peer may neither feed us nor be fed.
func (s *WebServer) adminTogglePeer(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)

	peer, ok := s.peerFromFormID(c, session)
	if !ok {
		return
	}
	peer.Enabled = !peer.Enabled
	if err := s.DB.UpdatePeer(peer); err != nil {
		log.Printf("[WEB]: Failed to toggle peer %d: %v", peer.ID, err)
		session.SetError("Failed to change the peer")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return
	}

	state := "disabled"
	if peer.Enabled {
		state = "enabled"
	}
	log.Printf("[WEB]: Admin %s peer %q (id %d)", state, peer.Name, peer.ID)
	session.SetSuccess("Peer " + peer.Name + " " + state)
	c.Redirect(http.StatusSeeOther, adminPeersRedirect)
}

// peerFromFormID loads the peer the "id" form field names. On a missing, invalid or unknown id
// it sets the error flash, redirects and returns false, so the caller just returns.
func (s *WebServer) peerFromFormID(c *Context, session *SessionData) (*models.Peer, bool) {
	idStr := strings.TrimSpace(c.PostForm("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if idStr == "" || err != nil || id <= 0 {
		session.SetError("Invalid peer ID")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return nil, false
	}
	peer, err := s.DB.GetPeerByID(id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[WEB]: Failed to load peer %d: %v", id, err)
		}
		session.SetError("Peer not found")
		c.Redirect(http.StatusSeeOther, adminPeersRedirect)
		return nil, false
	}
	return peer, true
}

// setPeerPassword writes one of the two peer passwords from the form fields "<side>_password"
// and "<side>_password_clear": the clear checkbox clears the stored password, a non-empty field
// replaces it, an empty field changes nothing. side is "in" or "out".
func (s *WebServer) setPeerPassword(peer *models.Peer, c *Context, side string) error {
	password := c.PostForm(side + "_password")
	wipe := peerFormBool(c, side+"_password_clear")
	if !wipe && password == "" {
		return nil
	}
	if wipe {
		password = ""
	}
	if side == "in" {
		return s.DB.SetPeerInPassword(peer.ID, password)
	}
	return s.DB.SetPeerOutPassword(peer.ID, password)
}

// peerConflict reports the admin-readable reason why p cannot be written: another peer already
// has its name or its peer login. It is a check before the write; the UNIQUE indexes of the
// peers table stay the authority (see peerUniqueConflict).
func (s *WebServer) peerConflict(p *models.Peer) (string, error) {
	peers, err := s.DB.GetPeers()
	if err != nil {
		return "", err
	}
	for _, other := range peers {
		if other.ID == p.ID {
			continue
		}
		if other.Name == p.Name {
			return "a peer named " + p.Name + " already exists", nil
		}
		if p.InUsername != "" && other.InUsername == p.InUsername {
			return "the peer login " + p.InUsername + " is already used by peer " + other.Name, nil
		}
	}
	return "", nil
}

// peerInPasswordLen checks a new peer login password. That password is ours to choose, so it
// uses the bounds of the NNTP user form (bcrypt ignores everything after byte 72 and refuses a
// longer password). An empty password is fine: it means "keep the stored one" on an edit and
// "no password" on a create. The outgoing password is the remote's choice and is not bounded.
func peerInPasswordLen(password string) error {
	if password == "" || (len(password) >= nntpAdminPasswordMin && len(password) <= nntpAdminPasswordMax) {
		return nil
	}
	return fmt.Errorf("the peer login password must be between %d and %d bytes", nntpAdminPasswordMin, nntpAdminPasswordMax)
}

// peerUniqueConflict reports whether err is a UNIQUE violation, that is the name column or the
// in_username index of the peers table, so the admin gets a message instead of a 500. The
// extended result code is what decides (as in database.IsWebUserMappingConflict), never the
// error text.
func peerUniqueConflict(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique
}

// peerFormBool reads a checkbox: on when any submitted value is "on", off when the field is
// absent. Every value is looked at, so the hidden "off" the edit form sends in front of each
// checkbox (which makes an unchecked box reach the handler) does not mask the checked box.
func peerFormBool(c *Context, key string) bool {
	for _, v := range c.PostFormArray(key) {
		switch v {
		case "on", "true", "1":
			return true
		}
	}
	return false
}

// peerFormInt reads an optional number. An empty field is reported as not given, so the caller
// keeps the value the peer already has (and a create, whose peer is zero, lets ValidatePeer
// fill the column default). The returned error is shown to the admin.
func peerFormInt(c *Context, key, label string) (n int, given bool, err error) {
	raw := strings.TrimSpace(c.PostForm(key))
	if raw == "" {
		return 0, false, nil
	}
	n, err = strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false, errors.New(label + " must be a number")
	}
	return n, true, nil
}

// peerFromForm fills every editable field of peer from the form (the passwords are handled
// separately). An empty number field leaves the stored port or connection limit alone, so an
// edit that submits no number does not reset it to the column default. The peer is not
// validated here: database.ValidatePeer does that and fills the defaults of the empty fields.
// validatePeerForm is database.ValidatePeer plus the out_flags parser of the outgoing feed:
// internal/database cannot call internal/feed (feed imports database), and a peer whose
// out_flags do not parse is never fed, so the admin must see the error here.
func validatePeerForm(peer *models.Peer) error {
	if err := database.ValidatePeer(peer); err != nil {
		return err
	}
	if _, err := feed.ParseFlags(peer.OutFlags); err != nil {
		return fmt.Errorf("peer %q: out_flags: %w", peer.Name, err)
	}
	return nil
}

func peerFromForm(c *Context, peer *models.Peer) error {
	outPort, outPortGiven, err := peerFormInt(c, "out_port", "Outgoing port")
	if err != nil {
		return err
	}
	maxConns, maxConnsGiven, err := peerFormInt(c, "max_conns", "Max connections")
	if err != nil {
		return err
	}
	peer.Name = strings.TrimSpace(c.PostForm("name"))
	peer.PathToken = strings.TrimSpace(c.PostForm("path_token"))
	peer.Hosts = strings.TrimSpace(c.PostForm("hosts"))
	peer.InUsername = strings.TrimSpace(c.PostForm("in_username"))
	peer.InPatterns = strings.TrimSpace(c.PostForm("in_patterns"))
	peer.OutEnabled = peerFormBool(c, "out_enabled")
	peer.OutHost = strings.TrimSpace(c.PostForm("out_host"))
	if outPortGiven {
		peer.OutPort = outPort
	}
	peer.OutTLS = peerFormBool(c, "out_tls")
	peer.OutUsername = strings.TrimSpace(c.PostForm("out_username"))
	peer.OutPatterns = strings.TrimSpace(c.PostForm("out_patterns"))
	peer.OutFlags = strings.TrimSpace(c.PostForm("out_flags"))
	if maxConnsGiven {
		peer.MaxConns = maxConns
	}
	peer.Enabled = peerFormBool(c, "enabled")
	peer.Notes = strings.TrimSpace(c.PostForm("notes"))
	return nil
}
