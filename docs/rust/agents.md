# Agents and approvals

WMUX runs ordinary shells without installed agents. Command adapters detect Codex,
Claude Code, Gemini and Aider; configure custom command arrays under [agents].
OS process status supplies exit codes. Terminal prose never marks a task complete.

## Official command hooks (opt-in)

Run `wmux agent hook claude` or `wmux agent hook codex` from a supported
provider command hook. JSON arrives on stdin; WMUX_HOME and WMUX_PANE_ID identify
the managed pane. Without a pane environment the hook is a no-op. Hooks never
start a daemon. Templates are in integrations/rust/.

Supported lifecycle events update activity separately from process health.
Stop means a turn ended and the agent is Idle; it does not complete a task.
PermissionRequest creates a persisted approval with the tool input as context,
waits for the user's decision, and emits provider JSON. Rejection, expiry or
daemon/pane unavailability emits deny. Special interactive dialogs requiring
rewritten input remain in the provider's native prompt. No persistent permission
rules or tool-input rewrites are emitted.

All provider permission requests use the conservative confirmation policy.
Inspect the request before approving. Named JSON token/password/secret fields
are redacted; arbitrary secrets embedded in free-form commands cannot always be
recognized. No transcripts, environment dumps or raw hook payloads are logged.
Set the provider timeout above WMUX's TTL (300 seconds by default).

Contract references, checked 2026-11-17:
[Claude hooks](https://code.claude.com/docs/en/hooks),
[Codex hooks](https://developers.openai.com/codex/hooks).
Real CLI/daemon contract tests exercise approval and rejection without paid
provider calls. Full live provider/version validation remains a release gate.
Do not overwrite existing hook files: merge selected entries after review.

## Other integrations

Custom wrappers can use `wmux agent event idle|input|approval` and
`wmux approval request TITLE --details TEXT`. The latter returns success only
after approval and executes nothing. Approve/reject in Ctrl+B, A or through CLI.
Requests expire on TTL, pane exit or daemon restart.

Gemini and Aider launch as ordinary CLI processes; their permission mechanisms
are not bridged. Windows npm shims use literal encoded PowerShell; unsafe CMD
metacharacters are rejected. Configure the native executable/node script for
those arguments.

BEL, OSC 9 and OSC 777 notifications are supported. Desktop delivery and OSC 99
remain unsupported. Hooks and PTYs are cooperative interfaces, not OS sandboxes.
Go notification commands are delegated to the preserved legacy/ pair; use
`wmux legacy list` to access its separate sessions and HTTP protocol.
