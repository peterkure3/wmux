// The command tree, on kong.
//
// kong owns the *shape* of the CLI — namespace grouping, legacy flat
// names kept as hidden aliases, and usage-error handling — while every
// leaf hands its raw, unparsed arguments straight to the cmdXxx(args
// []string) function that owns that command's own flags. root.go's Main
// drives it.
//
// Why passthrough rather than real per-command kong flag structs: kong's
// own --help flag, once declared anywhere in the tree, intercepts a
// literal "--help" token even under a passthrough leaf — there is no way
// to have both kong-rendered flag help *and* a leaf that transparently
// forwards "--help" to its own flag.FlagSet's existing, more detailed
// help output. Converting all 30 commands' flags into kong struct tags
// (gaining kong's real per-flag validation and generated help, losing
// nothing) is real, separable follow-up work — deliberately not done
// here, so kong.NoDefaultHelp() is set and this file defines no --help
// flag of its own; `wmux <cmd> --help`/`-h` keeps working exactly as
// before, via the untouched cmdXxx bodies.
package cli

// passArgs is embedded in every leaf command below: it captures every
// remaining token — including flags, including "--help" — unparsed, for
// the wrapped cmdXxx function to parse exactly as it always has.
type passArgs struct {
	Args []string `arg:"" optional:"" passthrough:"all"`
}

type newCmd struct{ passArgs }

func (c *newCmd) Run() error { cmdNew(c.Args); return nil }

type attachCmd struct{ passArgs }

func (c *attachCmd) Run() error { cmdAttach(c.Args); return nil }

type sessionCloseCmd struct{ passArgs }

func (c *sessionCloseCmd) Run() error { cmdClose(c.Args); return nil }

type listCmd struct{ passArgs }

func (c *listCmd) Run() error { cmdList(c.Args); return nil }

type pruneCmd struct{ passArgs }

func (c *pruneCmd) Run() error { cmdPrune(c.Args); return nil }

type surfaceNewCmd struct{ passArgs }

func (c *surfaceNewCmd) Run() error { cmdSurface(c.Args); return nil }

type connectCmd struct{ passArgs }

func (c *connectCmd) Run() error { cmdConnect(c.Args); return nil }

type logCmd struct{ passArgs }

func (c *logCmd) Run() error { cmdLog(c.Args); return nil }

type debugCmd struct{ passArgs }

func (c *debugCmd) Run() error { cmdDebug(c.Args); return nil }

type updateCmd struct{ passArgs }

func (c *updateCmd) Run() error { cmdUpdate(c.Args); return nil }

type autostartCmd struct{ passArgs }

func (c *autostartCmd) Run() error { cmdAutostart(c.Args); return nil }

type hookCmd struct{ passArgs }

func (c *hookCmd) Run() error { cmdHook(c.Args); return nil }

type hookClaudeCmd struct{ passArgs }

func (c *hookClaudeCmd) Run() error { runHook("hook-claude", "claude", c.Args); return nil }

type hookCodexCmd struct{ passArgs }

func (c *hookCodexCmd) Run() error { runHook("hook-codex", "codex", c.Args); return nil }

type notifyCmd struct{ passArgs }

func (c *notifyCmd) Run() error { cmdNotify(c.Args); return nil }

type themeCmd struct{ passArgs }

func (c *themeCmd) Run() error { cmdTheme(c.Args); return nil }

type watchCmd struct{ passArgs }

func (c *watchCmd) Run() error { cmdWatch(c.Args); return nil }

type versionCmd struct{ passArgs }

func (c *versionCmd) Run() error { cmdVersion(c.Args); return nil }

type tuiCmd struct{ passArgs }

func (c *tuiCmd) Run() error { cmdTui(c.Args); return nil }

type gridCmd struct{ passArgs }

func (c *gridCmd) Run() error { cmdGrid(c.Args); return nil }

type elevatedSchtasksCmd struct{ passArgs }

func (c *elevatedSchtasksCmd) Run() error { cmdElevatedSchtasks(c.Args); return nil }

