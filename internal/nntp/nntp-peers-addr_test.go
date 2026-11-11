package nntp

// G20 of the plan e2e-gap-tests: the address matching behind MatchIP / MatchAnyIP, i.e.
// hostsMatch over a peers.hosts list of literal addresses and CIDRs.
//
// Source of the cases: INN tests/lib/network/addr-ipv6-t.c and addr-ipv4-t.c
// (network_addr_match with a prefix length, and network_sockaddr_equal for the IPv4-mapped
// pairs). Their rules, which this test pins:
//   - a prefix length of 0 matches every address of that family, 128 / 32 only the address;
//   - an invalid prefix length (-1, 129, 33) and an invalid address match nothing and must
//     not panic: the row is simply unusable, never an open door;
//   - an IPv4-mapped IPv6 address (::ffff:a.b.c.d) is the IPv4 address, in both directions;
//   - the two families never match each other (a ::/0 row does not admit IPv4).
//
// TestPeerMatchIP (nntp-peers_test.go) covers the enabled/login/host-name side of the same
// functions; only the address arithmetic is new here. Identifiers use the pa prefix. No
// wall clock and no real resolver: both are injected.

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// paStore is a PeerStore that also implements PeerAllStore, so one case can be run through
// MatchIP (enabled peers) and MatchAnyIP (every row) at once. No login is possible.
type paStore struct{ peers []*models.Peer }

func (s *paStore) GetEnabledPeers() ([]*models.Peer, error) { return s.peers, nil }
func (s *paStore) GetPeers() ([]*models.Peer, error)        { return s.peers, nil }
func (s *paStore) AuthenticatePeer(string, string) (*models.Peer, error) {
	return nil, database.ErrPeerAuth
}

// paResult is what one hosts/address pair does.
type paResult struct {
	match    bool     // MatchIP returned the peer
	matchAny bool     // MatchAnyIP returned the peer
	resolved []string // the hosts entries handed to the resolver
}

// paMatch runs ip against a registry that holds one enabled peer row with the given hosts.
// The resolver fails every name and records it, so an entry that is neither an address nor
// a CIDR can never match, and the test can tell an address apart from a name.
func paMatch(t *testing.T, hosts, ip string) paResult {
	t.Helper()
	cip := net.ParseIP(ip)
	if cip == nil {
		t.Fatalf("test address %q does not parse", ip)
	}
	p := psRow(hosts, true)
	reg := peerTestRegistry(&paStore{peers: []*models.Peer{p}}, peerTestClockAt(), nil)
	var res paResult
	reg.resolve = func(_ context.Context, name string) ([]net.IP, error) {
		res.resolved = append(res.resolved, name)
		return nil, fmt.Errorf("no such host %q", name)
	}
	res.match = reg.MatchIP(cip) == p
	res.matchAny = reg.MatchAnyIP(cip) == p
	return res
}

// paCase is one peers.hosts value against one client address.
type paCase struct {
	name  string
	hosts string
	ip    string
	want  bool
	why   string // the INN case or rule this comes from
}

// paRun checks one case through both entry points.
func paRun(t *testing.T, tc paCase) {
	t.Helper()
	res := paMatch(t, tc.hosts, tc.ip)
	if res.match != tc.want {
		t.Errorf("MatchIP(%s) with hosts %q = %v, want %v (%s)", tc.ip, tc.hosts, res.match, tc.want, tc.why)
	}
	if res.matchAny != tc.want {
		t.Errorf("MatchAnyIP(%s) with hosts %q = %v, want %v (%s)", tc.ip, tc.hosts, res.matchAny, tc.want, tc.why)
	}
	if len(res.resolved) != 0 {
		t.Errorf("hosts %q was handed to the resolver as a host name %q: an address or a "+
			"prefix must never cost a DNS lookup", tc.hosts, res.resolved)
	}
}

