//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// implProcessIdentity reads a process's start marker and executable from
// /proc/<pid>/stat and /proc/<pid>/exe. This is the only reliable
// per-process identity source on Unix; systems without a /proc (macOS,
// *BSD) report ok=false, and callers then treat the PID as unverifiable
// rather than guessing.
func implProcessIdentity(pid int) (procIdent, bool) {
	if pid <= 0 {
		return procIdent{}, false
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid))

	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procIdent{}, false
	}
	// comm (field 2) is parenthesized and may contain spaces, so the
	// numeric fields are everything after the last ')'. starttime is the
	// 22nd field overall; skipping pid and comm, that's index 19 of the
	// remaining fields.
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i == -1 || i+1 >= len(s) {
		return procIdent{}, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) <= 19 {
		return procIdent{}, false
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return procIdent{}, false
	}

	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil {
		return procIdent{}, false
	}

	return procIdent{startTime: start, exe: exe}, true
}
