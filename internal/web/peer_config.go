package web

import (
	"fmt"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Peering configuration for the other side: the snippets a peer's admin pastes into INN
// (incoming.conf, newsfeeds, innfeed.conf) or Diablo (dnewsfeeds) to peer with this server,
// built from our peers row. The keys follow INN's incoming.conf(5), newsfeeds(5) and
// innfeed.conf(5), and the dnewsfeeds sample of diablo-nntp (v6/v7). Passwords are never
// printed: the snippets carry a placeholder where one belongs.

// peerConfigDefaultPort is the port the snippets give for our nntp-server: the web process
// does not know the port nntp-server listens on, so the snippet says so.
const peerConfigDefaultPort = 119

// PeerConfigSnippets are the configuration texts for one peer.
type PeerConfigSnippets struct {
	INN    string
	Diablo string
}

// peerConfigSnippets builds both snippets. ourHost is this server's Path identity and host
// name (config local_nntp_hostname); empty gives a placeholder.
func peerConfigSnippets(p *models.Peer, ourHost string) PeerConfigSnippets {
	ourHost = strings.TrimSpace(ourHost)
	if ourHost == "" {
		ourHost = "news.your-site.example"
	}
	return PeerConfigSnippets{
		INN:    peerConfigINN(p, ourHost),
		Diablo: peerConfigDiablo(p, ourHost),
	}
}

// peerConfigLabel is a config label for our side: the host name with characters the
// config files do not like replaced.
func peerConfigLabel(ourHost string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, ourHost)
}

// peerFeedsUs reports whether the peer can feed us at all: it is matched by hosts or by a login.
func peerFeedsUs(p *models.Peer) bool {
	return strings.TrimSpace(p.Hosts) != "" || strings.TrimSpace(p.InUsername) != ""
}

// innPatterns is a pattern list in INN's comma form (our lists also allow white space).
func innPatterns(list, def string) string {
	pats := nntp.ParsePeerPatterns(list)
	if len(pats) == 0 {
		return def
	}
	return strings.Join(pats, ",")
}

func peerConfigINN(p *models.Peer, ourHost string) string {
	var b strings.Builder
	label := peerConfigLabel(ourHost)
	fmt.Fprintf(&b, "# INN configuration for %s to peer with %s (go-pugleaf)\n", p.PathToken, ourHost)

	if p.OutEnabled {
		b.WriteString("\n# --- incoming.conf: accept our feed ---\n")
		fmt.Fprintf(&b, "peer %s {\n", label)
		b.WriteString("    # the address(es) our feed comes from\n")
		fmt.Fprintf(&b, "    hostname:        \"%s\"\n", ourHost)
		fmt.Fprintf(&b, "    patterns:        \"%s\"\n", innPatterns(p.OutPatterns, "*"))
		if p.MaxConns > 0 {
			fmt.Fprintf(&b, "    max-connections: %d\n", p.MaxConns)
		}
		b.WriteString("    streaming:       true\n")
		if strings.TrimSpace(p.OutUsername) != "" {
			b.WriteString("    # we log in (innd checks only the password)\n")
			b.WriteString("    password:        \"<the outgoing password set for you on our side>\"\n")
		}
		b.WriteString("}\n")
	} else {
		b.WriteString("\n# incoming.conf: nothing to add, we do not feed you (outgoing feed off on our side).\n")
	}

	if peerFeedsUs(p) {
		b.WriteString("\n# --- newsfeeds: feed us through innfeed ---\n")
		b.WriteString("# The site name is our Path identity: articles that passed through us are not sent back.\n")
		form, defaultVar, noBinaryVar := innPresetForm(innPatterns(p.InPatterns, "*"))
		if defaultVar {
			b.WriteString("# newsdeef.eu pattern variables (define them once in newsfeeds):\n")
			b.WriteString(newsdeefINNDefinitions(noBinaryVar))
		}
		fmt.Fprintf(&b, "%s\\\n", ourHost)
		fmt.Fprintf(&b, "    :%s\\\n", innNoControl(form))
		b.WriteString("    :Tm:innfeed!\n")

		b.WriteString("\n# --- innfeed.conf ---\n")
		fmt.Fprintf(&b, "peer %s {\n", ourHost)
		fmt.Fprintf(&b, "    ip-name:         \"%s\"\n", ourHost)
		b.WriteString("    # the port our nntp-server listens on\n")
		fmt.Fprintf(&b, "    port-number:     %d\n", peerConfigDefaultPort)
		b.WriteString("    max-connections: 2\n")
		b.WriteString("    streaming:       true\n")
		if strings.TrimSpace(p.InUsername) != "" {
			fmt.Fprintf(&b, "    username:        \"%s\"\n", p.InUsername)
			b.WriteString("    password:        \"<the login password set for you on our side>\"\n")
		}
		b.WriteString("}\n")
		if strings.TrimSpace(p.Hosts) != "" {
			fmt.Fprintf(&b, "# We accept your feed from: %s\n", p.Hosts)
		}
	} else {
		b.WriteString("\n# newsfeeds/innfeed.conf: nothing to add, we accept no feed from you (no hosts or login on our side).\n")
	}
	return b.String()
}

