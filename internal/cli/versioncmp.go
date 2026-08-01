package cli

import (
	"regexp"
	"strconv"
)

// semverPrefix matches a leading "vMAJOR.MINOR.PATCH" and ignores
// whatever follows (git describe's "-N-gHASH", a "-dirty"/"+dirty"
// suffix, etc.) — good enough to order two real version tags without
// pulling in a semver dependency (this repo has none).
var semverPrefix = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

// parseSemverPrefix extracts the leading major.minor.patch triple, if the
// string starts with one. Returns ok=false for anything else — notably a
// plain VCS revision hash, which is what version.String() reports for a
// build that was never stamped via `wmux update`'s -ldflags (a plain
// `go build` with no version info to compare against).
func parseSemverPrefix(s string) (major, minor, patch int, ok bool) {
	m := semverPrefix.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch, true
}

// compareVersions orders two version strings by their major.minor.patch
// prefix: cmp is negative if newV is older than oldV, zero if equal, and
// positive if newV is newer. comparable is false if either string doesn't
// start with a "vX.Y.Z" prefix — e.g. a raw commit hash from an unstamped
// build — in which case cmp is meaningless and the caller has no reliable
// way to tell whether newV is actually older or newer.
func compareVersions(oldV, newV string) (cmp int, comparable bool) {
	oMaj, oMin, oPatch, oOk := parseSemverPrefix(oldV)
	nMaj, nMin, nPatch, nOk := parseSemverPrefix(newV)
	if !oOk || !nOk {
		return 0, false
	}
	for _, d := range [][2]int{{nMaj, oMaj}, {nMin, oMin}, {nPatch, oPatch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}