// sessionGroup is `wmux session {new,attach,close,list,prune}` — the
// session-lifecycle commands, per the plan's own example grouping.
type sessionGroup struct {
	New    newCmd          `cmd:"" help:"spawn a new headless agent session (no TTY; daemon owns the pipe)"`
	Attach attachCmd       `cmd:"" help:"run a command interactively (real TTY), tracked by the daemon"`
	Close  sessionCloseCmd `cmd:"" help:"kill a session's tracked process"`
	List   listCmd         `cmd:"" help:"list sessions and their state"`
	Prune  pruneCmd        `cmd:"" help:"remove all exited sessions from daemon state"`
}

// daemonGroup is `wmux daemon {log,debug,update,autostart}` — wmuxd
// itself, not any one session.
type daemonGroup struct {
	Log       logCmd       `cmd:"" help:"inspect wmuxd's structured log"`
	Debug     debugCmd     `cmd:"" help:"inspect wmuxd's own runtime state"`
	Update    updateCmd    `cmd:"" help:"self-update wmux + wmuxd"`
	Autostart autostartCmd `cmd:"" help:"register/remove wmuxd as a Task Scheduler logon task"`
}

// cli is the full command tree. Every namespaced leaf above is also kept
// as a hidden flat field below with the exact same underlying type — same
// Go type, a second independent field/instance — so every command anyone
// (or any script, hook, or wt.exe profile) already invokes by its old
// flat name keeps working verbatim.
var cli struct {
	Session sessionGroup `cmd:"" help:"session lifecycle: new, attach, close, list, prune"`
	Daemon  daemonGroup  `cmd:"" help:"wmuxd itself: log, debug, update, autostart"`
	Hook    hookCmd      `cmd:"" help:"agent notification hooks: run <agent>, list"`

	// surface/connect are flat, unchanged — "surface" is itself an
	// existing flat command name (open one surface directly), so a
	// "wmux surface {new,...}" group would collide with the leaf command
	// already there.
	Surface surfaceNewCmd `cmd:"" help:"spawn CMD in a daemon-owned ConPTY (tmux-style, survives terminal close)"`
	Connect connectCmd    `cmd:"" help:"attach this terminal to a surface"`

	Notify  notifyCmd  `cmd:"" help:"manually push a notification (testing)"`
	Theme   themeCmd   `cmd:"" help:"print or persist the active sidebar theme"`
	Watch   watchCmd   `cmd:"" help:"stream notifications as they arrive"`
	TUI     tuiCmd     `cmd:"" name:"tui" help:"full-screen multi-pane multiplexer (what plain 'wmux' opens)"`
	Grid    gridCmd    `cmd:"" help:"open the multiplexer with N panes in a grid, e.g. 'wmux grid 4 --claude'"`
	Version versionCmd `cmd:"" help:"print the wmux version"`

	HookClaude hookClaudeCmd `cmd:"" name:"hook-claude" help:"alias for 'wmux hook run claude' (reads stdin JSON)"`
	HookCodex  hookCodexCmd  `cmd:"" name:"hook-codex" help:"alias for 'wmux hook run codex' (JSON as final arg)"`

	// Flat legacy aliases for everything namespaced above (session,
	// daemon) — hidden from --help (the namespaced form is what's
	// documented), functionally identical.
	New           newCmd              `cmd:"" hidden:""`
	Attach        attachCmd           `cmd:"" hidden:""`
	FlatClose     sessionCloseCmd     `cmd:"" name:"close" hidden:""`
	FlatList      listCmd             `cmd:"" name:"list" hidden:""`
	Prune         pruneCmd            `cmd:"" hidden:""`
	FlatLog       logCmd              `cmd:"" name:"log" hidden:""`
	FlatDebug     debugCmd            `cmd:"" name:"debug" hidden:""`
	FlatUpdate    updateCmd           `cmd:"" name:"update" hidden:""`
	FlatAutostart autostartCmd        `cmd:"" name:"autostart" hidden:""`
	Elevated      elevatedSchtasksCmd `cmd:"" name:"__elevated-schtasks" hidden:""`
}
