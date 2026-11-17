# CLI reference

Run wmux --help and wmux <command> --help for exact options. Global --home selects
the Rust state directory; --json provides stable structured state for list,
pane list, task list and approvals.

```text
wmux [new NAME --cwd PATH --detach -- EXECUTABLE ARGS...]
wmux attach [WORKSPACE] [--read-only]
wmux list | status | doctor | config
wmux kill-session WORKSPACE [--force]
wmux daemon start | stop --force
wmux pane list [-w WORKSPACE]
wmux pane split-horizontal|split-vertical [-w WORKSPACE] [--pane ID] [-- COMMAND...]
wmux pane close [-w WORKSPACE] [--pane ID] --force
wmux pane zoom|layout [-w WORKSPACE]
wmux pane focus PANE [-w WORKSPACE]
wmux pane resize --pane ID COLS ROWS
wmux pane adjust DELTA [-w WORKSPACE] [--pane ID]
wmux pane rename TITLE [-w WORKSPACE] [--pane ID]
wmux pane swap FIRST SECOND [-w WORKSPACE]
wmux pane send TEXT [-w WORKSPACE] [--pane ID] [--enter]
wmux agent list | spawn NAME [-w WORKSPACE] | stop PANE --force
wmux agent event STATE [--pane ID] [--message TEXT]
wmux task list | create TITLE [-w WORKSPACE] [--description TEXT]
wmux task assign TASK PANE
wmux task status TASK open|running|completed|failed|cancelled
wmux approvals
wmux approval request TITLE [--pane ID] [--details TEXT] [--destructive] [--ttl SECONDS]
wmux approval inspect|approve|reject ID
wmux git [-w WORKSPACE] status|diff
wmux git [-w WORKSPACE] worktree-add PATH BRANCH
wmux git [-w WORKSPACE] worktree-remove PATH --force
wmux capture -w WORKSPACE --output PATH [--width 120 --height 40]
```

Workspace selectors accept IDs or names. Pane selectors use IDs. Inside a pane,
WMUX_PANE_ID supplies its context; otherwise -w is required if more than one
workspace exists. Destructive commands prompt only on an interactive stdin;
scripts must use --force. Git cleanup never passes Git's --force, even when
WMUX's confirmation is bypassed.

New/attach use the full-screen UI unless new --detach is supplied. wmux detach
prints how to detach the current UI; a separate process cannot detach another
terminal client. Output from capture is text from the same Ratatui rendering
path and live daemon cells; it is not advertised as a screenshot.

Errors print to stderr and exit nonzero. Approval request exits zero only for
an explicit approved decision, and never executes the operation itself.

Additional commands: `wmux agent hook claude|codex` consumes official JSON on stdin; `wmux git diff --staged`, `wmux git files`, `wmux git preview RELATIVE_PATH` inspect repository files. `wmux legacy ARGS...` runs the preserved Go interface. Writable UI connections own workspace input/layout leases until detach; concurrent CLI writes receive a clear error.
