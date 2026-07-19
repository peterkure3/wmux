//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32Panes                  = syscall.NewLazyDLL("user32.dll")
	procEnumWindows              = user32Panes.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32Panes.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW           = user32Panes.NewProc("GetWindowTextW")
	procIsWindowVisible          = user32Panes.NewProc("IsWindowVisible")
)

// enumState carries one findWindowForPID call's input/output through the
// EnumWindows callback. The callback itself is created exactly once (see
// enumWindowsCB): syscall.NewCallback allocations are never released and
// Windows caps them process-wide, so a per-call closure would exhaust the
// limit for any long-lived caller — the sidebar probes windows every couple
// of seconds for its whole lifetime.
type enumState struct {
	pid   uint32
	found uintptr
	title string
}

var (
	enumMu      sync.Mutex // one enumeration at a time; guards enumCur
	enumCur     *enumState
	enumCBOnce  sync.Once
	enumCBAddr  uintptr
)

func enumWindowsCB() uintptr {
	enumCBOnce.Do(func() {
		enumCBAddr = syscall.NewCallback(func(h uintptr, _ uintptr) uintptr {
			var winPID uint32
			procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&winPID)))
			if winPID != enumCur.pid {
				return 1 // BOOL TRUE: keep enumerating
			}
			visible, _, _ := procIsWindowVisible.Call(h)
			if visible == 0 {
				return 1 // same pid but not visible (e.g. a hidden conhost); keep looking
			}
			buf := make([]uint16, 512)
			n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			enumCur.found = h
			enumCur.title = syscall.UTF16ToString(buf[:n])
			return 0 // BOOL FALSE: stop enumerating, found it
		})
	})
	return enumCBAddr
}

// wtPanesScript is the UI-Automation query behind wtPanesByTitle: for each
// requested name, find a Windows Terminal window containing a pane
// (TermControl) or tab titled exactly that, and print "name<TAB>0x<hwnd>".
// Same addressing trick as cmdFocus's focusScript, batched: UIA/PowerShell
// startup dominates the cost, so one run answers every session at once.
// %s is a PowerShell array literal of single-quoted names.
const wtPanesScript = `
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$names = @(%s)
$root = [System.Windows.Automation.AutomationElement]::RootElement
$cond = New-Object System.Windows.Automation.PropertyCondition([System.Windows.Automation.AutomationElement]::ClassNameProperty, 'CASCADIA_HOSTING_WINDOW_CLASS')
$wins = $root.FindAll([System.Windows.Automation.TreeScope]::Children, $cond)
foreach ($n in $names) {
  foreach ($w in $wins) {
    $nc = New-Object System.Windows.Automation.PropertyCondition([System.Windows.Automation.AutomationElement]::NameProperty, $n)
    $hits = $w.FindAll([System.Windows.Automation.TreeScope]::Descendants, $nc)
    $found = $false
    foreach ($h in $hits) {
      if ($h.Current.ClassName -eq 'TermControl' -or $h.Current.ControlType -eq [System.Windows.Automation.ControlType]::TabItem) { $found = $true }
    }
    if ($found) {
      Write-Output ("{0}` + "`t" + `0x{1:x}" -f $n, [Int64]$w.Current.NativeWindowHandle)
      break
    }
  }
}
`

// wtPanesByTitle locates Windows Terminal panes by pane/tab title (every
// wmux pane keeps its session ID as its fixed title) and returns
// id -> "0x<hwnd>" of the WT window holding each one found. This exists
// because findWindowForPID can never see a WT-hosted pane: the pane's
// process owns no top-level window — the window belongs to
// WindowsTerminal.exe — so PID enumeration only ever answers for classic
// standalone consoles. Best-effort: any failure returns nil.
func wtPanesByTitle(ids []string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = psQuote(id)
	}
	script := fmt.Sprintf(wtPanesScript, strings.Join(quoted, ","))
	out, err := exec.Command("powershell.exe", "-NoProfile", "-EncodedCommand", psEncodedCommand(script)).Output()
	if err != nil {
		return nil
	}
	found := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		id, hwnd, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && id != "" && strings.HasPrefix(hwnd, "0x") {
			found[id] = hwnd
		}
	}
	return found
}

// findWindowForPID enumerates top-level windows for one owned by pid,
// preferring a visible match — a native agent process normally owns
// exactly one (its console, or the pane hosting it). This is the only
// "does this session actually have a live pane" signal available without
// wt.exe's own (nonexistent) introspection API.
func findWindowForPID(pid int) (hwnd uintptr, title string, ok bool) {
	enumMu.Lock()
	defer enumMu.Unlock()

	st := &enumState{pid: uint32(pid)}
	enumCur = st
	procEnumWindows.Call(enumWindowsCB(), 0)
	enumCur = nil

	if st.found == 0 {
		return 0, "", false
	}
	return st.found, st.title, true
}
