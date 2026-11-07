package main

// iasource reads the download DB of mbox2nntp's iafetch (../mbox2nntp/cmd/iafetch) and
// turns its recorded files into the importer's planned inputs (plan mbox-import-tracking,
// -from-iafetch). The DB is only read, never written: iafetch keeps downloading while the
// importer runs, and a row of its files table is written only after the file's size and md5
// matched the metadata and FILE.part was renamed to FILE, so a recorded file is complete
// whatever the state of its item.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3" // SQLite3 driver ("sqlite3"), as the other cmd/ tools use it
)

// Reasons iasource puts in plannedInput.Reason. The suffix reasons name the formats of the
// archive.org mirror the importer cannot read yet, so -status can list what is left for
// later tools: "iso" (CD images), "sqsh", "tar", "ufs", "img" (spool trees and images),
// "txt" (OCR derivatives, plain text), "xml" (archive.org's _files.xml / _meta.xml),
// "torrent" (_archive.torrent) and iaReasonUnsupported for any other name.
const (
	iaReasonMissing     = "missing"            // recorded, but not on disk (any more)
	iaReasonSizeChanged = "size-changed"       // on disk with another size than iafetch recorded
	iaReasonStatError   = "stat-error"         // on disk, but stat failed (permissions, ...)
	iaReasonUnsupported = "unsupported-suffix" // a name the importer has no reader for
)

// iafetchPending reports whether a reason means "try this file again later" (the tracking
// DB's pending status): the file is an input the importer can read, only its state on disk
// is not right yet — iafetch is still writing, or the file was moved away. The format
// reasons (iso, txt, ...) are final: the importer has no reader for the name (status
// unsupported).
func iafetchPending(reason string) bool {
	switch reason {
	case iaReasonMissing, iaReasonSizeChanged, iaReasonStatError:
		return true
	}
	return false
}

// iaReadableSuffixes are the suffixes openSource (inputs.go) can actually read: the plain
// mbox/bag file and the three compressed forms (inputKind). A name that passes isInputName
// but ends in another suffix (x.mbox.bz2, x.mbox.zst) has no reader, so it is unsupported.
var iaReadableSuffixes = []string{".mbox", ".bag", ".gz", ".zip", ".7z"}

// iaUnsupportedSuffixes maps the suffixes worth naming in -status to their short reason.
var iaUnsupportedSuffixes = map[string]string{
	".iso":     "iso",
	".sqsh":    "sqsh",
	".tar":     "tar",
	".txt":     "txt",
	".ufs":     "ufs",
	".img":     "img",
	".xml":     "xml",
	".torrent": "torrent",
}

// iafetchFilesQuery lists every file iafetch recorded, in the order the items were
// selected: items.ord, then identifier and name. Files whose item has no items row, or an
// items row without an ord, come last (by identifier and name); the status of the item is
// not looked at, because a recorded file is complete even while its item is downloading.
const iafetchFilesQuery = `SELECT files.identifier, files.name, files.size, files.md5, files.path
	FROM files LEFT JOIN items ON items.identifier = files.identifier
	ORDER BY items.ord IS NULL, items.ord, files.identifier, files.name`