// TestPeerAddrIPv6: IPv6 prefix lengths, from INN addr-ipv6-t.c.
func TestPeerAddrIPv6(t *testing.T) {
	const full = "FEDC:BA98:7654:3210:FEDC:BA98:7654:3210"
	for _, tc := range []paCase{
		{"exact", full, full, true, "compare a a (null)"},
		{"exact-lowercase", full, "fedc:ba98:7654:3210:fedc:ba98:7654:3210", true, "the same address"},
		{"exact-128", full + "/128", full, true, "compare a a 128"},
		{"exact-60", full + "/60", full, true, "compare a a 60"},
		{"other-address", full, "fedc:ba98:7654:3210:fedc:ba98:7654:3211", false, "compare a b (null)"},
		{"zeros-128", "::127/128", "0:0::127", true, "compare ::127 0:0::127 128"},
		{"zeros-120", "::127/120", "0:0::128", true, "compare ::127 0:0::128 120"},
		{"zeros-128-off", "::127/128", "0:0::128", false, "compare ::127 0:0::128 128"},
		{"boundary-113", "::7fff/113", "0:0::8000", false, "compare ::7fff 0:0::8000 113"},
		{"boundary-112", "::7fff/112", "0:0::8000", true, "compare ::7fff 0:0::8000 112"},
		{"group-120", "::3:ffff/120", "::2:ffff", false, "compare ::3:ffff ::2:ffff 120"},
		{"group-119", "::3:ffff/119", "::2:ffff", false, "compare ::3:ffff ::2:ffff 119"},
		{"prefix-1", "7fff::1/1", "ffff::1", false, "compare ffff::1 7fff::1 1"},
		{"prefix-0", "7fff::1/0", "ffff::1", true, "compare ffff::1 7fff::1 0"},
		{"prefix-0-any", "::/0", "2001:db8::1", true, "a /0 row admits the whole family"},
		// invalid prefix lengths and addresses: no match, no panic
		{"prefix-129", "ffff::1/129", "ffff::1", false, "compare ffff::1 ffff::1 129"},
		{"prefix-minus-1", "ffff::1/-1", "ffff::1", false, "compare ffff::1 ffff::1 -1"},
		{"prefix-empty", "ffff::1/", "ffff::1", false, "a prefix length that is not a number"},
		{"prefix-text", "ffff::1/pete", "ffff::1", false, "compare 127.0.0.1 127.0.0.1 pete"},
		{"address-invalid", "fffg::1", "ffff::1", false, "compare fffg::1 fffg::1 (null)"},
		// the two families stay apart (network_addr_match compares within one family)
		{"v6-prefix-0-vs-v4", "::/0", "127.0.0.1", false, "a /0 IPv6 row must not admit IPv4"},
		{"v4-prefix-0-vs-v6", "0.0.0.0/0", "2001:db8::1", false, "a /0 IPv4 row must not admit IPv6"},
	} {
		t.Run(tc.name, func(t *testing.T) { paRun(t, tc) })
	}
}

// TestPeerAddrIPv4: IPv4 prefix lengths, from INN addr-ipv4-t.c.
func TestPeerAddrIPv4(t *testing.T) {
	for _, tc := range []paCase{
		{"exact", "127.0.0.1", "127.0.0.1", true, "compare 127.0.0.1 127.0.0.1 (null)"},
		{"other-address", "127.0.0.1", "127.0.0.2", false, "compare 127.0.0.1 127.0.0.2 (null)"},
		{"prefix-31", "127.0.0.0/31", "127.0.0.1", true, "compare 127.0.0.1 127.0.0.0 31"},
		{"prefix-32", "127.0.0.0/32", "127.0.0.1", false, "compare 127.0.0.1 127.0.0.0 32"},
		{"prefix-24", "10.10.4.255/24", "10.10.4.5", true, "compare 10.10.4.5 10.10.4.255 24"},
		{"prefix-25", "10.10.4.255/25", "10.10.4.5", false, "compare 10.10.4.5 10.10.4.255 25"},
		{"prefix-1", "1.0.0.0/1", "129.0.0.0", false, "compare 129.0.0.0 1.0.0.0 1"},
		{"prefix-0", "1.0.0.0/0", "129.0.0.0", true, "compare 129.0.0.0 1.0.0.0 0"},
		{"prefix-33", "127.0.0.1/33", "127.0.0.1", false, "compare 127.0.0.1 127.0.0.1 33"},
		{"prefix-minus-1", "127.0.0.1/-1", "127.0.0.1", false, "compare 127.0.0.1 127.0.0.1 -1"},
		{"prefix-text", "127.0.0.1/1p", "127.0.0.1", false, "compare 127.0.0.1 127.0.0.1 1p"},
		// INN also takes a dotted mask (255.255.255.0); peers.hosts is CIDR only, and
		// validPeerHost refuses the dotted form, so such a row matches nothing
		{"dotted-mask", "10.10.4.255/255.255.255.0", "10.10.4.5", false, "compare ... 255.255.255.0"},
		// a hosts list: every entry is tried, in order
		{"list-second-entry", "192.0.2.1, 10.10.4.0/24", "10.10.4.5", true, "a two-entry hosts list"},
		{"list-no-entry", "192.0.2.1 10.10.4.0/24", "10.10.5.5", false, "a two-entry hosts list"},
	} {
		t.Run(tc.name, func(t *testing.T) { paRun(t, tc) })
	}
}

