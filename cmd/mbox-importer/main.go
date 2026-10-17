// mbox-importer imports Usenet mbox archives (archive.org collections) directly into the
// go-pugleaf databases through the processor's bulk path: every article is filed into one
// group (the first of its Newsgroups header, or -group), crossposts are not processed.
package main

import (
	"os"
)

var appVersion = "-unset-"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses args and imports the inputs; it returns the process exit code
// (0 ok, 1 error, 2 bad command line, 130 stopped by a signal).
func run(args []string) int {
	return runWith(args, nil)
}

// runWith is run with an optional stop channel instead of the signal handler (tests).
func runWith(args []string, stopChan <-chan struct{}) int {
	_ = args
	_ = stopChan
	return 1 // wave 2
}
