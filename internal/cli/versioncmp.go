package cli

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// semverPrefix matches a leading "vMAJOR.MINOR.PATCH", with git describe's
// optional "-N-gHASH" distance-from-tag suffix captured separately (N
// defaults to 0 when absent — an exact tag, or a release build stamped
// with the tag name directly). Anything else trailing (a "-dirty"/"+dirty"
// suffix) is ignored. Good enough to order two real version strings
// without pulling in a semver dependency (this repo has none).
var semverPrefix = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?`)

// parseSemverPrefix extracts major.minor.patch and, if present, the
// commit-distance-past-that-tag component of a git-describe-shaped
// string. Returns ok=false for anything that doesn't start with a
// "vX.Y.Z" prefix at all — notably a plain VCS revision hash, which is
// what version.String() reports for a build that was never stamped via
// `wmux update`'s -ldflags (see resolveComparableVersion for how that
// case is upgraded into something comparable when possible).
func parseSemverPrefix(s string) (major, minor, patch, distance int, ok bool) {
	m := semverPrefix.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	if m[4] != "" {
		distance, _ = strconv.Atoi(m[4])
	}
	return major, minor, patch, distance, true
}

// compareVersions orders two version strings by (major, minor, patch,
// distance): cmp is negative if newV is older than oldV, zero if equal,
// positive if newV is newer. Distance is git describe's "commits past
// this tag" count, so "v0.4.0" (distance 0) correctly compares as older
// than "v0.4.0-6-gabc123" (distance 6, i.e. 6 commits past that same
// tag) even though their tag component is identical — without it, a
// release fetch could re-tag-match a since-diverged local build as
// "equal" when it's actually a real downgrade. comparable is false if
// either string doesn't start with a "vX.Y.Z" prefix at all — e.g. a
// raw commit hash from an unstamped build — in which case cmp is
// meaningless.
func compareVersions(oldV, newV string) (cmp int, comparable bool) {
	oMaj, oMin, oPatch, oDist, oOk := parseSemverPrefix(oldV)
	nMaj, nMin, nPatch, nDist, nOk := parseSemverPrefix(newV)
	if !oOk || !nOk {
		return 0, false
	}
	for _, d := range [][2]int{{nMaj, oMaj}, {nMin, oMin}, {nPatch, oPatch}, {nDist, oDist}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// resolveComparableVersion upgrades a raw VCS-hash version (from an
// unstamped `go build`, no `wmux update` provenance) into a git-describe
// string, purely so the downgrade guard has something to compare — the
// hash itself is untouched otherwise, this never changes what's actually
// installed. Tries, in order: repoHint (the --repo flag, if given),
// WMUX_REPO, then the current working directory — the same resolution
// wmux update already uses to find a source repo to build from, reused
// here even on the --release path where no repo is otherwise consulted.
// Falls back to returning v unchanged (still unparseable, still caught as
// "not comparable" by the caller) if no candidate is a real repo, or the
// hash isn't found in it (e.g. a build from a different checkout).
func resolveComparableVersion(v, repoHint string) string {
	if _, _, _, _, ok := parseSemverPrefix(v); ok {
		return v // already comparable, e.g. a --release/wmux-update-stamped build
	}
	dirty := strings.HasSuffix(v, "+dirty")
	hash := strings.TrimSuffix(v, "+dirty")
	if hash == "" || hash == "dev" {
		return v // never built inside a git tree at all (go run, -buildvcs=false)
	}

	for _, repo := range candidateRepos(repoHint) {
		out, err := exec.Command("git", "-C", repo, "describe", "--tags", "--always", hash).Output()
		if err != nil {
			continue // not a repo, or hash not found there — try the next candidate
		}
		desc := strings.TrimSpace(string(out))
		if desc == "" {
			continue
		}
		if dirty {
			desc += "-dirty"
		}
		return desc
	}
	return v // no candidate resolved it; caller still treats this as not comparable
}

// candidateRepos lists the places resolveComparableVersion tries, in
// the same priority order resolveRepo uses for the actual build path —
// --repo flag, WMUX_REPO, the stamped defaultRepo — plus the current
// working directory as a last resort (resolveRepo has no equivalent
// fallback, since building demands an explicit, verified repo; this is
// only ever used for a best-effort version comparison, so a looser
// guess is an acceptable trade for possibly catching more cases).
// Empty entries are skipped.
func candidateRepos(repoHint string) []string {
	var candidates []string
	for _, r := range []string{repoHint, os.Getenv("WMUX_REPO"), defaultRepo} {
		if r != "" {
			candidates = append(candidates, r)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	return candidates
}
