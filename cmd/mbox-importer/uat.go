package main

import (
	"errors"
	"os/exec"

	"github.com/go-while/mbox2nntp/uat"
)

var errUATNotImplemented = errors.New("uat: not implemented")

// uatSuffix returns the archive suffix of a UAT input (".usenet", ".usenet.xz" or
// ".usenet.7z"; a trailing ".done" is ignored) and "" for any other input.
func uatSuffix(path string) string {
	_ = path
	return ""
}

// importUAT imports one Usenet Archive Toolkit archive like importInput imports an mbox:
// the same sink, counters, mark-done rule and summary line.
func importUAT(path string, opts *runOptions, sink articleSink, stop <-chan struct{}) (c *counters, inputErr, stopped bool) {
	_, _ = exec.LookPath(opts.zstd)
	var p *uat.Package
	_ = p
	_ = sink
	_ = stop
	return newCounters(path), true, false
}

// unpackXz writes the decompressed .xz archive to dst with the xz program, or with
// 7-Zip (-txz) when xz is not found.
func unpackXz(archive, dst, xz, sevenZip string) error {
	_, _, _, _ = archive, dst, xz, sevenZip
	return errUATNotImplemented
}

// unpack7z writes the entry named like dst of a 7z archive to dst.
func unpack7z(archive, dst, sevenZip string) error {
	_, _, _ = archive, dst, sevenZip
	return errUATNotImplemented
}

// hasYEnc reports a yEnc body: a line starting with "=ybegin".
func hasYEnc(body []string) bool {
	_ = body
	return false
}
