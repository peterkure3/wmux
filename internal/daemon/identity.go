package daemon

import (
	"runtime"
	"strings"
)

// procIdent identifies a specific process instance, not just a PID.
// Operating systems reuse PIDs, so "PID N was running when wmuxd last
// saw it" is not "PID N is still the same process" — the OS can have
// recycled the number for an unrelated program. Two values that match on
// both the creation marker and the executable path are, in practice, the
// same process; that is the identity a persisted session is judged by
// after a daemon restart (see persist.go load() and Session.Close()).
type procIdent struct {
	// startTime is a platform-specific creation marker: the process
	// creation FILETIME on Windows, the /proc/<pid>/stat starttime field
	// (clock ticks since boot) on Unix. Comparable only within one host,
	// which is exactly the only place it is ever compared.
	startTime int64
	// exe is the absolute path of the process's executable image.
	exe string
}

// processIdentity queries a live process's creation marker and executable
// path. It returns ok=false when the PID is invisible to this process
// (a WSL-resident PID from a Windows daemon) or the platform exposes no
// such information (a Unix without /proc). Callers must treat an
// unverifiable identity as "not the recorded process", never as "safe to
// kill by PID". The platform-specific implementation lives in
// identity_windows.go / identity_other.go.
var processIdentity = implProcessIdentity

// sameProc reports whether a and b describe the same process instance.
// A zero identity on either side never matches: missing startTime or exe
// means the identity cannot be confirmed, and confirmation — not hope — is
// what the caller needs before acting on a PID that outlived a restart.
func sameProc(a, b procIdent) bool {
	if a.startTime == 0 || b.startTime == 0 || a.exe == "" || b.exe == "" {
		return false
	}
	if a.startTime != b.startTime {
		return false
	}
	// Windows paths are case-insensitive; Unix paths are not.
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a.exe, b.exe)
	}
	return a.exe == b.exe
}
