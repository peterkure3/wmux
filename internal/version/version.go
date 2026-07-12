// Package version holds the build version shared by wmux and wmuxd.
package version

import "runtime/debug"

// Version is stamped by `wmux update` via
//
//	-ldflags "-X github.com/peterkure/wmux/internal/version.Version=<git describe>"
//
// (the -X path follows go.mod's module name, not the GitHub repo URL —
// note the repo lives at github.com/peterkure3/wmux, with a 3).
var Version = "dev"

// String returns the stamped Version, falling back to the VCS revision Go
// embeds automatically when built inside a git work tree. Only `go run` and
// -buildvcs=false builds end up reporting plain "dev".
func String() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return Version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}
