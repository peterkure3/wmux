package cli

import "runtime"

// wmuxdBinaryName is the daemon's own filename for the current
// platform — "wmuxd.exe" on Windows, "wmuxd" everywhere else.
func wmuxdBinaryName() string {
	if runtime.GOOS == "windows" {
		return "wmuxd.exe"
	}
	return "wmuxd"
}

// wmuxBinaryName is this CLI's own filename for the current platform —
// "wmux.exe" on Windows, "wmux" everywhere else.
func wmuxBinaryName() string {
	if runtime.GOOS == "windows" {
		return "wmux.exe"
	}
	return "wmux"
}

// releaseAssetPlatform names the platform component of a published
// release archive (wmux_<tag>_<platform>.zip) — "windows-amd64" or
// "linux-amd64". Kept to amd64 for now, matching what the release
// workflow actually publishes; a future arm64 build would need this to
// consider GOARCH too.
func releaseAssetPlatform() string {
	if runtime.GOOS == "windows" {
		return "windows-amd64"
	}
	return "linux-amd64"
}

// releaseAssetExt is the archive format the release workflow actually
// packages each platform in (see .github/workflows/release.yml) —
// zip for Windows, tar.gz for Linux (tar preserves the executable bit,
// which a zip on a non-Windows build system would need extra tooling to
// set; gzip+tar is the path of least resistance there and is what's
// actually published).
func releaseAssetExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// killCommandName/killCommandArgs/killCommandLabel abstract the one-shot
// force-kill used by stopDaemon's bootstrap fallback (a running wmuxd
// that predates the /shutdown endpoint) across platforms.
func killCommandName() string {
	if runtime.GOOS == "windows" {
		return "taskkill"
	}
	return "pkill"
}

func killCommandArgs(name string) []string {
	if runtime.GOOS == "windows" {
		return []string{"/F", "/IM", name}
	}
	return []string{"-f", name}
}

func killCommandLabel() string {
	if runtime.GOOS == "windows" {
		return "taskkill"
	}
	return "pkill"
}