// TestPeerAddrV4Mapped: an IPv4-mapped IPv6 address is the IPv4 address, whichever side it
// is written on (INN addr-ipv6-t.c, network_sockaddr_equal of ::ffff:7f00:1 and 127.0.0.1).
// It matters on the wire: a connection accepted by a dual-stack listener arrives with a
// v4-mapped RemoteAddr, and it must still match the plain IPv4 row of its peer.
func TestPeerAddrV4Mapped(t *testing.T) {
	for _, tc := range []paCase{
		{"client-mapped-vs-row", "127.0.0.1", "::ffff:127.0.0.1", true, "sockaddr_equal of the mapped address"},
		{"client-mapped-hex-vs-row", "127.0.0.1", "::ffff:7f00:1", true, "the same address in hex"},
		{"client-mapped-other", "127.0.0.1", "::ffff:127.0.0.2", false, "...but not some other address"},
		{"row-mapped-vs-client", "::ffff:127.0.0.1", "127.0.0.1", true, "...and the other way around"},
		{"row-mapped-hex-vs-client", "::ffff:7f00:1", "127.0.0.1", true, "the same address in hex"},
		{"row-mapped-other", "::ffff:127.0.0.1", "127.0.0.2", false, "...and not some other address"},
		{"client-mapped-vs-cidr", "127.0.0.0/24", "::ffff:127.0.0.1", true, "a mapped client in an IPv4 prefix"},
		{"client-mapped-vs-cidr-off", "127.0.1.0/24", "::ffff:127.0.0.1", false, "...and outside it"},
		// A prefix on the mapped form is the IPv4 prefix 96 bits shorter (RFC 4291 §2.5.5.2),
		// for a mapped and for a plain IPv4 client alike: ::ffff:127.0.0.0/120 is 127.0.0.0/24
		// and ::ffff:0:0/96 is every IPv4 address. INN's network_addr_match is stricter here
		// (inet_pton(AF_INET6, "127.0.0.1") fails, so it never compares the pair), but the
		// reading below is the right one; write IPv4 ranges as IPv4 prefixes anyway.
		{"row-mapped-cidr-vs-client", "::ffff:127.0.0.0/120", "127.0.0.1", true, "= 127.0.0.0/24"},
		{"row-mapped-cidr-vs-client-off", "::ffff:127.0.1.0/120", "127.0.0.1", false, "= 127.0.1.0/24"},
		{"row-mapped-cidr-vs-mapped", "::ffff:127.0.0.0/120", "::ffff:127.0.0.1", true, "= 127.0.0.0/24"},
		{"row-mapped-all-v4", "::ffff:0:0/96", "::ffff:127.0.0.1", true, "= 0.0.0.0/0"},
		{"row-mapped-all-v4-plain-client", "::ffff:0:0/96", "198.51.100.7", true, "= 0.0.0.0/0, any IPv4"},
		// shorter than /96 the entry is no longer a mapped prefix, so it stays IPv6-only
		{"row-mapped-cidr-64", "::ffff:127.0.0.0/64", "127.0.0.1", false, "::/64 is not an IPv4 prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) { paRun(t, tc) })
	}
}
