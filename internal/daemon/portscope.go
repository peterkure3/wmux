package daemon

import (
	"runtime"
	"strconv"
	"strings"
)

// processAlive reports whether a process with the given PID currently
// exists, via a direct OS probe (see exec_windows.go / exec_other.go).
// Deliberately not a tasklist/ps shell-out: pollMetadata asks this every
// 3 seconds per session, and a transient shell-out failure must never
// read as "process died" — that verdict irreversibly marks the session
// exited. Only meaningful for PIDs in the daemon's own namespace; see
// pidVisible in session.go.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return processAliveNative(pid)
}

// processTree returns rootPID plus every descendant PID, in the same
// process namespace as the daemon's own process. Used to scope
// listeningPorts to a session's actual process tree instead of every
// listening port on the machine.
//
// Only meaningful when the daemon and the target PID share a namespace —
// see the comment on listeningPorts for the one case (a Windows-native
// daemon's Spawn-mode session, which is always WSL-targeted) where this
// isn't true and scoping is skipped entirely rather than attempted with a
// PID that means nothing on the other side of the WSL boundary.
func processTree(rootPID int) map[int]bool {
	if runtime.GOOS == "windows" {
		return processTreeWindows(rootPID)
	}
	return processTreeUnix(rootPID)
}

func processTreeWindows(rootPID int) map[int]bool {
	// CSV of every process's own PID and its parent's PID; NoTypeInformation
	// drops the PS type-name header line CSV export otherwise adds.
	out, err := hiddenCommand("powershell.exe", "-NoProfile", "-Command",
		"Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId | ConvertTo-Csv -NoTypeInformation").Output()
	if err != nil {
		return map[int]bool{rootPID: true}
	}
	return buildTree(rootPID, parseCSVPidPairs(string(out)))
}

// parseCSVPidPairs parses PowerShell's ConvertTo-Csv output for two int
// columns (pid, parent pid), skipping the header line and any row that
// doesn't parse cleanly.
func parseCSVPidPairs(csv string) map[int]int {
	parents := make(map[int]int) // pid -> parent pid
	lines := strings.Split(csv, "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header: "ProcessId","ParentProcessId"
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(strings.Trim(fields[0], `"`))
		ppid, err2 := strconv.Atoi(strings.Trim(fields[1], `"`))
		if err1 != nil || err2 != nil {
			continue
		}
		parents[pid] = ppid
	}
	return parents
}

func processTreeUnix(rootPID int) map[int]bool {
	out, err := hiddenCommand("ps", "-eo", "pid,ppid", "--no-headers").Output()
	if err != nil {
		return map[int]bool{rootPID: true}
	}
	parents := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		parents[pid] = ppid
	}
	return buildTree(rootPID, parents)
}

// buildTree walks a pid->parent-pid map to find rootPID and every
// (transitive) child of it.
func buildTree(rootPID int, parents map[int]int) map[int]bool {
	children := make(map[int][]int) // parent pid -> child pids
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}

	tree := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if !tree[c] {
				tree[c] = true
				queue = append(queue, c)
			}
		}
	}
	return tree
}

// listeningPortsForTree returns the local listening ports owned by any
// PID in tree, using a platform-appropriate port->owning-pid source.
func listeningPortsForTree(tree map[int]bool) []int {
	if runtime.GOOS == "windows" {
		return listeningPortsWindows(tree)
	}
	return listeningPortsUnixByPID(tree)
}

func listeningPortsWindows(tree map[int]bool) []int {
	out, err := hiddenCommand("powershell.exe", "-NoProfile", "-Command",
		"Get-NetTCPConnection -State Listen | Select-Object LocalPort,OwningProcess | ConvertTo-Csv -NoTypeInformation").Output()
	if err != nil {
		return nil
	}
	var ports []int
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		port, err1 := strconv.Atoi(strings.Trim(fields[0], `"`))
		pid, err2 := strconv.Atoi(strings.Trim(fields[1], `"`))
		if err1 != nil || err2 != nil || !tree[pid] {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}

// listeningPortsUnixByPID parses `ss -ltnp` (the -p variant, which adds a
// users:(("name",pid=NNN,fd=N)) column) and keeps only ports owned by a
// PID in tree. Works without root for the invoking user's own sockets,
// which is the only case that matters here (wmuxd and the session it's
// scoping always run as the same user).
func listeningPortsUnixByPID(tree map[int]bool) []int {
	out, err := hiddenCommand("ss", "-ltnp").Output()
	if err != nil {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		idx := strings.LastIndex(fields[3], ":")
		if idx == -1 {
			continue
		}
		port, err := strconv.Atoi(fields[3][idx+1:])
		if err != nil {
			continue
		}

		pidIdx := strings.Index(line, "pid=")
		if pidIdx == -1 {
			continue
		}
		rest := line[pidIdx+4:]
		end := strings.IndexAny(rest, ",)")
		if end == -1 {
			continue
		}
		pid, err := strconv.Atoi(rest[:end])
		if err != nil || !tree[pid] {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}