// iafetchInputs returns the files recorded in iafetch's DB at dbPath as planned inputs, in
// import order. base is iafetch's working directory (-from-iafetch-base): a relative
// files.path is joined to it, an absolute one is used as is. A relative path with an empty
// base is an error naming the first such row, because guessing the folder is how the wrong
// tree gets read. The recorded path itself stays inside the item's directory: iafetch's
// localPath refuses an identifier or a file name that is not local (fetch.go), so the names
// of the mirror cannot escape the base.
//
// Supported is true for the names the importer can read today (iafetchSupported); any other
// name gets Supported false and a suffix reason. A supported file that is not on disk, or
// has another size than iafetch recorded, is not supported either (reason iaReasonMissing /
// iaReasonSizeChanged, both iafetchPending: worth another look on the next scan). The size
// is only compared when iafetch recorded one (> 0; it stores -1 for an unknown and 0 for an
// empty file), and when it did not, Size is taken from disk. MD5 comes from the DB, Mtime
// from disk (0 when the file is missing or cannot be stat'ed).
//
// The DB is opened read-only with one short query; it is closed before the call returns.
func iafetchInputs(dbPath, base string) ([]plannedInput, error) {
	if dbPath == "" {
		return nil, errors.New("iafetch DB: no path given")
	}
	db, err := sql.Open("sqlite3", iafetchDSN(dbPath))
	if err != nil {
		return nil, iafetchReadError(dbPath, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	rows, err := db.Query(iafetchFilesQuery)
	if err != nil {
		return nil, iafetchReadError(dbPath, err)
	}
	defer rows.Close()

	var inputs []plannedInput
	for rows.Next() {
		var identifier, name, md5, path string
		var size int64
		if err := rows.Scan(&identifier, &name, &size, &md5, &path); err != nil {
			return nil, fmt.Errorf("iafetch DB %q: %w", dbPath, err)
		}
		in, err := iafetchInput(identifier, name, size, md5, path, base)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iafetch DB %q: %w", dbPath, err)
	}
	return inputs, nil
}

// iafetchDSN is the read-only DSN of iafetch's DB. The path is URI-escaped (as iafetch
// itself builds its DSN, cmd/iafetch/db.go): the mattn driver hands a "file:" DSN to
// SQLite's URI parser with SQLITE_OPEN_URI|READWRITE|CREATE, so a '?' or '#' in the path of
// a concatenated DSN would cut the path short, drop mode=ro and open — or create — another
// file read-write. mode=ro is the only thing that keeps this reader off iafetch's DB.
func iafetchDSN(dbPath string) string {
	return "file:" + (&url.URL{Path: dbPath}).EscapedPath() + "?mode=ro&_busy_timeout=10000"
}

// iafetchInput turns one files row into a planned input (see iafetchInputs). recorded is
// the row's path as iafetch wrote it: filepath.Join(-o, identifier, name), which is
// relative when iafetch ran with a relative -o.
func iafetchInput(identifier, name string, size int64, md5, recorded, base string) (plannedInput, error) {
	if strings.TrimSpace(recorded) == "" {
		return plannedInput{}, fmt.Errorf("iafetch DB: item %q file %q has no path", identifier, name)
	}
	path := filepath.FromSlash(recorded)
	if !filepath.IsAbs(path) {
		if base == "" {
			return plannedInput{}, fmt.Errorf("iafetch DB: item %q file %q has the relative path %q: "+
				"-from-iafetch-base (iafetch's working directory) is needed to resolve it",
				identifier, name, recorded)
		}
		path = filepath.Join(base, path)
	}

	in := plannedInput{
		Path:       path,
		Source:     sourceIAFetch,
		Identifier: identifier,
		Name:       name,
		Size:       size,
		MD5:        strings.ToLower(strings.TrimSpace(md5)),
	}
	in.Supported, in.Reason = iafetchSupported(name)

	fi, err := os.Stat(path)
	switch {
	case err == nil:
		in.Mtime = fi.ModTime().Unix()
		if size > 0 {
			if in.Supported && fi.Size() != size {
				in.Supported, in.Reason = false, iaReasonSizeChanged
			}
		} else {
			// iafetch had no size to verify (-1 unknown, 0 empty file): the file on disk
			// is the only size there is, and the tracking DB needs one.
			in.Size = fi.Size()
		}
	case errors.Is(err, os.ErrNotExist):
		if in.Supported {
			in.Supported, in.Reason = false, iaReasonMissing
		}
	default:
		if in.Supported {
			in.Supported, in.Reason = false, iaReasonStatError
		}
	}
	return in, nil
}

// iafetchSupported reports whether a recorded file name is an input the importer can read
// and, if it is not, the short reason for -status. An archive.org file name can hold a
// subfolder ("sub/dir/NAME"), so only its base name decides.
//
// A name has to pass both halves of the importer's own rule: isInputName (the rule of the
// directory scan in inputs.go) and a suffix openSource has a reader for (iaReadableSuffixes
// or a UAT archive) — isInputName only looks for ".mbox"/".bag" anywhere in the name, so a
// mirror file like "x.mbox.bz2" or "x.mbox.zst" would pass it and then fail to open.
// isInputName is case-sensitive like the directory scan, so an upper-case "X.MBOX.ZIP" is
// not an input here either.
func iafetchSupported(name string) (bool, string) {
	base := filepath.Base(filepath.FromSlash(name))
	if isInputName(base) && iafetchReadable(base) {
		return true, ""
	}
	if isCSVIndexName(base) {
		return false, "csv" // NAME.mbox.csv.gz: the offset index of NAME.mbox.gz
	}
	if reason, ok := iaUnsupportedSuffixes[strings.ToLower(filepath.Ext(base))]; ok {
		return false, reason
	}
	return false, iaReasonUnsupported
}

// iafetchReadable reports whether openSource (inputs.go) has a reader for base's suffix.
func iafetchReadable(base string) bool {
	if uatSuffix(base) != "" {
		return true
	}
	lower := strings.ToLower(base)
	for _, s := range iaReadableSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// iafetchReadError wraps a failure to open iafetch's DB or to run the first query with the
// usual cause: iafetch keeps the DB in WAL mode, so a reader needs read access to the DB
// folder and to the -wal and -shm files next to the DB, not only to the DB file.
func iafetchReadError(dbPath string, err error) error {
	return fmt.Errorf("iafetch DB %q: %w (the DB folder and the -wal/-shm files next to the DB "+
		"need to be readable: iafetch keeps the DB in WAL mode)", dbPath, err)
}
