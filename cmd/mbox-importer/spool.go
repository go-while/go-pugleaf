package main

// News spool inputs (plan mbox-import-discs): a directory tree, a zip file or an unpacked
// disc image with one article per file (NetNews/CD, NetNews Offline, utzoo, InfoMagic CDs).

import (
	"errors"
	"io/fs"
)

// errSpoolNotImplemented is returned by the wave-0 stub of readSpool.
var errSpoolNotImplemented = errors.New("spool reader not implemented yet")

// readSpool walks fsys (os.DirFS of a directory or an unpacked image, or a *zip.Reader) and
// feeds every article it finds to sink: one-article files through handleMessage, mbox files
// through the mbox reader, A-news batches through parseANews. label names the input in the
// log. It returns errStopped (with the counters so far) when stop was closed between two
// articles or files.
func readSpool(fsys fs.FS, label string, opts *runOptions, sink inputSink, stop <-chan struct{}) (*counters, error) {
	_, _, _, _ = fsys, opts, sink, stop
	return newCounters(label), errSpoolNotImplemented
}
