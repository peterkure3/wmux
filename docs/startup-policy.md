# Terminal-only daemon startup

The default installers no longer register a logon service. Run `wmux` in a terminal to start the daemon on demand. The invoking terminal displays `wmux: wmuxd started in background` after readiness succeeds. The daemon runs detached, with output in its background log and no new command window. Subsequent invocations reuse it. Closing the UI leaves sessions running.

The retained Go CLI also supports `wmux daemon start`. Notification hooks, help and version commands do not start a daemon. Explicit `wmux autostart install` and installer opt-in flags remain available for users who choose logon startup.

On this Windows installation, the installed Go launcher was updated without replacing the daemon binary or stopping existing sessions. The `wmux-wmuxd` logon task had `DisallowStartIfOnBatteries` enabled, explaining the AC-power behavior. The task was removed with administrator elevation on 2026-11-17. A subsequent Task Scheduler inventory found zero WMUX tasks. The installed launcher passed isolated cold startup, warm reuse, and authenticated access after CLI exit against its existing daemon binary. No reboot test is claimed.

Rust executables remain a separate implementation milestone; the installed Go launcher preserves existing Go agent-hook compatibility. Rust startup has independent real-process tests on Windows and Linux.