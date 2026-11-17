use anyhow::{Context, Result, bail, ensure};
use std::{collections::BTreeMap, path::PathBuf, time::Duration};
use tokio::sync::{mpsc, oneshot, watch};
use wmux_core::{
    config::Config,
    layout::{Axis, Node},
    *,
};
use wmux_ipc::{Reply, Request, Snapshot};
use wmux_terminal::Terminal;

struct Live {
    pty: wmux_pty::Pty,
    terminal: Terminal,
    cols: u16,
    rows: u16,
}
struct Command {
    client: Option<String>,
    request: Request,
    reply: oneshot::Sender<Result<Reply>>,
}
#[derive(Clone)]
struct Handle {
    commands: mpsc::Sender<Command>,
    revision: watch::Receiver<u64>,
    shutdown: watch::Sender<bool>,
}
impl Handle {
    async fn call(&self, request: Request) -> Result<Reply> {
        self.call_as(request, None).await
    }
    async fn call_as(&self, request: Request, client: Option<String>) -> Result<Reply> {
        let (tx, rx) = oneshot::channel();
        self.commands
            .send(Command {
                client,
                request,
                reply: tx,
            })
            .await
            .context("daemon stopped")?;
        rx.await.context("daemon stopped")?
    }
}
struct Actor {
    home: PathBuf,
    owners: BTreeMap<String, String>,
    state: State,
    live: BTreeMap<String, Live>,
    pending_save: bool,
    store: wmux_storage::Store,
    config: Config,
    events: mpsc::Sender<wmux_pty::Event>,
    runtime: tokio::runtime::Handle,
    revision: u64,
    changes: watch::Sender<u64>,
}
impl Actor {
    fn workspace_id(&self, selector: &str) -> Result<String> {
        if self.state.workspaces.contains_key(selector) {
            return Ok(selector.into());
        }
        self.state
            .workspaces
            .values()
            .find(|w| w.name == selector)
            .map(|w| w.id.clone())
            .context("workspace not found")
    }
    fn pane_workspace(&self, pane: &str) -> Result<String> {
        self.state
            .workspaces
            .values()
            .find(|w| w.panes.contains_key(pane))
            .map(|w| w.id.clone())
            .context("pane not found")
    }
    fn changed(&mut self) {
        self.revision = self.revision.wrapping_add(1);
        self.changes.send_replace(self.revision);
    }
    fn save(&mut self) -> Result<()> {
        self.store.save(&self.state)?;
        self.pending_save = false;
        Ok(())
    }
    fn retire(&mut self, pane: &str) {
        if let Some(live) = self.live.remove(pane) {
            // ClosePseudoConsole can wait for its output reader. Never block
            // the actor that drains the reader's bounded event queue.
            self.runtime.spawn_blocking(move || drop(live));
        }
    }
    fn notify(&mut self, workspace: String, pane: String, text: String) {
        if !self.config.notifications {
            return;
        }
        self.pending_save = true;
        self.state.notifications.push(Notification {
            id: id(),
            workspace,
            pane,
            text: wmux_security::clean_text(&text)
                .chars()
                .take(2048)
                .collect(),
            created: now(),
        });
        if self.state.notifications.len() > 500 {
            self.state
                .notifications
                .drain(..self.state.notifications.len() - 500);
        }
    }
    fn spawn(
        &mut self,
        workspace: &str,
        command: Vec<String>,
        agent: Option<String>,
        axis: Axis,
        target: Option<String>,
    ) -> Result<String> {
        ensure!(
            self.live.len() < self.config.max_panes,
            "pane process limit reached"
        );
        let w = self
            .state
            .workspaces
            .get(workspace)
            .context("workspace not found")?;
        ensure!(
            w.panes.len() < 64,
            "workspace metadata limit reached; close old panes first"
        );
        let command = if let Some(ref a) = agent {
            ensure!(command.is_empty(), "use either an agent or command");
            wmux_agents::launch(a, &self.config)?
        } else if command.is_empty() {
            self.config.shell.clone()
        } else {
            command
        };
        ensure!(
            command.len() <= 256
                && command
                    .iter()
                    .all(|s| s.len() <= 65536 && !s.contains('\0')),
            "invalid command arguments"
        );
        let target = target.or_else(|| w.focused.clone());
        if let Some(ref target) = target {
            ensure!(w.panes.contains_key(target), "split target not found");
        }
        let pane = id();
        let cwd = w.cwd.clone();
        let pty = wmux_pty::Pty::spawn(
            pane.clone(),
            &command,
            &cwd,
            &self.config,
            &self.home,
            self.events.clone(),
            &self.runtime,
        )?;
        let p = Pane {
            id: pane.clone(),
            title: agent.clone().unwrap_or_else(|| {
                std::path::Path::new(&command[0])
                    .file_name()
                    .unwrap_or_default()
                    .to_string_lossy()
                    .into()
            }),
            command,
            cwd,
            state: ProcessState::Running,
            exit_code: None,
            pid: pty.pid(),
            agent,
            activity: AgentActivity::Unknown,
        };
        let w = self.state.workspaces.get_mut(workspace).unwrap();
        w.panes.insert(pane.clone(), p);
        if let (Some(layout), Some(target)) = (&mut w.layout, target) {
            ensure!(layout.split(&target, &pane, axis), "invalid layout target");
        } else {
            w.layout = Some(Node::Leaf(pane.clone()));
        }
        w.focused = Some(pane.clone());
        self.live.insert(
            pane.clone(),
            Live {
                pty,
                terminal: Terminal::new(24, 80, self.config.effective_scrollback()),
                cols: 80,
                rows: 24,
            },
        );
        if let Err(e) = self.save() {
            self.retire(&pane);
            let w = self.state.workspaces.get_mut(workspace).unwrap();
            w.panes.remove(&pane);
            w.layout = w.layout.take().and_then(|n| n.remove(&pane));
            w.focused = w.layout.as_ref().and_then(|n| n.leaves().first().cloned());
            return Err(e);
        }
        self.changed();
        Ok(pane)
    }
    fn close(&mut self, workspace: &str, pane: &str) -> Result<()> {
        let w = self
            .state
            .workspaces
            .get(workspace)
            .context("workspace not found")?;
        ensure!(w.panes.contains_key(pane), "pane not found");
        if let Some(p) = self.live.get_mut(pane) {
            p.pty.terminate()?;
        }
        self.retire(pane);
        let w = self.state.workspaces.get_mut(workspace).unwrap();
        w.panes.remove(pane);
        w.layout = w.layout.take().and_then(|n| n.remove(pane));
        if w.focused.as_deref() == Some(pane) {
            w.focused = w.layout.as_ref().and_then(|n| n.leaves().first().cloned());
        }
        if w.panes.is_empty() {
            w.zoomed = false;
        }
        self.save()?;
        self.changed();
        Ok(())
    }
    fn request(&mut self, request: Request, client: Option<&str>) -> Result<Reply> {
        // A Control frame delivered internally with a client releases that
        // connection's leases. The public handshake is handled before dispatch.
        if matches!(request, Request::Control) {
            if let Some(client) = client {
                self.owners.retain(|_, owner| owner != client);
            }
            return Ok(Reply::Ok);
        }
        let workspace = match &request {
            Request::Kill { workspace }
            | Request::Split { workspace, .. }
            | Request::Close { workspace, .. }
            | Request::Focus { workspace, .. }
            | Request::Zoom { workspace }
            | Request::Adjust { workspace, .. }
            | Request::Swap { workspace, .. }
            | Request::Rename { workspace, .. }
            | Request::Layout { workspace, .. } => Some(self.workspace_id(workspace)?),
            Request::Input { pane, .. } | Request::Resize { pane, .. } => {
                Some(self.pane_workspace(pane)?)
            }
            _ => None,
        };
        if let Some(workspace) = workspace {
            if let Some(owner) = self.owners.get(&workspace) {
                ensure!(
                    Some(owner.as_str()) == client,
                    "workspace controlled by another client; attach with --read-only or detach its writer"
                );
            } else if let Some(client) = client {
                self.owners.insert(workspace, client.into());
            }
        }
        match request {
            Request::Control => unreachable!(),
            Request::Search {
                pane,
                query,
                offset,
                backwards,
            } => {
                ensure!(query.len() <= 1024, "search query exceeds 1 KiB");
                return Ok(Reply::Search(
                    self.live
                        .get_mut(&pane)
                        .context("pane process is not connected")?
                        .terminal
                        .search(&query, offset, backwards),
                ));
            }
            Request::Ping => return Ok(Reply::Pong),
            Request::Get { scrollback } => {
                let screens = self
                    .live
                    .iter_mut()
                    .map(|(id, p)| {
                        (
                            id.clone(),
                            p.terminal
                                .snapshot(scrollback.get(id).copied().unwrap_or(0)),
                        )
                    })
                    .collect();
                return Ok(Reply::Snapshot(Snapshot {
                    state: self.state.clone(),
                    screens,
                    revision: self.revision,
                }));
            }
            Request::Subscribe => bail!("Subscribe requires a streaming connection"),
            Request::New { name, cwd, command } => {
                ensure!(
                    !name.trim().is_empty()
                        && name.chars().count() <= 100
                        && !name.chars().any(char::is_control),
                    "invalid workspace name"
                );
                ensure!(self.state.workspaces.len() < 100, "workspace limit reached");
                ensure!(
                    !self.state.workspaces.values().any(|w| w.name == name),
                    "workspace already exists"
                );
                let cwd = std::fs::canonicalize(cwd)
                    .context("invalid workspace directory")?
                    .to_string_lossy()
                    .into_owned();
                let key = id();
                self.state.workspaces.insert(
                    key.clone(),
                    Workspace {
                        id: key.clone(),
                        name,
                        cwd,
                        panes: BTreeMap::new(),
                        layout: None,
                        focused: None,
                        zoomed: false,
                    },
                );
                if let Err(e) = self.spawn(&key, command, None, Axis::Columns, None) {
                    self.state.workspaces.remove(&key);
                    return Err(e);
                }

                let count = match self.config.default_layout.as_str() {
                    "grid" | "main-stack" => 4,
                    "columns" | "rows" => 2,
                    _ => 1,
                };
                for _ in 1..count {
                    if let Err(e) = self.spawn(&key, Vec::new(), None, Axis::Columns, None) {
                        let ids = self.state.workspaces[&key]
                            .panes
                            .keys()
                            .cloned()
                            .collect::<Vec<_>>();
                        for id in ids {
                            self.retire(&id);
                        }
                        self.state.workspaces.remove(&key);
                        self.save()?;
                        self.changed();
                        return Err(e);
                    }
                }
                let w = self.state.workspaces.get_mut(&key).unwrap();
                if let Some(n) = &w.layout {
                    w.layout = Node::preset(&n.leaves(), &self.config.default_layout);
                }
                self.save()?;
                self.changed();
                return Ok(Reply::Created(key));
            }
            Request::Split {
                workspace,
                pane,
                axis,
                command,
                agent,
            } => {
                let w = self.workspace_id(&workspace)?;
                return Ok(Reply::Created(self.spawn(&w, command, agent, axis, pane)?));
            }
            Request::Kill { workspace } => {
                let w = self.workspace_id(&workspace)?;
                let ids: Vec<_> = self.state.workspaces[&w].panes.keys().cloned().collect();
                for p in ids {
                    self.close(&w, &p)?;
                }
                self.state.workspaces.remove(&w);
            }
            Request::Close { workspace, pane } => {
                let w = self.workspace_id(&workspace)?;
                self.close(&w, &pane)?;
                return Ok(Reply::Ok);
            }
            Request::Input { pane, data } => {
                self.live
                    .get(&pane)
                    .context("pane process is not connected")?
                    .pty
                    .input(data)?;
                return Ok(Reply::Ok);
            }
            Request::Resize { pane, cols, rows } => {
                let total: usize = self
                    .live
                    .iter()
                    .filter(|(id, _)| *id != &pane)
                    .map(|(_, p)| p.cols as usize * p.rows as usize)
                    .sum();
                ensure!(
                    total + (cols as usize * rows as usize) <= 120000,
                    "aggregate terminal screen cell limit reached"
                );

                let p = self
                    .live
                    .get_mut(&pane)
                    .context("pane process is not connected")?;
                if p.cols != cols || p.rows != rows {
                    p.pty.resize(cols, rows)?;
                    p.terminal.resize(rows, cols);
                    p.cols = cols;
                    p.rows = rows;
                    self.changed();
                }
                return Ok(Reply::Ok);
            }
            Request::Focus { workspace, pane } => {
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                ensure!(w.panes.contains_key(&pane), "pane not found");
                w.focused = Some(pane);
            }
            Request::Zoom { workspace } => {
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                w.zoomed = !w.zoomed;
            }
            Request::Adjust {
                workspace,
                pane,
                delta,
            } => {
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                ensure!(
                    w.layout.as_mut().is_some_and(|n| n.resize(&pane, delta)),
                    "pane has no split divider"
                );
            }
            Request::Swap {
                workspace,
                first,
                second,
            } => {
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                ensure!(
                    w.panes.contains_key(&first) && w.panes.contains_key(&second),
                    "pane not found"
                );
                if let Some(n) = w.layout.as_mut() {
                    n.swap(&first, &second);
                }
            }
            Request::Rename {
                workspace,
                pane,
                title,
            } => {
                ensure!(
                    !title.trim().is_empty()
                        && title.chars().count() <= 100
                        && !title.chars().any(char::is_control),
                    "invalid title"
                );
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                if let Some(p) = pane {
                    w.panes.get_mut(&p).context("pane not found")?.title = title;
                } else {
                    w.name = title;
                }
            }
            Request::Layout { workspace, name } => {
                ensure!(
                    ["single", "columns", "rows", "grid", "main-stack"].contains(&name.as_str()),
                    "unknown layout"
                );
                let id = self.workspace_id(&workspace)?;
                let w = self.state.workspaces.get_mut(&id).unwrap();
                let ids = w.layout.as_ref().map(|n| n.leaves()).unwrap_or_default();
                if name == "single" {
                    w.zoomed = true;
                } else {
                    w.layout = Node::preset(&ids, &name);
                    w.zoomed = false;
                }
            }
            Request::TaskCreate {
                title,
                description,
                workspace,
            } => {
                ensure!(
                    !title.trim().is_empty() && title.len() <= 512 && description.len() <= 65536,
                    "invalid task text"
                );
                ensure!(self.state.tasks.len() < 10000, "task limit reached");
                let workspace = self.workspace_id(&workspace)?;
                let description = wmux_security::clean_text(&description);
                ensure!(
                    !title.chars().any(char::is_control),
                    "task title contains control characters"
                );
                let task = Task {
                    id: id(),
                    title,
                    description,
                    workspace,
                    pane: None,
                    repository: None,
                    worktree: None,
                    status: "open".into(),
                    created: now(),
                    completed: None,
                };
                self.state.tasks.push(task.clone());
                self.save()?;
                self.changed();
                return Ok(Reply::Task(task));
            }
            Request::TaskAssign { task, pane } => {
                let workspace = self.pane_workspace(&pane)?;
                let task = self
                    .state
                    .tasks
                    .iter_mut()
                    .find(|t| t.id == task)
                    .context("task not found")?;
                ensure!(
                    task.workspace == workspace,
                    "task and pane must belong to the same workspace"
                );
                task.pane = Some(pane);
            }
            Request::TaskStatus { task, status } => {
                ensure!(
                    ["open", "running", "completed", "failed", "cancelled"]
                        .contains(&status.as_str()),
                    "invalid task status"
                );
                let t = self
                    .state
                    .tasks
                    .iter_mut()
                    .find(|t| t.id == task)
                    .context("task not found")?;
                t.completed = if status == "completed" {
                    Some(now())
                } else {
                    None
                };
                t.status = status;
            }
            Request::AgentEvent {
                pane,
                activity,
                message,
            } => {
                let workspace = self.pane_workspace(&pane)?;
                let p = self
                    .state
                    .workspaces
                    .get_mut(&workspace)
                    .unwrap()
                    .panes
                    .get_mut(&pane)
                    .unwrap();
                ensure!(
                    p.state == ProcessState::Running,
                    "agent process is not running"
                );
                p.activity = activity;
                if let Some(text) = message {
                    self.notify(workspace, pane, text);
                }
            }

            Request::ApprovalCreate {
                pane,
                title,
                details,
                destructive,
                ttl,
            } => {
                ensure!(
                    !title.trim().is_empty() && title.len() <= 512 && details.len() <= 65536,
                    "invalid approval context"
                );
                ensure!(
                    (1..=3600).contains(&ttl),
                    "approval TTL must be 1..3600 seconds"
                );
                let workspace = self.pane_workspace(&pane)?;
                ensure!(
                    self.state.workspaces[&workspace].panes[&pane].state == ProcessState::Running,
                    "approval requester is not running"
                );
                ensure!(
                    self.state
                        .approvals
                        .iter()
                        .filter(|a| a.status == ApprovalStatus::Pending)
                        .count()
                        < 128,
                    "approval queue full"
                );
                if self.state.approvals.len() >= 1000 {
                    self.state
                        .approvals
                        .retain(|a| a.status == ApprovalStatus::Pending);
                }
                ensure!(
                    !title.chars().any(char::is_control),
                    "approval title contains control characters"
                );
                let details = wmux_security::clean_text(&details);
                let key = id();
                self.state.approvals.push(Approval {
                    id: key.clone(),
                    pane: pane.clone(),
                    workspace: workspace.clone(),
                    title: title.clone(),
                    details,
                    destructive,
                    status: ApprovalStatus::Pending,
                    created: now(),
                    expires: now() + ttl,
                });
                self.state
                    .workspaces
                    .get_mut(&workspace)
                    .unwrap()
                    .panes
                    .get_mut(&pane)
                    .unwrap()
                    .activity = AgentActivity::WaitingForApproval;
                self.notify(workspace, pane, format!("Approval requested: {title}"));
                self.save()?;
                self.changed();
                return Ok(Reply::Created(key));
            }
            Request::ApprovalDecide { id, approve } => {
                let a = self
                    .state
                    .approvals
                    .iter_mut()
                    .find(|a| a.id == id)
                    .context("approval not found")?;
                ensure!(
                    a.status == ApprovalStatus::Pending && a.expires > now(),
                    "approval is no longer pending"
                );
                ensure!(
                    self.state
                        .workspaces
                        .get(&a.workspace)
                        .and_then(|w| w.panes.get(&a.pane))
                        .is_some_and(|p| p.state == ProcessState::Running),
                    "requester is not running"
                );
                a.status = if approve {
                    ApprovalStatus::Approved
                } else {
                    ApprovalStatus::Rejected
                };
                if let Some(p) = self
                    .state
                    .workspaces
                    .get_mut(&a.workspace)
                    .and_then(|w| w.panes.get_mut(&a.pane))
                {
                    p.activity = AgentActivity::Unknown;
                }
            }
            Request::Shutdown => return Ok(Reply::Ok),
        }
        self.save()?;
        self.changed();
        Ok(Reply::Ok)
    }
    fn event(&mut self, event: wmux_pty::Event) {
        match event {
            wmux_pty::Event::Output(id, data) => {
                if let Some(p) = self.live.get_mut(&id) {
                    p.terminal.process(&data);
                    let events = p.terminal.events();
                    if !events.replies.is_empty()
                        && let Err(e) = p.pty.input(events.replies)
                    {
                        tracing::warn!(pane=%id,error=%e,"terminal reply failed");
                    }
                    if let Ok(w) = self.pane_workspace(&id) {
                        for text in events.notifications {
                            self.notify(w.clone(), id.clone(), text);
                        }
                    }
                    self.changed();
                }
            }
            wmux_pty::Event::Error(id, error) => {
                tracing::warn!(pane=%id,"PTY I/O failed");
                if let Ok(w) = self.pane_workspace(&id) {
                    self.notify(w, id, error);
                    self.changed();
                }
            }
            wmux_pty::Event::Eof(_) => {
                self.poll();
            }
        }
    }
    fn poll(&mut self) {
        if self.pending_save
            && let Err(e) = self.save()
        {
            tracing::error!(error=%e,"cannot persist notification history");
        }
        let mut expired = false;
        for a in &mut self.state.approvals {
            let running = self
                .state
                .workspaces
                .get(&a.workspace)
                .and_then(|w| w.panes.get(&a.pane))
                .is_some_and(|p| p.state == ProcessState::Running);
            if a.status == ApprovalStatus::Pending && (a.expires <= now() || !running) {
                a.status = ApprovalStatus::Expired;
                expired = true;
            }
        }
        if expired {
            if let Err(e) = self.save() {
                tracing::error!(error=%e,"cannot persist approval expiry");
            }
            self.changed();
        }

        let mut done = Vec::new();
        for (id, p) in &mut self.live {
            match p.pty.poll() {
                Ok(Some(code)) => done.push((id.clone(), Some(code))),
                Ok(None) => (),
                Err(_) => done.push((id.clone(), None)),
            }
        }
        for (id, code) in done {
            // Keep the final screen in memory for inspection until the user closes it.
            if let Ok(w) = self.pane_workspace(&id) {
                let p = self
                    .state
                    .workspaces
                    .get_mut(&w)
                    .unwrap()
                    .panes
                    .get_mut(&id)
                    .unwrap();
                if p.state != ProcessState::Running {
                    continue;
                }
                p.exit_code = code;
                p.pid = None;
                p.activity = AgentActivity::Unknown;
                p.state = match code {
                    Some(0) => ProcessState::Completed,
                    Some(_) => ProcessState::Failed,
                    None => ProcessState::Disconnected,
                };
                self.notify(
                    w,
                    id,
                    format!(
                        "Process exited: {}",
                        code.map(|c| c.to_string())
                            .unwrap_or("unknown status".into())
                    ),
                );
                if let Err(e) = self.save() {
                    tracing::error!(error=%e,"cannot persist process exit");
                }
                self.changed();
            }
        }
    }
}
pub async fn serve(home: PathBuf, config: Config) -> Result<()> {
    wmux_security::private_dir(&home)?;
    let lockpath = home.join("daemon.lock");
    let lock = std::fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(&lockpath)?;
    wmux_security::private_file(&lockpath)?;
    fs2::FileExt::try_lock_exclusive(&lock)
        .context("another Rust wmuxd owns this state directory")?;
    let store = wmux_storage::Store::open(&home)?;
    let state = store.load()?;
    let (tx, mut rx) = mpsc::channel::<Command>(256);
    let (events, mut erx) = mpsc::channel(256);
    let (changes, revision) = watch::channel(0);
    let (shutdown, mut shutdown_rx) = watch::channel(false);
    let mut actor = Actor {
        home: home.clone(),
        owners: BTreeMap::new(),
        state,
        live: BTreeMap::new(),
        pending_save: false,
        store,
        config,
        events,
        runtime: tokio::runtime::Handle::current(),
        revision: 0,
        changes,
    };
    let (ticks, mut tick_rx) = mpsc::channel::<()>(1);
    let ticker = tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_millis(200));
        loop {
            interval.tick().await;
            if ticks.send(()).await.is_err() {
                break;
            }
        }
    });
    let (inbox, mut inbox_rx) = mpsc::channel::<ActorMessage>(256);
    let forward = tokio::spawn(async move {
        loop {
            tokio::select! {
                c=rx.recv()=>match c {Some(c)=>if inbox.send(ActorMessage::Command(c)).await.is_err(){break;},None=>break},
                e=erx.recv()=>match e {Some(e)=>if inbox.send(ActorMessage::Event(e)).await.is_err(){break;},None=>break},
                _=tick_rx.recv()=>if inbox.send(ActorMessage::Tick).await.is_err(){break;},
            }
        }
    });
    let worker = tokio::task::spawn_blocking(move || {
        while let Some(message) = inbox_rx.blocking_recv() {
            match message {
                ActorMessage::Command(c) => {
                    let result = actor.request(c.request, c.client.as_deref());
                    let _ = c.reply.send(result);
                }
                ActorMessage::Event(e) => actor.event(e),
                ActorMessage::Tick => actor.poll(),
            }
        }
        // Dropping every PTY terminates its process group/Job. Persist stopped
        // metadata only after releasing the native process resources.
        actor.live.clear();
        for w in actor.state.workspaces.values_mut() {
            for p in w.panes.values_mut() {
                if p.state == ProcessState::Running {
                    p.state = ProcessState::Stopped;
                    p.pid = None;
                }
            }
        }
        actor.save()
    });
    let handle = Handle {
        commands: tx,
        revision,
        shutdown: shutdown.clone(),
    };
    let listener = listen(&home, handle, shutdown.clone());
    tokio::pin!(listener);
    let result = tokio::select! {
        r=&mut listener=>r,
        _=tokio::signal::ctrl_c()=>Ok(()),
        _=shutdown_rx.changed()=>Ok(()),
    };
    shutdown.send_replace(true);
    forward.abort();
    ticker.abort();
    worker.await.context("daemon actor panicked")??;
    #[cfg(unix)]
    {
        let _ = std::fs::remove_file(home.join("daemon.sock"));
    }
    drop(lock);
    result
}
enum ActorMessage {
    Command(Command),
    Event(wmux_pty::Event),
    Tick,
}
async fn connection(
    mut stream: wmux_ipc::Connection,
    handle: Handle,
    mut shutdown: watch::Receiver<bool>,
) -> Result<()> {
    use wmux_ipc::*;
    let envelope: Envelope =
        tokio::time::timeout(Duration::from_secs(5), read(&mut stream)).await??;
    if envelope.version != VERSION {
        write(
            &mut stream,
            &Response::new(Err(anyhow::anyhow!(
                "unsupported IPC version {}",
                envelope.version
            ))),
        )
        .await?;
        return Ok(());
    }
    if matches!(envelope.request, Request::Control) {
        let client = id();
        let result: Result<()> = async {
            write(&mut stream, &Response::new(Ok(Reply::Ok))).await?;
            loop {
                let frame: Envelope = tokio::select! {
                    frame = read(&mut stream) => frame?,
                    _ = shutdown.changed() => break,
                };
                let stopping = matches!(frame.request, Request::Shutdown);
                let reply = if frame.version != VERSION {
                    Err(anyhow::anyhow!("unsupported IPC version {}", frame.version))
                } else if matches!(frame.request, Request::Control | Request::Subscribe) {
                    Err(anyhow::anyhow!("stream handshake already completed"))
                } else {
                    handle.call_as(frame.request, Some(client.clone())).await
                };
                let succeeded = reply.is_ok();
                tokio::time::timeout(
                    Duration::from_secs(5),
                    write(&mut stream, &Response::new(reply)),
                )
                .await??;
                if stopping && succeeded {
                    handle.shutdown.send_replace(true);
                    break;
                }
            }
            Ok(())
        }
        .await;
        // Always release on EOF, timeout, protocol/write error, and clean exit.
        let _ = handle.call_as(Request::Control, Some(client)).await;
        result?;
    } else if matches!(envelope.request, Request::Subscribe) {
        let mut changes = handle.revision.clone();
        loop {
            let reply = handle
                .call(Request::Get {
                    scrollback: BTreeMap::new(),
                })
                .await;
            tokio::time::timeout(
                Duration::from_secs(5),
                write(&mut stream, &Response::new(reply)),
            )
            .await??;
            tokio::select! {r=changes.changed()=>{r?;},_=shutdown.changed()=>break}
            tokio::time::sleep(Duration::from_millis(33)).await;
        }
    } else {
        let stopping = matches!(envelope.request, Request::Shutdown);
        let result = handle.call(envelope.request).await;
        let succeeded = result.is_ok();
        tokio::time::timeout(
            Duration::from_secs(5),
            write(&mut stream, &Response::new(result)),
        )
        .await??;
        // Flush acknowledgement before the runtime begins cancelling clients.
        if stopping && succeeded {
            handle.shutdown.send_replace(true);
        }
    }
    Ok(())
}
async fn listen(
    home: &std::path::Path,
    handle: Handle,
    shutdown: watch::Sender<bool>,
) -> Result<()> {
    let endpoint = wmux_ipc::endpoint(home)?;
    let permits = std::sync::Arc::new(tokio::sync::Semaphore::new(64));
    #[cfg(unix)]
    let listener = {
        use std::os::unix::fs::{FileTypeExt, PermissionsExt};
        if let Ok(meta) = std::fs::symlink_metadata(&endpoint) {
            ensure!(
                meta.file_type().is_socket(),
                "IPC endpoint exists and is not a socket"
            );
            std::fs::remove_file(&endpoint)?;
        }
        let listener = tokio::net::UnixListener::bind(&endpoint)?;
        std::fs::set_permissions(&endpoint, std::fs::Permissions::from_mode(0o600))?;
        listener
    };
    #[cfg(windows)]
    let mut windows_server = windows_pipe(&endpoint, true)?;
    loop {
        #[cfg(unix)]
        let stream: wmux_ipc::Connection = Box::new(listener.accept().await?.0);
        #[cfg(windows)]
        let stream: wmux_ipc::Connection = {
            windows_server.connect().await?;
            // Keep a listening instance alive before dispatching the accepted
            // connection. Otherwise a fast client can close the last handle
            // while the next client opens, yielding ERROR_FILE_NOT_FOUND.
            let next = windows_pipe(&endpoint, false)?;
            Box::new(std::mem::replace(&mut windows_server, next))
        };
        let Ok(permit) = permits.clone().try_acquire_owned() else {
            drop(stream);
            continue;
        };
        let h = handle.clone();
        let s = shutdown.subscribe();
        tokio::spawn(async move {
            let _permit = permit;
            if let Err(e) = connection(stream, h, s).await {
                tracing::debug!(error=%e,"IPC client disconnected");
            }
        });
    }
}

#[cfg(windows)]
fn windows_pipe(
    endpoint: &str,
    first: bool,
) -> Result<tokio::net::windows::named_pipe::NamedPipeServer> {
    use tokio::net::windows::named_pipe::ServerOptions;
    let mut security = wmux_security::windows::PipeSecurity::new()?;
    // SAFETY: The descriptor outlives synchronous CreateNamedPipe; it grants
    // this user only and rejects remote clients on every listening instance.
    unsafe {
        Ok(ServerOptions::new()
            .first_pipe_instance(first)
            .reject_remote_clients(true)
            .create_with_security_attributes_raw(
                endpoint,
                std::ptr::from_mut(&mut security.attrs).cast(),
            )?)
    }
}
