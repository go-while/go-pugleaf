package database

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// Peers (table peers, migration 0034, plan nntp-peering). Only a peer may feed us with IHAVE,
// MODE STREAM, CHECK and TAKETHIS; the outgoing feed sends to peers with out_enabled.

// Bounds of peers.in_max_size (bytes) when it is not 0 (0 = the server limit, -maxartsize).
// The upper bound keeps the drain limit of an oversized article within 64 MiB.
const (
	MinPeerInMaxSize int64 = 1024
	MaxPeerInMaxSize int64 = 16 << 20
)

// ErrPeerAuth is returned by AuthenticatePeer for an unknown login, a wrong password or a
// disabled peer (one error for all three: no login enumeration).
var ErrPeerAuth = errors.New("peer authentication failed")

const peerColumns = `id, name, path_token, hosts, in_username, in_password_hash, in_patterns, in_max_size,
	out_enabled, out_host, out_port, out_tls, out_username, out_password, out_patterns, out_flags,
	max_conns, enabled, notes, created_at, updated_at`

func scanPeer(scan func(dest ...interface{}) error) (*models.Peer, error) {
	var p models.Peer
	err := scan(&p.ID, &p.Name, &p.PathToken, &p.Hosts, &p.InUsername, &p.InPasswordHash, &p.InPatterns, &p.InMaxSize,
		&p.OutEnabled, &p.OutHost, &p.OutPort, &p.OutTLS, &p.OutUsername, &p.OutPassword, &p.OutPatterns, &p.OutFlags,
		&p.MaxConns, &p.Enabled, &p.Notes, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (db *Database) queryPeers(where string, args ...interface{}) ([]*models.Peer, error) {
	rows, err := RetryableQuery(db.mainDB, `SELECT `+peerColumns+` FROM peers `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []*models.Peer
	for rows.Next() {
		p, err := scanPeer(rows.Scan)
		if err != nil {
			return nil, err
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// GetPeers returns all peers ordered by name.
func (db *Database) GetPeers() ([]*models.Peer, error) {
	return db.queryPeers("")
}

// GetEnabledPeers returns the enabled peers ordered by name.
func (db *Database) GetEnabledPeers() ([]*models.Peer, error) {
	return db.queryPeers("WHERE enabled = 1")
}

// GetPeerByID returns one peer; sql.ErrNoRows when there is none.
func (db *Database) GetPeerByID(id int64) (*models.Peer, error) {
	return db.getPeer("id = ?", id)
}

// GetPeerByName returns one peer; sql.ErrNoRows when there is none.
func (db *Database) GetPeerByName(name string) (*models.Peer, error) {
	return db.getPeer("name = ?", name)
}

func (db *Database) getPeer(where string, arg interface{}) (*models.Peer, error) {
	return scanPeer(func(dest ...interface{}) error {
		return RetryableQueryRowScan(db.mainDB, `SELECT `+peerColumns+` FROM peers WHERE `+where, []interface{}{arg}, dest...)
	})
}

// ValidatePeer checks a peer before it is written: a name, a Path token without '!' or white
// space, hosts entries that are IPs, CIDRs or host names, ports and max_conns in range, and an
// out_host when the outgoing feed is enabled, and hosts or an in_username (a peer with neither
// can never be matched). It also fills empty pattern fields with the
// table defaults.
func ValidatePeer(p *models.Peer) error {
	p.Name = strings.TrimSpace(p.Name)
	p.PathToken = strings.TrimSpace(p.PathToken)
	p.InUsername = strings.TrimSpace(p.InUsername)
	p.OutHost = strings.TrimSpace(p.OutHost)
	if p.Name == "" {
		return errors.New("peer name is empty")
	}
	if p.PathToken == "" || strings.ContainsAny(p.PathToken, "! \t\r\n") {
		return fmt.Errorf("peer %q: path token %q must be one Path element (no '!' or spaces)", p.Name, p.PathToken)
	}
	for _, h := range ParsePeerHosts(p.Hosts) {
		if !validPeerHost(h) {
			return fmt.Errorf("peer %q: hosts entry %q is not an IP, CIDR or host name", p.Name, h)
		}
	}
	p.Hosts = strings.Join(ParsePeerHosts(p.Hosts), ",")
	if p.Hosts == "" && p.InUsername == "" {
		return fmt.Errorf("peer %q: needs hosts or a login (in_username), else it can never be matched", p.Name)
	}
	if strings.TrimSpace(p.InPatterns) == "" {
		p.InPatterns = "*"
	}
	if strings.TrimSpace(p.OutPatterns) == "" {
		p.OutPatterns = "*,!control*,!junk"
	}
	if p.OutPort == 0 {
		p.OutPort = 119
	}
	if p.OutPort < 1 || p.OutPort > 65535 {
		return fmt.Errorf("peer %q: out_port %d out of range", p.Name, p.OutPort)
	}
	if p.MaxConns == 0 {
		p.MaxConns = 4
	}
	if p.MaxConns < 1 || p.MaxConns > 64 {
		return fmt.Errorf("peer %q: max_conns %d out of range 1..64", p.Name, p.MaxConns)
	}
	if p.InMaxSize != 0 && (p.InMaxSize < MinPeerInMaxSize || p.InMaxSize > MaxPeerInMaxSize) {
		return fmt.Errorf("peer %q: in_max_size %d out of range (0 = server limit, or %d..%d bytes)",
			p.Name, p.InMaxSize, MinPeerInMaxSize, MaxPeerInMaxSize)
	}
	if p.OutEnabled && p.OutHost == "" {
		return fmt.Errorf("peer %q: outgoing feed enabled without out_host", p.Name)
	}
	return nil
}

// ParsePeerHosts splits a peers.hosts value (comma or white space separated) into its entries.
func ParsePeerHosts(hosts string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(hosts, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func validPeerHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(h); err == nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// InsertPeer validates and inserts a peer and returns its id. inPassword (may be empty) is
// stored as a bcrypt hash; p.OutPassword is stored as given.
func (db *Database) InsertPeer(p *models.Peer, inPassword string) (int64, error) {
	if err := ValidatePeer(p); err != nil {
		return 0, err
	}
	if inPassword != "" && p.InUsername == "" {
		return 0, fmt.Errorf("peer %q: a login password needs a login name (in_username)", p.Name)
	}
	hash, err := hashPeerPassword(inPassword)
	if err != nil {
		return 0, err
	}
	res, err := RetryableExec(db.mainDB, `INSERT INTO peers (name, path_token, hosts, in_username, in_password_hash, in_patterns, in_max_size,
		out_enabled, out_host, out_port, out_tls, out_username, out_password, out_patterns, out_flags, max_conns, enabled, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.PathToken, p.Hosts, p.InUsername, hash, p.InPatterns, p.InMaxSize,
		p.OutEnabled, p.OutHost, p.OutPort, p.OutTLS, p.OutUsername, p.OutPassword, p.OutPatterns, p.OutFlags,
		p.MaxConns, p.Enabled, p.Notes)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	p.ID = id
	return id, nil
}

// UpdatePeer validates and writes every field of p except the two passwords (see
// SetPeerInPassword and SetPeerOutPassword), so an edit form never has to show them back.
func (db *Database) UpdatePeer(p *models.Peer) error {
	if err := ValidatePeer(p); err != nil {
		return err
	}
	res, err := RetryableExec(db.mainDB, `UPDATE peers SET name = ?, path_token = ?, hosts = ?, in_username = ?, in_patterns = ?, in_max_size = ?,
		out_enabled = ?, out_host = ?, out_port = ?, out_tls = ?, out_username = ?, out_patterns = ?, out_flags = ?,
		max_conns = ?, enabled = ?, notes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		p.Name, p.PathToken, p.Hosts, p.InUsername, p.InPatterns, p.InMaxSize,
		p.OutEnabled, p.OutHost, p.OutPort, p.OutTLS, p.OutUsername, p.OutPatterns, p.OutFlags,
		p.MaxConns, p.Enabled, p.Notes, p.ID)
	if err != nil {
		return err
	}
	return requireOneRow(res, p.ID)
}

// SetPeerInPassword sets the password the peer logs in with (bcrypt); empty clears it.
func (db *Database) SetPeerInPassword(id int64, password string) error {
	hash, err := hashPeerPassword(password)
	if err != nil {
		return err
	}
	res, err := RetryableExec(db.mainDB, `UPDATE peers SET in_password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, hash, id)
	if err != nil {
		return err
	}
	return requireOneRow(res, id)
}

// SetPeerOutPassword sets the password we send to the peer (stored as given); empty clears it.
func (db *Database) SetPeerOutPassword(id int64, password string) error {
	res, err := RetryableExec(db.mainDB, `UPDATE peers SET out_password = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, password, id)
	if err != nil {
		return err
	}
	return requireOneRow(res, id)
}

// DeletePeer removes a peer.
func (db *Database) DeletePeer(id int64) error {
	res, err := RetryableExec(db.mainDB, `DELETE FROM peers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireOneRow(res, id)
}

func requireOneRow(res interface{ RowsAffected() (int64, error) }, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("peer %d not found", id)
	}
	return nil
}

func hashPeerPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash peer password: %w", err)
	}
	return string(h), nil
}

// AuthenticatePeer checks an AUTHINFO login against the enabled peers' in_username and
// in_password_hash (not nntp_users). Unknown logins spend the same bcrypt time as known ones.
// Peer logins are rare, so there is no cache.
func (db *Database) AuthenticatePeer(username, password string) (*models.Peer, error) {
	if username == "" {
		return nil, ErrPeerAuth
	}
	p, err := db.getPeer("in_username = ? AND enabled = 1", username)
	if err != nil || p.InPasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(nntpDummyHash(), []byte(password))
		return nil, ErrPeerAuth
	}
	if bcrypt.CompareHashAndPassword([]byte(p.InPasswordHash), []byte(password)) != nil {
		return nil, ErrPeerAuth
	}
	return p, nil
}

// HistoryRejectDays returns the config key HistoryRejectDays: how many days a rejected
// message-id stays in history (0 = as long as history). Missing, invalid or negative values read
// as config.DefaultHistoryRejectDays.
func (db *Database) HistoryRejectDays() int {
	v, err := db.GetConfigValue(config.CFG_KEY_HISTORY_REJECT_DAYS)
	if err != nil {
		log.Printf("Database: Failed to read %s, using %d: %v", config.CFG_KEY_HISTORY_REJECT_DAYS, config.DefaultHistoryRejectDays, err)
		return config.DefaultHistoryRejectDays
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return config.DefaultHistoryRejectDays
	}
	return n
}
