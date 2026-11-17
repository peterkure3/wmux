# Development and Regression Testing

Verified on 2026-11-02: the complete Windows coverage suite and Linux/WSL
race suite passed, including the binary lifecycle test. Static checks and
module verification passed. Vulnerability scanning on both platforms reported
zero called-symbol vulnerabilities; it still lists advisories in imported
code that the application does not appear to call. Thirty Windows token
stress runs (eight simultaneous processes each) passed after serializing
provisioning reads and writes. Full-screen visual QA remains manual.

Use Go 1.27.3, selected by `go.mod` and CI. The minimum supported Go
release is 1.26.10. To explicitly select the preferred compiler when a
different newer toolchain is installed, set `GOTOOLCHAIN=go1.27.3` for
the command.

Run from the repository root:

```sh
go mod verify
go vet ./...
go test -count=1 -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

On PowerShell, quote `'-coverprofile=coverage.out'` to preserve the argument.
Run `go test -race -count=1 ./...` on Linux, or Windows with a compatible
C compiler. Both CI platforms run the binary integration test automatically;
Linux also runs the race detector. Coverage reports are generated in CI.

`go test -count=1 -v ./integration` builds both binaries into a temporary
directory and verifies authentication, browser rejection, startup failure,
native PTY creation/input/replay/resize, large notifications, CLI listing,
close/prune, and shutdown. It uses a separate loopback port, token, state,
and log file, and cleans up its child processes. Go must be on PATH.
Use `go test -short ./...` to skip this binary test during quick iterations.

Focused regression suites cover:

- `internal/daemon`: filesystem fault injection preserves the previous
  snapshot; validation, missing-resource, conflict, and unexpected errors
  map to 400, 404, 409, and 500 respectively.
- `internal/cli`: large escaped events, stream errors, process exit codes,
  authorization failures, and disconnected watchers.
- `internal/authtoken`: first creation, corrupt-token repair, preservation
  of existing tokens, and concurrent provisioning across processes.
- `internal/wmuxlog`: rotation during writes and concurrent record integrity.

`wmux watch` accepts encoded event lines up to 8 MiB, checks HTTP status,
and exits with code 3 plus a diagnostic on read failure or disconnect.
Restart it to reconnect. It does not silently retry: the daemon has no
notification replay protocol, so events during a disconnect are not recovered.

For manual terminal QA, run an isolated daemon with `-addr` and `-state`,
and set matching `WMUX_ADDR` and `WMUX_TOKEN_FILE` for the CLI.
Set `WMUX_LOG_FILE` for both processes to isolate log inspection as well.
Verify COMMAND-mode splits, focus changes, keyboard input, mouse focus,
window resizing, and detach/reattach on each supported terminal. Automated
PTY tests do not replace visual checks in Windows Terminal or Linux terminals.
