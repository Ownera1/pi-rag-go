// Package version holds the release version and commit, set at build time
// with -ldflags "-X".
package version

import "runtime/debug"

var Version = "dev"
var Commit = "unknown"

// A plain go build sets no -ldflags, so name its commit from the VCS stamp
// Go embeds, marking uncommitted changes; reports from two local builds then
// still tell them apart.
func init() {
	info, ok := debug.ReadBuildInfo()
	if Commit != "unknown" || !ok {
		return
	}
	revision, modified := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value[:min(7, len(s.Value))]
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return
	}
	Commit = revision
	if modified {
		Commit += "-dirty"
	}
}
