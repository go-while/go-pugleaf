package web

// Provider presets of the admin providers page (plan fetcher-group-routing, Q8): the public
// servers newsdeef.eu lists under "Hierarchies from other servers" (same owner), with the
// hierarchies each one carries as fetcher routing. Picking a preset in the create form fills
// host, name and routing; port and TLS stay for the admin to type (119, no TLS by default) and
// nothing is inserted until the form is submitted.

// ProviderPreset is one entry of the preset menu of the provider create form.
type ProviderPreset struct {
	Host    string // NNTP host name (newsdeef lists no port or TLS)
	Name    string // suggested provider name (the fetch progress is keyed by it)
	Routing string // providers.routing value filled into the form
	Note    string // shown next to the menu ("" = none)
}

// providerPresets returns the presets in menu order (the order of the newsdeef.eu list).
func providerPresets() []ProviderPreset {
	return []ProviderPreset{
		{Host: "news.gmane.io", Name: "news.gmane.io", Routing: "gmane.*, gwene.*"},
		// newsdeef lists "tin.*, ka.*, lists.*"; tin.* also matches tin.it.* (Telecom Italia,
		// archive.org import), which news.tin.org does not carry.
		{Host: "news.tin.org", Name: "news.tin.org", Routing: "tin.*, !tin.it.*, ka.*, lists.*",
			Note: "newsdeef.eu lists tin.*, ka.*, lists.* for news.tin.org, but tin.* also matches the archive's tin.it.* (Telecom Italia): the preset adds !tin.it.* so those groups stay with the other providers."},
		{Host: "nntp.lore.kernel.org", Name: "nntp.lore.kernel.org", Routing: "com.*, dev.linux.*, net.*, org.*, tech.*"},
		{Host: "news.php.net", Name: "news.php.net", Routing: "php.*"},
		{Host: "nntp.perl.org", Name: "nntp.perl.org", Routing: "perl.*"},
		{Host: "news.povray.org", Name: "news.povray.org", Routing: "povray.*, moray.*, irtc.*"},
		{Host: "news.digitalmars.com", Name: "news.digitalmars.com", Routing: "digitalmars.*, D, D.gnu"},
		{Host: "news.grc.com", Name: "news.grc.com", Routing: "grc.*"},
		{Host: "lugnet.com", Name: "lugnet.com", Routing: "lugnet.*"},
		{Host: "fltk.org", Name: "fltk.org", Routing: "fltk.*"},
		{Host: "fysh.org", Name: "fysh.org", Routing: "fysh.*"},
		{Host: "news.dbase.com", Name: "news.dbase.com", Routing: "dbase.*"},
		{Host: "news.kraft-s.ru", Name: "news.kraft-s.ru", Routing: "kraft.*"},
		{Host: "news.hispagatos.org", Name: "news.hispagatos.org", Routing: "hispagatos.*"},
	}
}