// innNoControl appends !control,!control.* to a newsfeeds pattern list that does not
// mention control yet ($NEWSDEEFDEFAULT excludes it already): go-pugleaf refuses control messages, so the peer need not send them.
func innNoControl(pats string) string {
	if strings.Contains(pats, "control") || strings.Contains(pats, "$NEWSDEEFDEFAULT") {
		return pats
	}
	return pats + ",!control,!control.*"
}

// diabloGroupLines turns a pattern list into dnewsfeeds addgroup/delgroup/delgroupany lines
// (INN "!" = delgroup, "@" = delgroupany: the article is not fed if any group matches).
func diabloGroupLines(list, def string) []string {
	pats := nntp.ParsePeerPatterns(list)
	if len(pats) == 0 {
		pats = nntp.ParsePeerPatterns(def)
	}
	lines := make([]string, 0, len(pats))
	for _, pat := range pats {
		switch {
		case strings.HasPrefix(pat, "@"):
			lines = append(lines, "    delgroupany "+pat[1:])
		case strings.HasPrefix(pat, "!"):
			lines = append(lines, "    delgroup    "+pat[1:])
		default:
			lines = append(lines, "    addgroup    "+pat)
		}
	}
	return lines
}

func peerConfigDiablo(p *models.Peer, ourHost string) string {
	var b strings.Builder
	label := peerConfigLabel(ourHost)
	fmt.Fprintf(&b, "# Diablo dnewsfeeds for %s to peer with %s (go-pugleaf)\n", p.PathToken, ourHost)
	feedsUs := peerFeedsUs(p)
	if !feedsUs && !p.OutEnabled {
		b.WriteString("# Nothing to configure: we neither accept a feed from you nor feed you.\n")
		return b.String()
	}
	if feedsUs && strings.TrimSpace(p.Hosts) == "" {
		b.WriteString("# NOTE: we accept your feed by login only, but dnewslink sends no AUTHINFO:\n")
		b.WriteString("# ask us to add your feeding address(es) to your peer entry (hosts).\n")
	}
	fmt.Fprintf(&b, "\nlabel %s\n", label)
	fmt.Fprintf(&b, "    alias       %s\n", ourHost)
	if p.OutEnabled {
		b.WriteString("    # we connect to you from here\n")
		fmt.Fprintf(&b, "    inhost      %s\n", ourHost)
		if p.MaxConns > 0 {
			fmt.Fprintf(&b, "    maxconnect  %d\n", p.MaxConns)
		}
	}
	if feedsUs {
		fmt.Fprintf(&b, "    hostname    %s\n", ourHost)
		b.WriteString("    # the port our nntp-server listens on\n")
		fmt.Fprintf(&b, "    port        %d\n", peerConfigDefaultPort)
		b.WriteString("    maxparallel 2\n")
		b.WriteString("    stream      on\n")
		for _, l := range diabloGroupLines(p.InPatterns, "*") {
			b.WriteString(l + "\n")
		}
		b.WriteString("    # we refuse control messages\n")
		b.WriteString("    delgroup    control.*\n")
	}
	b.WriteString("end\n")
	if p.OutEnabled {
		fmt.Fprintf(&b, "\n# What we send you: %s (Diablo has no inbound pattern list per label;\n", innPatterns(p.OutPatterns, "*"))
		b.WriteString("# use 'filter' lines in this label to discard groups you do not want from us).\n")
	}
	if feedsUs && strings.TrimSpace(p.Hosts) != "" {
		fmt.Fprintf(&b, "# We accept your feed from: %s\n", p.Hosts)
	}
	return b.String()
}
