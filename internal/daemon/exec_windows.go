//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// Raw Win32 constants not exposed by the syscall package; raw rather than
// pulling in golang.org/x/sys for a couple of values (the repo has no
// external deps).
const (
	createNoWindow                 = 0x08000000
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259 // GetExitCodeProcess: process has not exited
)

// daemonHasConsole is whether this process is attached to a console.
// Checked once at startup: a foreground wmuxd (run by hand in a terminal)
// has one, a detached wmuxd (restarted by `wmux update`) does not.
var daemonHasConsole = func() bool {
	h, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	return h != 0
}()

// hideConsole keeps a console-subsystem child (git, powershell.exe,
// wsl.exe) from allocating a visible console window. Only needed when
// wmuxd itself has no console: in that state every console child gets a
// brand-new *visible* console by default — with pollMetadata shelling out
// every 3 seconds per running session, that's a console window flashing
// on the user's screen every few seconds for as long as the daemon lives.
// When wmuxd runs foreground, children share its console (nothing can
// flash), and leaving the default also preserves console-group Ctrl+C
// taking spawned sessions down with the daemon, as it always has.
// HideWindow rides along for any GUI-subsystem child.
func hideConsole(cmd *exec.Cmd) {
	if daemonHasConsole {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// processAliveNative probes PID existence with OpenProcess instead of a
// tasklist shell-out: pollMetadata asks this every 3 seconds per session,
// and a transient shell-out failure must never read as "process died"
// (that verdict irreversibly marks a session exited).
func processAliveNative(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// The one error a live process can produce here is access denied
		// (a protected process still *exists*); anything else means no
		// such process.
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // the handle opened, so the process object exists
	}
	return code == stillActive
}
