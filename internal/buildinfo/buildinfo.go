// Package buildinfo carries link-time build metadata.
package buildinfo

// Version is stamped by goreleaser via -ldflags "-X .../buildinfo.Version=v...";
// unlinked builds (go run, go test) report "dev".
var Version = "dev"
