//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isAccessDenied reports whether a failed command's output indicates a
// Windows privilege check rejected it, as opposed to a genuine usage
// error — the only case worth retrying elevated rather than surfacing
// directly. schtasks (and most console tools) report this in their own
// output text rather than through the process's exit-code machinery, so
// text is what we have to go on.
func isAccessDenied(err error, output string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(output, "Access is denied")
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW. Field order and widths must
// match the Windows SDK struct exactly — this is what ShellExecuteExW
// reads and writes across the syscall boundary.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       uintptr
}

const (
	seeMaskNoCloseProcess = 0x00000040
	swHide                = 0
	errorCancelled        = 1223 // user clicked "No" on the UAC prompt
)

var (
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
	elevatedWaitTimeout = 3 * time.Minute // generous: this is a human clicking "Yes", not a computation
)

// runElevated re-launches this same wmux.exe with UAC consent (the "runas"
// verb) running the hidden __elevated-schtasks subcommand, which performs
// the actual schtasks call and reports back its exit code and combined
// output. A normal os/exec pipe can't be attached across an elevation
// boundary, so the child writes its output to a temp file that this
// process reads back once the child exits.
func runElevated(hiddenSubcommand string, realArgs ...string) (output string, exitCode int, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", -1, fmt.Errorf("could not resolve wmux's own path: %w", err)
	}

	outFile, err := os.CreateTemp("", "wmux-elevated-*.txt")
	if err != nil {
		return "", -1, fmt.Errorf("could not create temp file for elevated output: %w", err)
	}
	outPath := outFile.Name()
	outFile.Close()
	defer os.Remove(outPath)

	parts := append([]string{hiddenSubcommand, outPath}, realArgs...)
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = syscall.EscapeArg(p)
	}
	params := strings.Join(escaped, " ")

	verbPtr, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return "", -1, err
	}
	filePtr, err := syscall.UTF16PtrFromString(self)
	if err != nil {
		return "", -1, err
	}
	paramsPtr, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return "", -1, err
	}

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verbPtr,
		lpFile:       filePtr,
		lpParameters: paramsPtr,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, _ := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if err := windows.GetLastError(); err == syscall.Errno(errorCancelled) {
			return "", -1, fmt.Errorf("elevation was cancelled")
		}
		return "", -1, fmt.Errorf("ShellExecuteExW: %v", windows.GetLastError())
	}
	if info.hProcess == 0 {
		return "", -1, fmt.Errorf("ShellExecuteExW reported success but returned no process handle")
	}
	h := windows.Handle(info.hProcess)
	defer windows.CloseHandle(h)

	waitResult, err := windows.WaitForSingleObject(h, uint32(elevatedWaitTimeout.Milliseconds()))
	if err != nil {
		return "", -1, fmt.Errorf("WaitForSingleObject: %w", err)
	}
	if waitResult == uint32(windows.WAIT_TIMEOUT) {
		return "", -1, fmt.Errorf("elevated process did not finish within %s", elevatedWaitTimeout)
	}

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return "", -1, fmt.Errorf("GetExitCodeProcess: %w", err)
	}

	raw, _ := os.ReadFile(outPath)
	return string(raw), int(code), nil
}

// cmdElevatedSchtasks is the hidden subcommand runElevated launches inside
// the elevated child: run schtasks with the given args, write its combined
// output to the temp file the parent is waiting to read, and exit with
// schtasks' own exit code. Never invoked directly by a user — only via
// runElevated, which controls every argument.
func cmdElevatedSchtasks(args []string) {
	if len(args) < 1 {
		os.Exit(2)
	}
	outPath, schtasksArgs := args[0], args[1:]
	out, err := exec.Command("schtasks", schtasksArgs...).CombinedOutput()
	_ = os.WriteFile(outPath, out, 0o600)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
}
