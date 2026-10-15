package nntp

// Default exclusion patterns for administrative and unwanted groups (INN2 $DEFAULT)
// These !patterns exclude specific groups but allow crossposted articles
var DefaultNoSendPatterns = []string{
	"!control", "!control.*", // Exclude control messages
	"!junk", "!junk.*", // Exclude junk/spam groups
	"!local", "!local.*", // Exclude local server groups
	"!ka.*", "!gmane.*", "!gwene.*", // Exclude gateway and mailing list groups
}

// Default exclusion patterns for binary groups (INN2 style)
// These @patterns will reject entire articles if any newsgroup matches
var DefaultBinaryExcludePatterns = []string{
	"@dk.b.*", "@*dvdnordic*",
	"@a.b.*", "@ab.alt.*", "@ab.mom*", "@alt.b.*",
	"@*alt-bin*", "@*alt.bin*", "@*alt.dvd*", "@*alt.hdtv*",
	"@*alt.binaries*", "@*alt.binaries.dvd*", "@*alt.binaries.hdtv*",
	"@*nairies*", "@*naries*", "@*.bain*", "@*.banar*", "@*.banir*",
	"@*.biana*", "@*.bianr*", "@*.biin*", "@*.binar*", "@*.binai*",
	"@*.binaer*", "@*.bineri*", "@*.biniar*", "@*.binira*",
	"@*.binrie*", "@*.biya*", "@*.boneles*", "@*cd.image*",
	"@*dateien*", "@*.files*", "@*.newfiles*", "@*music.bin*",
	"@*nzb*", "@*mp3*", "@*ictures*", "@*iktures*",
	"@*crack*", "@*serial*", "@*warez*",
	"@unidata.*",
}

var DefaultSexExcludePatterns = []string{
	"@*erotic*", "@*gay*", "@*paedo*", "@*pedo*", "@*porn*", "@*sex*", "@*xxx*",
}
