mod approval;
mod background;
mod compat;
mod hooks;
use anyhow::{Context, Result, bail, ensure};
use clap::{Parser, Subcommand};
use std::{
    collections::BTreeMap,
    io::{IsTerminal, Write},
    path::{Path, PathBuf},
    time::Duration,
};
use wmux_core::{
    AgentActivity,
    config::{Config, home},
    layout::Axis,
};
use wmux_ipc::{Reply, Request, Snapshot};

#[derive(Parser)]
#[command(
    name = "wmux",
    version,
    about = "Native PTY multiplexer — Rust implementation"
)]
struct Cli {
    #[arg(
        long,
        global = true,
        help = "Select an isolated state/config directory"
    )]
    home: Option<PathBuf>,
    #[arg(long, global = true)]
    json: bool,
    #[command(subcommand)]
    command: Option<Command>,
}
#[derive(Subcommand)]
enum Command {
    New {
        name: String,
        #[arg(long, default_value = ".")]
        cwd: String,
        #[arg(long)]
        detach: bool,
        #[arg(last = true)]
        command: Vec<String>,
    },
    Attach {
        workspace: Option<String>,
        #[arg(long)]
        read_only: bool,
    },
    Detach,
    List,
    KillSession {
        workspace: String,
        #[arg(long)]
        force: bool,
    },
    Pane {
        #[command(subcommand)]
        command: PaneCommand,
    },
    Agent {
        #[command(subcommand)]
        command: AgentCommand,
    },
    Task {
        #[command(subcommand)]
        command: TaskCommand,
    },
    Approvals,
    Approval {
        #[command(subcommand)]
        command: approval::Command,
    },
    Status,
    Doctor,
    Config {
        #[arg(long)]
        print_default: bool,
        #[arg(long)]
        check: bool,
    },
    Daemon {
        #[command(subcommand)]
        command: DaemonCommand,
    },
    /// Run preserved Go commands and access Go sessions (separate daemon/state).
    Legacy {
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<std::ffi::OsString>,
    },
    Git {
        #[arg(short, long)]
        workspace: Option<String>,
        #[command(subcommand)]
        command: GitCommand,
    },
    /// Capture the real daemon screen cells through the same Ratatui view.
    Capture {
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long, default_value_t = 120)]
        width: u16,
        #[arg(long, default_value_t = 40)]
        height: u16,
        #[arg(short, long)]
        output: PathBuf,
    },
}
#[derive(Subcommand)]
enum DaemonCommand {
    Start,
    Stop {
        #[arg(long)]
        force: bool,
    },
}
#[derive(Subcommand)]
enum PaneCommand {
    List {
        #[arg(short, long)]
        workspace: Option<String>,
    },
    SplitHorizontal {
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        pane: Option<String>,
        #[arg(last = true)]
        command: Vec<String>,
    },
    SplitVertical {
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        pane: Option<String>,
        #[arg(last = true)]
        command: Vec<String>,
    },
    Close {
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        pane: Option<String>,
        #[arg(long)]
        force: bool,
    },
    Zoom {
        #[arg(short, long)]
        workspace: Option<String>,
    },
    Focus {
        pane: String,
        #[arg(short, long)]
        workspace: Option<String>,
    },
    Resize {
        #[arg(long)]
        pane: String,
        cols: u16,
        rows: u16,
    },
    Adjust {
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        pane: Option<String>,
        delta: i16,
    },
    Rename {
        title: String,
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        pane: Option<String>,
    },
    Swap {
        first: String,
        second: String,
        #[arg(short, long)]
        workspace: Option<String>,
    },
    Layout {
        name: String,
        #[arg(short, long)]
        workspace: Option<String>,
    },
    Send {
        text: String,
        #[arg(long)]
        pane: Option<String>,
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long)]
        enter: bool,
    },
}
#[derive(Subcommand)]
enum AgentCommand {
    List,
    /// Consume official provider JSON on stdin; never starts a daemon.
    Hook {
        #[arg(value_parser=["claude", "codex"])]
        provider: String,
        #[arg(long, env = "WMUX_PANE_ID")]
        pane: Option<String>,
        #[arg(long, default_value_t = 300)]
        ttl: u64,
    },
    Spawn {
        name: String,
        #[arg(short, long)]
        workspace: Option<String>,
    },
    Stop {
        pane: String,
        #[arg(long)]
        force: bool,
    },
    /// Explicit structured hook event; terminal output is never interpreted as activity.
    Event {
        #[arg(long, env = "WMUX_PANE_ID")]
        pane: String,
        #[arg(value_parser=["unknown","idle","input","approval"])]
        state: String,
        #[arg(long)]
        message: Option<String>,
    },
}
#[derive(Subcommand)]
enum TaskCommand {
    List,
    Create {
        title: String,
        #[arg(short, long)]
        workspace: Option<String>,
        #[arg(long, default_value = "")]
        description: String,
    },
    Assign {
        task: String,
        pane: String,
    },
    Status {
        task: String,
        #[arg(value_parser=["open","running","completed","failed","cancelled"])]
        status: String,
    },
}
#[derive(Subcommand)]
enum GitCommand {
    Status,
    Diff {
        #[arg(long)]
        staged: bool,
    },
    Files,
    Preview {
        path: PathBuf,
    },
    WorktreeAdd {
        path: PathBuf,
        branch: String,
    },
    WorktreeRemove {
        path: PathBuf,
        #[arg(long)]
        force: bool,
    },
}
async fn snapshot(home: &Path) -> Result<Snapshot> {
    match wmux_ipc::call(
        home,
        Request::Get {
            scrollback: BTreeMap::new(),
        },
    )
    .await?
    {
        Reply::Snapshot(s) => Ok(s),
        _ => bail!("invalid daemon state response"),
    }
}
fn workspace(s: &Snapshot, selector: Option<String>) -> Result<String> {
    if let Some(name) = selector {
        return s
            .state
            .workspaces
            .values()
            .find(|w| w.id == name || w.name == name)
            .map(|w| w.id.clone())
            .context("workspace not found");
    }
    if let Ok(pane) = std::env::var("WMUX_PANE_ID")
        && let Some(w) = s
            .state
            .workspaces
            .values()
            .find(|w| w.panes.contains_key(&pane))
    {
        return Ok(w.id.clone());
    }
    ensure!(
        s.state.workspaces.len() == 1,
        "select a workspace with --workspace (or create one with wmux new)"
    );
    s.state
        .workspaces
        .keys()
        .next()
        .cloned()
        .context("no workspace")
}
fn pane(s: &Snapshot, w: &str, selected: Option<String>) -> Result<String> {
    if let Some(id) = selected {
        ensure!(
            s.state.workspaces[w].panes.contains_key(&id),
            "pane not found"
        );
        return Ok(id);
    }
    if let Ok(id) = std::env::var("WMUX_PANE_ID")
        && s.state.workspaces[w].panes.contains_key(&id)
    {
        return Ok(id);
    }
    s.state.workspaces[w]
        .focused
        .clone()
        .context("no focused pane")
}
fn confirm(force: bool, message: &str) -> Result<()> {
    if force {
        return Ok(());
    }
    ensure!(
        std::io::stdin().is_terminal(),
        "{message}; pass --force to confirm in scripts"
    );
    eprint!("{message} [y/N] ");
    std::io::stderr().flush()?;
    let mut answer = String::new();
    std::io::stdin().read_line(&mut answer)?;
    ensure!(answer.trim().eq_ignore_ascii_case("y"), "cancelled");
    Ok(())
}
async fn start(home: &Path) -> Result<()> {
    if matches!(wmux_ipc::call(home, Request::Ping).await, Ok(Reply::Pong)) {
        return Ok(());
    }
    let executable =
        std::env::current_exe()?.with_file_name(if cfg!(windows) { "wmuxd.exe" } else { "wmuxd" });
    ensure!(
        executable.is_file(),
        "wmuxd must be installed alongside wmux"
    );
    wmux_security::private_dir(home)?;
    let logpath = home.join("daemon.log");
    let mut child = background::spawn(&executable, home).context("cannot start daemon")?;
    let deadline = tokio::time::Instant::now() + Duration::from_secs(30);
    while tokio::time::Instant::now() < deadline {
        if matches!(wmux_ipc::call(home, Request::Ping).await, Ok(Reply::Pong)) {
            eprintln!("wmux: wmuxd started in background");
            return Ok(());
        }
        if let Some(status) = child.try_wait()? {
            bail!("daemon exited with {status}; inspect {}", logpath.display());
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    bail!("daemon did not become ready; inspect {}", logpath.display())
}
async fn execute(cli: Cli) -> Result<()> {
    let home = cli.home.unwrap_or_else(home);
    // Resolve early so daemon and clients hash identical paths on Windows.
    let home = if home.is_absolute() {
        home
    } else {
        std::env::current_dir()?.join(home)
    };
    let config = Config::load(&home)?;
    match cli.command {
        Some(Command::Legacy { args }) => {
            let mut forward = vec![std::ffi::OsString::from("legacy")];
            forward.extend(args);
            let code = compat::run(&forward)?.context("legacy command missing")?;
            ensure!(code == 0, "Go compatibility command exited {code}");
        }
        Some(Command::Config {
            print_default,
            check,
        }) => {
            if print_default {
                print!("{}", toml::to_string_pretty(&Config::default())?);
            } else if check {
                println!(
                    "Configuration valid: {}",
                    home.join("config.toml").display()
                );
            } else {
                println!("{}", home.join("config.toml").display());
            }
        }
        Some(Command::Doctor) => doctor(&home, &config).await?,
        Some(Command::Daemon {
            command: DaemonCommand::Start,
        }) => {
            start(&home).await?;
            println!("Daemon ready: {}", wmux_ipc::endpoint(&home)?);
        }
        Some(Command::Daemon {
            command: DaemonCommand::Stop { force },
        }) => {
            confirm(force, "Stop daemon and all pane processes?")?;
            wmux_ipc::call(&home, Request::Shutdown).await?;
        }
        Some(Command::Detach) => println!(
            "Detach in the attached UI with Ctrl+{}, D. Sessions remain in wmuxd.",
            config.prefix.to_ascii_uppercase()
        ),
        None => {
            start(&home).await?;
            let s = snapshot(&home).await?;
            if s.state.workspaces.is_empty() {
                wmux_ipc::call(
                    &home,
                    Request::New {
                        name: config.default_workspace.clone(),
                        cwd: std::env::current_dir()?.to_string_lossy().into(),
                        command: vec![],
                    },
                )
                .await?;
            }
            wmux_tui::run(home, config, None, false).await?;
        }
        Some(Command::New {
            name,
            cwd,
            detach,
            command,
        }) => {
            start(&home).await?;
            let result = wmux_ipc::call(&home, Request::New { name, cwd, command }).await?;
            if let Reply::Created(id) = result {
                if detach {
                    println!("{id}");
                } else {
                    wmux_tui::run(home, config, Some(id), false).await?;
                }
            }
        }
        Some(Command::Attach {
            workspace,
            read_only,
        }) => {
            let s = snapshot(&home).await?;
            if let Some(ref name) = workspace {
                ensure!(
                    s.state
                        .workspaces
                        .values()
                        .any(|w| w.id == *name || w.name == *name),
                    "workspace not found"
                );
            }
            wmux_tui::run(home, config, workspace, read_only).await?;
        }
        Some(Command::List | Command::Status) => {
            let s = snapshot(&home).await?;
            if cli.json {
                println!("{}", serde_json::to_string_pretty(&s.state)?);
            } else {
                for w in s.state.workspaces.values() {
                    println!("{}\t{}\t{} panes\t{}", w.id, w.name, w.panes.len(), w.cwd);
                }
            }
        }
        Some(Command::KillSession { workspace, force }) => {
            confirm(force, "Terminate every pane in this workspace?")?;
            wmux_ipc::call(&home, Request::Kill { workspace }).await?;
        }
        Some(Command::Approval { command }) => approval::run(&home, command).await?,
        Some(Command::Approvals) => {
            let s = snapshot(&home).await?;
            if cli.json {
                println!("{}", serde_json::to_string_pretty(&s.state.approvals)?);
            } else {
                for a in s.state.approvals {
                    println!(
                        "{}\t{:?}\t{}\t{}",
                        a.id,
                        a.status,
                        if a.destructive {
                            "destructive"
                        } else {
                            "review"
                        },
                        a.title
                    );
                }
            }
        }
        Some(Command::Pane { command }) => {
            let s = snapshot(&home).await?;
            let request = match command {
                PaneCommand::List {
                    workspace: selected,
                } => {
                    let w = workspace(&s, selected)?;
                    if cli.json {
                        println!(
                            "{}",
                            serde_json::to_string_pretty(&s.state.workspaces[&w].panes)?
                        );
                    } else {
                        for p in s.state.workspaces[&w].panes.values() {
                            println!("{}\t{}\t{:?}\t{:?}", p.id, p.title, p.state, p.activity);
                        }
                    }
                    return Ok(());
                }
                PaneCommand::SplitHorizontal {
                    workspace: selected,
                    pane,
                    command,
                } => Request::Split {
                    workspace: workspace(&s, selected)?,
                    pane,
                    axis: Axis::Rows,
                    command,
                    agent: None,
                },
                PaneCommand::SplitVertical {
                    workspace: selected,
                    pane,
                    command,
                } => Request::Split {
                    workspace: workspace(&s, selected)?,
                    pane,
                    axis: Axis::Columns,
                    command,
                    agent: None,
                },
                PaneCommand::Close {
                    workspace: selected,
                    pane: selected_pane,
                    force,
                } => {
                    let w = workspace(&s, selected)?;
                    confirm(force, "Terminate this pane process?")?;
                    Request::Close {
                        pane: pane(&s, &w, selected_pane)?,
                        workspace: w,
                    }
                }
                PaneCommand::Zoom {
                    workspace: selected,
                } => Request::Zoom {
                    workspace: workspace(&s, selected)?,
                },
                PaneCommand::Focus {
                    pane,
                    workspace: selected,
                } => Request::Focus {
                    workspace: workspace(&s, selected)?,
                    pane,
                },
                PaneCommand::Resize { pane, cols, rows } => Request::Resize { pane, cols, rows },
                PaneCommand::Adjust {
                    workspace: selected,
                    pane: selected_pane,
                    delta,
                } => {
                    let w = workspace(&s, selected)?;
                    Request::Adjust {
                        pane: pane(&s, &w, selected_pane)?,
                        workspace: w,
                        delta,
                    }
                }
                PaneCommand::Rename {
                    workspace: selected,
                    pane: selected_pane,
                    title,
                } => {
                    let w = workspace(&s, selected)?;
                    Request::Rename {
                        pane: Some(pane(&s, &w, selected_pane)?),
                        workspace: w,
                        title,
                    }
                }
                PaneCommand::Swap {
                    workspace: selected,
                    first,
                    second,
                } => Request::Swap {
                    workspace: workspace(&s, selected)?,
                    first,
                    second,
                },
                PaneCommand::Layout {
                    workspace: selected,
                    name,
                } => Request::Layout {
                    workspace: workspace(&s, selected)?,
                    name,
                },
                PaneCommand::Send {
                    text,
                    pane: selected_pane,
                    workspace: selected,
                    enter,
                } => {
                    let w = workspace(&s, selected)?;
                    let mut data = text.into_bytes();
                    if enter {
                        data.push(b'\r');
                    }
                    Request::Input {
                        pane: pane(&s, &w, selected_pane)?,
                        data,
                    }
                }
            };
            if let Reply::Created(id) = wmux_ipc::call(&home, request).await? {
                println!("{id}");
            }
        }
        Some(Command::Agent { command }) => match command {
            AgentCommand::Hook {
                provider,
                pane,
                ttl,
            } => {
                if let Some(pane) = pane {
                    hooks::run(&home, &provider, pane, ttl).await?;
                }
            }
            AgentCommand::List => {
                use wmux_agents::Adapter;
                for name in wmux_agents::BUILTINS
                    .iter()
                    .copied()
                    .chain(config.agents.keys().map(|s| s.as_str()))
                {
                    let adapter = wmux_agents::CommandAgent(name.into());
                    println!(
                        "{}\t{}",
                        name,
                        if adapter.available(&config) {
                            "installed"
                        } else {
                            "missing"
                        }
                    );
                }
                if let Ok(s) = snapshot(&home).await {
                    for w in s.state.workspaces.values() {
                        for p in w.panes.values().filter(|p| p.agent.is_some()) {
                            println!(
                                "{}\t{}\t{:?}\t{:?}",
                                p.id,
                                p.agent.as_deref().unwrap_or(""),
                                p.state,
                                p.activity
                            );
                        }
                    }
                }
            }
            AgentCommand::Spawn {
                name,
                workspace: selected,
            } => {
                let s = snapshot(&home).await?;
                let w = workspace(&s, selected)?;
                if let Reply::Created(id) = wmux_ipc::call(
                    &home,
                    Request::Split {
                        workspace: w,
                        pane: None,
                        axis: Axis::Columns,
                        command: vec![],
                        agent: Some(name),
                    },
                )
                .await?
                {
                    println!("{id}");
                }
            }
            AgentCommand::Stop { pane, force } => {
                let s = snapshot(&home).await?;
                let w = s
                    .state
                    .workspaces
                    .values()
                    .find(|w| w.panes.contains_key(&pane))
                    .context("pane not found")?;
                confirm(force, "Stop and close this agent pane?")?;
                wmux_ipc::call(
                    &home,
                    Request::Close {
                        workspace: w.id.clone(),
                        pane,
                    },
                )
                .await?;
            }
            AgentCommand::Event {
                pane,
                state,
                message,
            } => {
                let activity = match state.as_str() {
                    "idle" => AgentActivity::Idle,
                    "input" => AgentActivity::WaitingForInput,
                    "approval" => AgentActivity::WaitingForApproval,
                    _ => AgentActivity::Unknown,
                };
                wmux_ipc::call(
                    &home,
                    Request::AgentEvent {
                        pane,
                        activity,
                        message,
                    },
                )
                .await?;
            }
        },
        Some(Command::Task { command }) => {
            let request = match command {
                TaskCommand::List => {
                    let s = snapshot(&home).await?;
                    if cli.json {
                        println!("{}", serde_json::to_string_pretty(&s.state.tasks)?);
                    } else {
                        for t in s.state.tasks {
                            println!("{}\t{}\t{}", t.id, t.status, t.title);
                        }
                    }
                    return Ok(());
                }
                TaskCommand::Create {
                    title,
                    workspace: selected,
                    description,
                } => {
                    let s = snapshot(&home).await?;
                    Request::TaskCreate {
                        title,
                        description,
                        workspace: workspace(&s, selected)?,
                    }
                }
                TaskCommand::Assign { task, pane } => Request::TaskAssign { task, pane },
                TaskCommand::Status { task, status } => Request::TaskStatus { task, status },
            };
            if let Reply::Task(t) = wmux_ipc::call(&home, request).await? {
                println!("{}", t.id);
            }
        }
        Some(Command::Git {
            workspace: selected,
            command,
        }) => {
            let cwd = if selected.is_some() {
                let s = snapshot(&home).await?;
                PathBuf::from(&s.state.workspaces[&workspace(&s, selected)?].cwd)
            } else {
                std::env::current_dir()?
            };
            match command {
                GitCommand::Status => {
                    let info = wmux_git::info(&cwd)?;
                    println!(
                        "{}\n{} · {} changed files · {} worktrees",
                        info.root, info.branch, info.changed, info.worktrees
                    );
                }
                GitCommand::Diff { staged } => print!(
                    "{}",
                    if staged {
                        wmux_git::staged_diff(&cwd)?
                    } else {
                        wmux_git::diff(&cwd)?
                    }
                ),
                GitCommand::Files => println!(
                    "{}",
                    serde_json::to_string_pretty(&wmux_git::untracked(&cwd)?)?
                ),
                GitCommand::Preview { path } => {
                    let preview = wmux_git::preview(&cwd, &path)?;
                    print!("{}", wmux_security::clean_text(&preview.text));
                    if preview.truncated {
                        println!("\n[preview truncated at 64 KiB]");
                    }
                }
                GitCommand::WorktreeAdd { path, branch } => {
                    wmux_git::create_worktree(&cwd, &path, &branch)?
                }
                GitCommand::WorktreeRemove { path, force } => {
                    confirm(
                        force,
                        "Remove this clean Git worktree? Dirty trees are always refused.",
                    )?;
                    wmux_git::remove_worktree(&cwd, &path)?;
                }
            }
        }
        Some(Command::Capture {
            workspace,
            width,
            height,
            output,
        }) => {
            ensure!(
                (20..=400).contains(&width) && (5..=150).contains(&height),
                "capture size exceeds supported limits"
            );
            let buffer =
                wmux_tui::render_buffer(snapshot(&home).await?, workspace, config, width, height)?;
            let mut text = String::new();
            for y in 0..height {
                for x in 0..width {
                    text.push_str(buffer[(x, y)].symbol());
                }
                text.push('\n');
            }
            std::fs::write(output, text)?;
        }
    }
    Ok(())
}
async fn doctor(home: &Path, config: &Config) -> Result<()> {
    use wmux_agents::Adapter;
    println!(
        "Platform: {} / {}",
        std::env::consts::OS,
        std::env::consts::ARCH
    );
    println!(
        "Terminal: {} · interactive input: {}",
        std::env::var("TERM").unwrap_or("not advertised (normal on Windows)".into()),
        std::io::stdin().is_terminal()
    );
    println!(
        "Configuration: valid · {}",
        home.join("config.toml").display()
    );
    let shell = wmux_agents::find(&config.shell[0]);
    println!(
        "Shell: {:?} · {}",
        config.shell,
        if shell.is_some() { "found" } else { "MISSING" }
    );
    println!("IPC: {}", wmux_ipc::endpoint(home)?);
    println!(
        "Daemon: {}",
        if matches!(wmux_ipc::call(home, Request::Ping).await, Ok(Reply::Pong)) {
            "connected"
        } else {
            "not running"
        }
    );
    for name in wmux_agents::BUILTINS {
        let a = wmux_agents::CommandAgent(name.into());
        println!(
            "Agent {name}: {}",
            if a.available(config) {
                "installed"
            } else {
                "not installed (optional)"
            }
        );
    }
    let (tx, mut rx) = tokio::sync::mpsc::channel(64);
    let command = if cfg!(windows) {
        vec![
            "cmd.exe".into(),
            "/d".into(),
            "/c".into(),
            "echo WMUX_PTY_OK".into(),
        ]
    } else {
        vec!["/bin/sh".into(), "-c".into(), "printf WMUX_PTY_OK".into()]
    };
    let mut pty = wmux_pty::Pty::spawn(
        "doctor".into(),
        &command,
        &std::env::current_dir()?.to_string_lossy(),
        config,
        home,
        tx,
        &tokio::runtime::Handle::current(),
    )?;
    pty.resize(100, 30)?;
    let mut output = Vec::new();
    let mut emulator = wmux_terminal::Terminal::new(30, 100, 0);
    tokio::time::timeout(Duration::from_secs(10), async {
        while let Some(event) = rx.recv().await {
            match event {
                wmux_pty::Event::Output(_, data) => {
                    output.extend_from_slice(&data);
                    emulator.process(&data);
                    let replies = emulator.events().replies;
                    if !replies.is_empty() {
                        pty.input(replies)?;
                    }
                    if String::from_utf8_lossy(&output).contains("WMUX_PTY_OK") {
                        break;
                    }
                }
                wmux_pty::Event::Eof(_) => break,
                wmux_pty::Event::Error(_, e) => bail!("{e}"),
            }
        }
        Ok::<_, anyhow::Error>(())
    })
    .await
    .context("PTY self-test timed out")??;
    ensure!(
        String::from_utf8_lossy(&output).contains("WMUX_PTY_OK"),
        "PTY self-test output missing"
    );
    pty.terminate()?;
    println!("Native PTY: spawn/output/resize/termination passed");
    ensure!(shell.is_some(), "configured shell not found");
    Ok(())
}
#[tokio::main(flavor = "current_thread")]
async fn main() {
    let args = std::env::args_os().skip(1).collect::<Vec<_>>();
    match compat::run(&args) {
        Ok(Some(code)) => std::process::exit(code),
        Ok(None) => (),
        Err(e) => {
            eprintln!("wmux: {e:#}");
            std::process::exit(1);
        }
    }
    let cli = Cli::parse();
    if let Err(e) = execute(cli).await {
        eprintln!("wmux: {e:#}");
        std::process::exit(1);
    }
}
