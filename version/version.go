// Package version holds kage's build version, set via -ldflags -X at build
// time (see flake.nix). Left at "dev" for plain `go build`/`go run`.
package version

var Version = "dev"
