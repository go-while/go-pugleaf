package main

// Input sources for the tracking DB (plan mbox-import-tracking).
const (
	sourceCLI     = "cli"     // a positional argument (file or directory entry)
	sourceIAFetch = "iafetch" // a row of iafetch's files table (-from-iafetch)
)

// plannedInput is one input known before the import: from the command line or from
// iafetch's DB. Supported is false for a file the importer cannot read (Reason says why:
// "iso", "txt", "missing", "size-changed", ...); such inputs are recorded, not imported.
type plannedInput struct {
	Path       string // the file to open (absolute or relative to the working directory)
	Source     string // sourceCLI or sourceIAFetch
	Identifier string // archive.org item identifier ("" for sourceCLI)
	Name       string // the file name within the item, or the base name for sourceCLI
	Size       int64  // bytes; from iafetch's DB, or the file's size on disk for sourceCLI
	Mtime      int64  // modification time in Unix seconds on disk; 0 when the file is missing
	MD5        string // hex md5 from iafetch's DB ("" when unknown)
	Supported  bool
	Reason     string // why not supported, or why pending ("" when Supported)
}
