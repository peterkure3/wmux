//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32SK             = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole      = kernel32SK.NewProc("AttachConsole")
	procFreeConsole        = kernel32SK.NewProc("FreeConsole")
	procCreateFileW        = kernel32SK.NewProc("CreateFileW")
	procWriteConsoleInputW = kernel32SK.NewProc("WriteConsoleInputW")
)

const (
	genericRead      = 0x80000000
	genericWrite     = 0x40000000
	fileShareRead    = 0x00000001
	fileShareWrite   = 0x00000002
	openExisting     = 3
	invalidHandleVal = ^uintptr(0)
)

// Virtual-key codes for the named keys sendKeysToPID understands. VK_A..Z
// conveniently equal their uppercase ASCII codes in Win32, used below for
// Ctrl+<letter>.
const (
	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0D
	vkEscape = 0x1B
	vkSpace  = 0x20
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
)

const (
	keyEventType    = 1      // INPUT_RECORD.EventType for a keyboard event
	leftCtrlPressed = 0x0008 // KEY_EVENT_RECORD.dwControlKeyState
)

// keyEventRecord mirrors Win32's KEY_EVENT_RECORD byte-for-byte:
// bKeyDown(4)+wRepeatCount(2)+wVirtualKeyCode(2)+wVirtualScanCode(2)+
// UnicodeChar(2)+dwControlKeyState(4) = 16 bytes.
type keyEventRecord struct {
	bKeyDown          int32
	wRepeatCount      uint16
	wVirtualKeyCode   uint16
	wVirtualScanCode  uint16
	unicodeChar       uint16
	dwControlKeyState uint32
}

// inputRecord mirrors Win32's INPUT_RECORD sized for the KeyEvent member of
// its union: EventType(2)+pad(2)+KeyEvent(16) = 20 bytes total, matching
// sizeof(INPUT_RECORD).
type inputRecord struct {
	eventType uint16
	_         uint16
	keyEvent  keyEventRecord
}

func keyPair(vk uint16, ch uint16, ctrlState uint32) []inputRecord {
	down := inputRecord{eventType: keyEventType, keyEvent: keyEventRecord{
		bKeyDown: 1, wRepeatCount: 1, wVirtualKeyCode: vk, unicodeChar: ch, dwControlKeyState: ctrlState,
	}}
	up := down
	up.keyEvent.bKeyDown = 0
	return []inputRecord{down, up}
}

// namedKeys covers the common non-printable keys — enough for the actual
// use case (dismiss a prompt, confirm, cancel), not a full terminfo-style
// keymap. Anything not matched here or as "Ctrl <letter>" is sent as
// literal Unicode text instead (see sendKeysToPID).
var namedKeys = map[string]uint16{
	"enter": vkReturn, "return": vkReturn,
	"tab": vkTab, "backspace": vkBack,
	"esc": vkEscape, "escape": vkEscape,
	"space": vkSpace,
	"up":    vkUp, "down": vkDown, "left": vkLeft, "right": vkRight,
}

// parseKeyToken turns one CLI token (e.g. "Enter", "Ctrl c", or plain
// literal text like "yes") into the INPUT_RECORD pairs to send for it.
func parseKeyToken(tok string) []inputRecord {
	if vk, ok := namedKeys[strings.ToLower(tok)]; ok {
		ch := uint16(0)
		switch vk {
		case vkReturn:
			ch = '\r'
		case vkTab:
			ch = '\t'
		case vkBack:
			ch = 0x08
		case vkEscape:
			ch = 0x1B
		case vkSpace:
			ch = ' '
		}
		return keyPair(vk, ch, 0)
	}

	parts := strings.Fields(tok)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Ctrl") && len(parts[1]) == 1 {
		letter := strings.ToUpper(parts[1])[0]
		if letter >= 'A' && letter <= 'Z' {
			return keyPair(uint16(letter), uint16(letter-'A'+1), leftCtrlPressed)
		}
	}

	// Not a recognized special key — send it as literal Unicode text, one
	// key event pair per rune, so a plain string like "yes" types "yes".
	var records []inputRecord
	for _, r := range tok {
		records = append(records, keyPair(0, uint16(r), 0)...)
	}
	return records
}

// sendKeysToPID injects synthetic key events directly into pid's own
// console input buffer via WriteConsoleInputW — not UI Automation, not
// SendInput/simulated OS-wide keystrokes (which would steal focus from
// whatever window is actually in front); this writes straight into the
// target console's own input queue regardless of what's focused.
func sendKeysToPID(pid int, keys []string) error {
	// Detach from whatever console this CLI invocation inherited — a
	// process can only be attached to one console at a time.
	procFreeConsole.Call()
	ok, _, err := procAttachConsole.Call(uintptr(pid))
	if ok == 0 {
		return fmt.Errorf("AttachConsole(%d): %v (is the session's process still alive and console-owning?)", pid, err)
	}
	defer procFreeConsole.Call()

	// GetStdHandle(STD_INPUT_HANDLE) is unreliable here — this process's
	// own stdio may already be redirected/closed (any wmux CLI invocation
	// with piped/absent stdin), and AttachConsole doesn't retroactively
	// fix that up. Opening "CONIN$" directly gets a real handle onto the
	// just-attached console's input buffer regardless of this process's
	// own stdio state.
	conin, _ := syscall.UTF16PtrFromString("CONIN$")
	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(conin)),
		uintptr(genericRead|genericWrite),
		uintptr(fileShareRead|fileShareWrite),
		0, uintptr(openExisting), 0, 0,
	)
	if handle == 0 || handle == invalidHandleVal {
		return fmt.Errorf(`CreateFileW("CONIN$") on attached console: %v`, err)
	}

	var records []inputRecord
	for _, tok := range keys {
		records = append(records, parseKeyToken(tok)...)
	}
	if len(records) == 0 {
		return nil
	}

	var written uint32
	ret, _, err := procWriteConsoleInputW.Call(
		handle,
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(len(records)),
		uintptr(unsafe.Pointer(&written)),
	)
	if ret == 0 {
		return fmt.Errorf("WriteConsoleInputW: %v", err)
	}
	if int(written) != len(records) {
		return fmt.Errorf("WriteConsoleInputW: wrote %d/%d events", written, len(records))
	}
	return nil
}
