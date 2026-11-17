# Application Testing Assessment

Implementation follow-up: the priorities below have been addressed in source.
See [development testing](development-testing.md) for the new regression suites,
binary smoke test, patched toolchain, and current watcher behavior. The original
findings and measurements below describe the pre-fix application.

Date: 2026-11-02. Tested the current Windows working tree using Go 1.27.0 (`windows/amd64`). The repository requests Go 1.26.6; Go selected the newer installed toolchain. Application source was not changed. Existing GUI deletions and other user changes were left untouched.

## Results

| Check | Result |
| --- | --- |
| `go test -count=1 '-coverprofile=build/test-coverage.out' ./...` | Passed across all packages with tests |
| `go vet ./...` | Passed |
| Builds of `./cmd/wmux` and `./cmd/wmuxd` | Passed |
| `go mod verify` | All modules verified |
| `go test -race -count=1 ./...` | Blocked: GCC is absent; no race-free claim |
| `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` | Failed: 10 called-symbol standard-library advisories reported |

Unit-test statement coverage is approximately 38% overall: CLI 5.0%, client 50.9%, daemon 53.3%, TUI 66.6%, layout 87.2%, agent profiles 87.5%, and logging 64.4%. Token provisioning has no direct unit tests. These figures exclude the separate binary smoke test.

Live testing used freshly built binaries, `127.0.0.1:47983`, and separate token/state files under ignored `build/`. Health and identification worked; protected requests without a token returned 401; browser-origin requests returned 403; malformed JSON returned 400. CLI version/profile listing worked. Native ConPTY creation, duplicate-session rejection, CLI session listing, resizing, ANSI replay attachment at the new dimensions, notification delivery, closing, and pruning worked. No recovered daemon panics were reported. Test processes were stopped afterward.

## Prioritized Improvements

### 1. High: update build toolchains and rebuild releases

The scanner reported GO-2026-6809, GO-2026-6617, GO-2026-6613, GO-2026-6612, GO-2026-6611, GO-2026-6610, GO-2026-6608, GO-2026-6607, GO-2026-6605, and GO-2026-6603 against installed Go 1.27.0. The reported fixed versions are 1.27.2 or 1.27.3. These are scanner reachability findings; this assessment did not demonstrate exploitation of each advisory.

Update the development toolchain and `go.mod`/CI pins together, then rerun scanning and rebuild distributed binaries. Go 1.27.3 or 1.26.10 includes the newer security patches described in the [official release history](https://go.dev/doc/devel/release). The [GO-2026-6809 advisory](https://pkg.go.dev/vuln/GO-2026-6809) explicitly affects both the installed 1.27.0 and repository-pinned 1.26.6 versions. Existing binaries were not scanned individually.

### 2. High: propagate persistence write errors

Source review: `internal/daemon/persist.go:100` declares an inner `err` for `tmp.Chmod`; `tmp.Write` assigns to that inner variable. Both errors disappear when the block ends. The later sync/rename path can therefore proceed after a failed or partial write, replacing a good snapshot with incomplete data.

Use the outer error variable consistently and abort before rename on chmod/write failure. Add fault-injection coverage that verifies the previous snapshot survives those failures. Normal persistence tests passed; filesystem failure conditions were not reproduced live.

### 3. Medium: prevent silent notification-watcher exits

Reproduced twice: start `wmux watch`, deliver a short notification successfully, then POST a notification with a 70,000-character body. The daemon accepts it with 202, but the watcher exits with no stderr message.

`internal/cli/notify.go:67` uses the default scanner limit (about 64 KiB) and never checks `scanner.Err()`. Align the scanner limit with the maximum encoded event size, check HTTP status and scanner errors, and define reconnect behavior. Test normal messages, oversized messages, and interrupted streams. Account for JSON escaping and envelope overhead when choosing compatible limits.

### 4. Medium: classify API failures accurately

Reproduced: resizing an existing surface to `99999x30` returns 404; creating a surface with missing fields returns 409. These are validation errors. `internal/daemon/server.go` maps broad groups of errors to single status codes.

Introduce typed validation/not-found/conflict errors: return 400 for invalid fields, 404 for absent sessions, and 409 for duplicate active IDs. Assert those distinctions in handler tests.

### 5. Medium: extend CLI and operational coverage

Prioritize subprocess tests for command exit codes, watcher failures, daemon startup failures, and token creation/corruption/concurrent provisioning. Promote the isolated smoke flow into CI with assertions and cleanup. Add terminal-driven checks for pane creation, keyboard routing, resize, and reconnect, plus native Linux/WSL execution.

Logging also deserves a long-running test: `internal/wmuxlog/wmuxlog.go:119` rotates only during initialization, so the 10 MiB threshold does not bound log growth while a daemon remains running. Consider rotation during writes.

## Reproduction Artifacts and Limits

- `build/smoke-test.ps1`: isolated Windows smoke/reproduction script; emits observations rather than asserting every result.
- `build/smoke-results.json`: compact results, including the watcher failure and invalid-resize status.
- `build/govulncheck.txt`: full scanner output.
- `build/test-coverage.out`: unit-test coverage profile.

Full-screen visual/keyboard usability, Linux/WSL runtime behavior, installers, updates, real agent integrations, and the absent GUI were not exercised. Fallow was unavailable locally, so no repository-wide Fallow metrics are claimed. Initial PowerShell/cache failures were resolved before the successful test run above.
