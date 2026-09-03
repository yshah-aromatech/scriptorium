// Package buildinfo carries link-time build metadata and the one naming
// convention every release artifact (goreleaser's archives, install.sh's
// download URLs) has to agree on.
package buildinfo

import "fmt"

// Version, Commit and Date are stamped by goreleaser via
// `-ldflags "-X .../buildinfo.Version=v1.2.3 -X .../buildinfo.Commit=abcdef1
// -X .../buildinfo.Date=2026-09-02T00:00:00Z"`. Unlinked builds (go run, go
// test, a plain `go build`) report the zero-value defaults below — "dev" is
// also the self-update mode switch: internal/update and its callers treat
// Version=="dev" as "not a released binary, keep the existing git-pull path".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String is what `--version` prints.
func String() string {
	return fmt.Sprintf("scriptorium %s (commit %s, built %s)", Version, Commit, Date)
}

// AssetName is the ONE place the release-archive naming convention is
// spelled out. goreleaser's archive name_template and install.sh's download
// URL are both hand-written to match it; TestGoreleaserArchiveNameMatches
// (here) and install.sh's own cross-check test (hack/install-test) both
// verify against this function instead of against each other, so a drift in
// either file fails a test instead of shipping a broken curl-pipe install.
func AssetName(goos, arch string) string {
	return fmt.Sprintf("scriptorium_%s_%s.tar.gz", goos, arch)
}

// ChecksumsFile is goreleaser's checksum manifest name, shared by the same
// convention: install.sh downloads this exact filename from the latest
// release alongside the archive.
const ChecksumsFile = "checksums.txt"
