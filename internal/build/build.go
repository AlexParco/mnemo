// Package build is what this binary says it is: the version, where it came
// from, and the number that decides whether two machines can talk to each other.
//
// It is its own package because three different layers need it and none of them
// should depend on the others: the command layer prints it, the HTTP server
// answers it on /v1/health, and the relay refuses to work with a server whose
// api differs. Setting it from one place also means one -ldflags line rather
// than one per package.
package build

import "fmt"

// Version, Commit and Date are set at build time with -ldflags. A build from a
// working copy says so rather than claiming a release it is not.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// API is the version of the HTTP interface and the relay that speaks to it.
//
// It is not the release version, and it does not move when the release version
// does: it changes only when the interface changes in a way the other side
// cannot handle. A server and a client must have the same number, and when they
// do not, the one that is older is told to update — which is only possible
// because this number says nothing about features, only about compatibility.
const API = 1

// Line is what `mnemo version` and `mnemo --version` print.
func Line() string {
	return fmt.Sprintf("mnemo %s (commit %s, built %s, api %d)", Version, Commit, Date, API)
}
