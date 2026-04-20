package main

import "fmt"

// buildVersion renders the --version output. The three inputs are populated at
// build time by GoReleaser's ldflags (see .goreleaser.yaml) and left at their
// "dev"/"none"/"unknown" defaults for `go run` / ad-hoc builds.
func buildVersion(version, commit, date string) string {
	return fmt.Sprintf("escrow-proxy %s (commit %s, built %s)", version, commit, date)
}
