package web

import "strings"

// Pattern presets of the admin peers page: the newsdeef.eu community defaults that
// pugleaf.net publishes for INN ($NEWSDEEFDEFAULT, $NEWSDEEFNOBINARY in newsfeeds). Picking
// a preset fills the patterns field with the expanded list (our matcher needs concrete
// patterns, not newsfeeds variables); the INN snippet writes the variables back when a peer's
// patterns are exactly a preset.

// newsdeefDefault is $NEWSDEEFDEFAULT: groups never fed between peers.
var newsdeefDefault = []string{
	"!control", "!control.*",
	"!junk", "!junk.*",
	"!local", "!local.*",
	"!ka.*", "!gmane.*", "!gwene.*",
	"!newsdeef.private.*",
}

// newsdeefNoBinary is $NEWSDEEFNOBINARY: '@' drops an article crossposted to any of these
// (binaries, porn, warez).
var newsdeefNoBinary = []string{
	"@a.b.*", "@ab.alt.*", "@ab.mom", "@alt.b.*", "@alt.*sex*",
	"@dk.b.*", "@*alt-bin*", "@*alt.bin*",
	"@*alt.dvd*", "@*alt.hdtv*", "@*dvdnordic*",
	"@*nairies*", "@*naries*", "@*.bain*", "@*.banar*", "@*.banir*",
	"@*.biana*", "@*.bianr*", "@*.biin*", "@*.binar*", "@*.binai*",
	"@*.binaer*", "@*.bineri*", "@*.biniar*", "@*.binira*",
	"@*.binrie*", "@*.biya*", "@*.boneles*", "@*cd.image*",
	"@*dateien*", "@*files.images*", "@*music.bin*",
	"@*nzb*", "@*mp3*", "@*ictures*", "@*iktures*",
	"@*erotic*", "@*porno*", "@*pedo*", "@*paedo*", "@*xxx*", "@*warez*", "@unidata.*",
}

// newsdeefMaxSize is the article size limit newsdeef peers put in their newsfeeds flags
// (<262144); as an out_flags value it is "<262144".
const newsdeefMaxSize = 262144

// PatternPreset is one entry of the preset menu of the patterns fields.
type PatternPreset struct {
	Label    string // menu text
	Patterns string // the expanded comma list put into the field
	INN      string // the same list written with newsfeeds variables ("" = Patterns)
}

func joinPatterns(parts ...[]string) string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return strings.Join(all, ",")
}

// peerPatternPresets returns the presets in menu order.
func peerPatternPresets() []PatternPreset {
	return []PatternPreset{
		{Label: "everything (*)", Patterns: "*"},
		{Label: "go-pugleaf default: * without control and junk", Patterns: "*,!control*,!junk"},
		{Label: "newsdeef default ($NEWSDEEFDEFAULT)",
			Patterns: joinPatterns([]string{"*"}, newsdeefDefault),
			INN:      "*,$NEWSDEEFDEFAULT"},
		{Label: "newsdeef text only ($NEWSDEEFDEFAULT + $NEWSDEEFNOBINARY)",
			Patterns: joinPatterns([]string{"*"}, newsdeefDefault, newsdeefNoBinary),
			INN:      "*,$NEWSDEEFDEFAULT,$NEWSDEEFNOBINARY"},
	}
}

// newsdeefINNDefinitions are the newsfeeds variable definitions the INN snippet prints when
// it uses them, in pugleaf.net's layout.
func newsdeefINNDefinitions(withNoBinary bool) string {
	var b strings.Builder
	writeVar := func(name string, pats []string) {
		b.WriteString("$" + name + "=\\\n")
		const perLine = 5
		for i := 0; i < len(pats); i += perLine {
			end := min(i+perLine, len(pats))
			b.WriteString("    " + strings.Join(pats[i:end], ","))
			if end < len(pats) {
				b.WriteString(",\\")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	writeVar("NEWSDEEFDEFAULT", newsdeefDefault)
	if withNoBinary {
		writeVar("NEWSDEEFNOBINARY", newsdeefNoBinary)
	}
	return b.String()
}

// innPresetForm returns the newsfeeds form of a pattern list: the variable form when the list is
// exactly a newsdeef preset (with which variables it needs), else the list itself.
func innPresetForm(pats string) (form string, defaultVar, noBinaryVar bool) {
	for _, pr := range peerPatternPresets() {
		if pr.INN != "" && pr.Patterns == pats {
			return pr.INN, true, strings.Contains(pr.INN, "$NEWSDEEFNOBINARY")
		}
	}
	return pats, false, false
}
