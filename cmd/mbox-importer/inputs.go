package main

import (
	"errors"

	mbox "github.com/go-while/go-lib-mbox2nntp"
)

var errNotImplemented = errors.New("not implemented")

// source is one opened input: a plain, gzip, zip or 7z mbox stream.
type source struct {
	Path   string
	Stream *mbox.MboxStream // nil: the input is an empty file
}

// expandInputs turns the positional arguments into the list of files to import:
// a file is itself, a directory contributes its *.mbox* / *.bag* entries (not *.done),
// sorted by name, without recursion. Missing paths are reported in errs.
func expandInputs(args []string) (files []string, errs []error) {
	_ = args
	return nil, []error{errNotImplemented}
}

// openSource opens path by its suffix (.mbox/.bag, .gz, .zip, .7z via the sevenZip program).
func openSource(path, sevenZip string) (*source, error) {
	_ = path
	_ = sevenZip
	return nil, errNotImplemented
}

// Unread reports whether the parser returned io.EOF before the underlying reader did.
func (s *source) Unread() bool { return false }

// Close releases the input; for 7z it waits for the process and returns its exit error.
func (s *source) Close() error { return nil }

// markDone renames path to path+".done".
func markDone(path string) error {
	_ = path
	return errNotImplemented
}
