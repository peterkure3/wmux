# Terminal-only daemon startup

The installed Windows default is the Rust WMUX 0.3.0 interface. Run wmux in your
terminal; cold startup reports "wmux: wmuxd started in background" there. The
daemon is detached without a new command window. Subsequent invocations reuse
it; detaching the UI leaves its sessions running.

The previous wmux-wmuxd logon task had DisallowStartIfOnBatteries enabled,
matching the reported AC-power behavior. It was removed with administrator
elevation. Final installation checks found zero WMUX scheduled tasks. No reboot
test was performed.

Installers do not register tasks/services or launch the daemon. Help/version,
notification hooks and read-only diagnostics do not implicitly start it.
Explicit terminal commands such as new/attach/daemon start can start it.

The Go pair is preserved in legacy/ with separate original state. Use
wmux legacy list/attach for Go sessions; existing Go notification aliases retain
their protocol. Existing sessions were not terminated or migrated. The Go
compatibility binaries use the security-patched Go 1.27.4 compiler.

The installed Rust build passed an isolated four-PowerShell-pane test with real
input/output, startup notice, warm reuse and daemon survival after CLI exit.
The test daemon was stopped afterward. Windows host-job closure is separately
tested; hosts that prohibit breakaway retain control of their daemon and WMUX
reports that OS policy limitation.
