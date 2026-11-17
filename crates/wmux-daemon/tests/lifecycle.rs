use anyhow::{Context, Result};
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    time::Duration,
};
use wmux_core::{ProcessState, config::Config, layout::Axis};
use wmux_ipc::{Envelope, Reply, Request, Response, Snapshot, VERSION};

static DAEMON_SLOTS: tokio::sync::Semaphore = tokio::sync::Semaphore::const_new(2);

#[tokio::test]
async fn writer_ownership_releases_on_disconnect_and_observers_remain_available() {
    let mut d = Daemon::start().await;
    let (_, pane) = d.new_workspace("writer-ownership").await;
    let mut first = wmux_ipc::Control::connect(&d.home).await.unwrap();
    let mut second = wmux_ipc::Control::connect(&d.home).await.unwrap();
    let resize = || Request::Resize {
        pane: pane.clone(),
        cols: 90,
        rows: 26,
    };
    first.call(resize()).await.unwrap();
    assert!(
        second
            .call(resize())
            .await
            .unwrap_err()
            .to_string()
            .contains("another client")
    );
    assert!(
        wmux_ipc::call(
            &d.home,
            Request::Input {
                pane: pane.clone(),
                data: b"unwanted".to_vec()
            }
        )
        .await
        .is_err()
    );
    assert_eq!(d.snapshot().await.screens[&pane].cols, 90);
    // An application error must not poison the control channel or its lease.
    assert!(
        first
            .call(Request::Input {
                pane: "missing".into(),
                data: vec![]
            })
            .await
            .is_err()
    );
    first.call(resize()).await.unwrap();
    assert!(first.is_connected());
    drop(first);
    tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            if second.call(resize()).await.is_ok() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    drop(second);
    d.stop().await;
}
struct Daemon {
    _slot: tokio::sync::SemaphorePermit<'static>,
    child: Child,
    home: PathBuf,
}
impl Daemon {
    async fn start() -> Self {
        let slot = DAEMON_SLOTS.acquire().await.unwrap();
        let home = std::env::temp_dir().join(format!("wmux-it-{}", wmux_core::id()));
        std::fs::create_dir_all(&home).unwrap();
        let mut config = Config {
            shell: if cfg!(windows) {
                vec![
                    "powershell.exe".into(),
                    "-NoLogo".into(),
                    "-NoProfile".into(),
                ]
            } else {
                vec!["/bin/bash".into(), "--noprofile".into(), "--norc".into()]
            },
            ..Config::default()
        };
        config
            .agents
            .insert("missing".into(), vec!["wmux-no-such-agent-7823".into()]);
        std::fs::write(home.join("config.toml"), toml_string(&config)).unwrap();
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_wmuxd"));
        cmd.env("WMUX_HOME", &home)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::inherit());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            cmd.creation_flags(0x08000000);
        }
        let child = cmd.spawn().unwrap();
        let mut d = Self {
            child,
            home,
            _slot: slot,
        };
        for _ in 0..1200 {
            if matches!(
                wmux_ipc::call(&d.home, Request::Ping).await,
                Ok(Reply::Pong)
            ) {
                return d;
            }
            assert!(
                d.child.try_wait().unwrap().is_none(),
                "daemon exited before ready"
            );
            tokio::time::sleep(Duration::from_millis(25)).await;
        }
        panic!("daemon startup timed out")
    }
    async fn call(&self, r: Request) -> Reply {
        wmux_ipc::call(&self.home, r).await.unwrap()
    }
    async fn snapshot(&self) -> Snapshot {
        get(&self.home).await.unwrap()
    }
    async fn new_workspace(&self, name: &str) -> (String, String) {
        let Reply::Created(w) = self
            .call(Request::New {
                name: name.into(),
                cwd: std::env::current_dir().unwrap().to_string_lossy().into(),
                command: vec![],
            })
            .await
        else {
            panic!()
        };
        let s = self.snapshot().await;
        let p = s.state.workspaces[&w].focused.clone().unwrap();
        (w, p)
    }
    async fn stop(&mut self) {
        let _ = self.call(Request::Shutdown).await;
        for _ in 0..1200 {
            if self.child.try_wait().unwrap().is_some() {
                return;
            }
            tokio::time::sleep(Duration::from_millis(25)).await;
        }
        panic!("daemon failed to shut down")
    }
}
impl Drop for Daemon {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_dir_all(&self.home);
    }
}
fn toml_string(config: &Config) -> String {
    toml::to_string(config).unwrap()
}
async fn get(home: &Path) -> Result<Snapshot> {
    match wmux_ipc::call(
        home,
        Request::Get {
            scrollback: BTreeMap::new(),
        },
    )
    .await?
    {
        Reply::Snapshot(s) => Ok(s),
        _ => anyhow::bail!("invalid snapshot"),
    }
}
async fn wait_text(d: &Daemon, pane: &str, text: &str) -> Snapshot {
    let mut last = String::new();
    for _ in 0..200 {
        let s = d.snapshot().await;
        if let Some(screen) = s.screens.get(pane) {
            last = screen.text();
            if last.contains(text) {
                return s;
            }
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("missing {text:?}; screen={last:?}")
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn real_shell_split_resize_detach_reattach_tasks_and_failures() {
    let mut d = Daemon::start().await;
    let (w, p) = d.new_workspace("integration").await;
    let command = if cfg!(windows) {
        "Write-Output ('WMUX_' + 'LIVE_9371')\r"
    } else {
        "printf 'WMUX_%s\\n' LIVE_9371\r"
    };
    d.call(Request::Input {
        pane: p.clone(),
        data: command.as_bytes().to_vec(),
    })
    .await;
    wait_text(&d, &p, "WMUX_LIVE_9371").await;
    let s = d.snapshot().await;
    let pid = s.state.workspaces[&w].panes[&p].pid;
    assert!(pid.is_some());
    d.call(Request::Resize {
        pane: p.clone(),
        cols: 100,
        rows: 30,
    })
    .await;
    assert_eq!(d.snapshot().await.screens[&p].cols, 100);
    let mut observer = wmux_ipc::connect(&d.home).await.unwrap();
    wmux_ipc::write(
        &mut observer,
        &Envelope {
            version: VERSION,
            request: Request::Subscribe,
        },
    )
    .await
    .unwrap();
    assert!(matches!(
        wmux_ipc::read::<_, Response>(&mut observer)
            .await
            .unwrap()
            .result()
            .unwrap(),
        Reply::Snapshot(_)
    ));
    drop(observer); // The only subscribed UI disconnects; shell must survive.
    tokio::time::sleep(Duration::from_millis(100)).await;
    assert_eq!(d.snapshot().await.state.workspaces[&w].panes[&p].pid, pid);
    let mut panes = vec![p.clone()];
    for axis in [Axis::Columns, Axis::Rows, Axis::Rows] {
        let Reply::Created(id) = d
            .call(Request::Split {
                workspace: w.clone(),
                pane: None,
                axis,
                command: vec![],
                agent: None,
            })
            .await
        else {
            panic!()
        };
        panes.push(id);
    }
    d.call(Request::Layout {
        workspace: w.clone(),
        name: "grid".into(),
    })
    .await;
    assert_eq!(
        d.snapshot().await.state.workspaces[&w]
            .layout
            .as_ref()
            .unwrap()
            .leaves()
            .len(),
        4
    );
    assert!(
        wmux_ipc::call(
            &d.home,
            Request::Split {
                workspace: w.clone(),
                pane: None,
                axis: Axis::Rows,
                command: vec![],
                agent: Some("missing".into())
            }
        )
        .await
        .is_err()
    );
    assert_eq!(d.snapshot().await.state.workspaces[&w].panes.len(), 4);
    let Reply::Task(task) = d
        .call(Request::TaskCreate {
            workspace: w.clone(),
            title: "verify PTY".into(),
            description: "real subprocess".into(),
        })
        .await
    else {
        panic!()
    };
    d.call(Request::TaskAssign {
        task: task.id.clone(),
        pane: p.clone(),
    })
    .await;
    d.call(Request::TaskStatus {
        task: task.id,
        status: "completed".into(),
    })
    .await;
    assert!(d.snapshot().await.state.tasks[0].completed.is_some());
    // Invalid sizes cannot corrupt emulator/PTY geometry.
    assert!(
        wmux_ipc::call(
            &d.home,
            Request::Resize {
                pane: p.clone(),
                cols: 0,
                rows: 0
            }
        )
        .await
        .is_err()
    );
    assert_eq!(d.snapshot().await.screens[&p].cols, 100);
    d.call(Request::Close {
        workspace: w.clone(),
        pane: panes[3].clone(),
    })
    .await;
    assert_eq!(d.snapshot().await.state.workspaces[&w].panes.len(), 3);
    d.stop().await;
    // Metadata persists; stale PIDs must never be resurrected.
    let store = wmux_storage::Store::open(&d.home).unwrap();
    let recovered = store.load().unwrap();
    assert_eq!(recovered.workspaces.len(), 1);
    assert!(
        recovered.workspaces[&w]
            .panes
            .values()
            .all(|p| p.pid.is_none() && p.state != ProcessState::Running)
    );
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn protocol_rejects_unknown_versions_and_duplicate_daemon() {
    let mut d = Daemon::start().await;
    let mut s = wmux_ipc::connect(&d.home).await.unwrap();
    wmux_ipc::write(
        &mut s,
        &Envelope {
            version: 999,
            request: Request::Ping,
        },
    )
    .await
    .unwrap();
    assert!(
        wmux_ipc::read::<_, Response>(&mut s)
            .await
            .unwrap()
            .result()
            .is_err()
    );
    let mut duplicate = Command::new(env!("CARGO_BIN_EXE_wmuxd"))
        .env("WMUX_HOME", &d.home)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let status = tokio::task::spawn_blocking(move || duplicate.wait())
        .await
        .unwrap()
        .unwrap();
    assert!(!status.success());
    assert!(matches!(d.call(Request::Ping).await, Reply::Pong));
    d.stop().await;
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn process_crash_and_daemon_crash_are_reported_truthfully() {
    let mut d = Daemon::start().await;
    let (w, p) = d.new_workspace("crash").await;
    d.call(Request::Input {
        pane: p.clone(),
        data: b"exit 7\r".to_vec(),
    })
    .await;
    for _ in 0..200 {
        if d.snapshot().await.state.workspaces[&w].panes[&p].state == ProcessState::Failed {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    assert_eq!(
        d.snapshot().await.state.workspaces[&w].panes[&p].exit_code,
        Some(7)
    );
    let (_, running) = d.new_workspace("surviving-metadata").await;
    d.child.kill().unwrap();
    d.child.wait().unwrap();
    let store = wmux_storage::Store::open(&d.home).unwrap();
    let recovered = store.load().unwrap();
    let pane = recovered
        .workspaces
        .values()
        .find_map(|w| w.panes.get(&running))
        .context("persisted pane")
        .unwrap();
    assert_eq!(pane.state, ProcessState::Disconnected);
    assert!(pane.pid.is_none());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn structured_approvals_require_explicit_decisions_and_expire() {
    use wmux_core::ApprovalStatus;
    let mut d = Daemon::start().await;
    let (_, pane) = d.new_workspace("approvals").await;
    let Reply::Created(id) = d
        .call(Request::ApprovalCreate {
            pane: pane.clone(),
            title: "Review operation".into(),
            details: "Explicit custom hook context".into(),
            destructive: true,
            ttl: 60,
        })
        .await
    else {
        panic!()
    };
    assert_eq!(
        d.snapshot().await.state.approvals[0].status,
        ApprovalStatus::Pending
    );
    d.call(Request::ApprovalDecide {
        id: id.clone(),
        approve: false,
    })
    .await;
    assert_eq!(
        d.snapshot().await.state.approvals[0].status,
        ApprovalStatus::Rejected
    );
    assert!(
        wmux_ipc::call(&d.home, Request::ApprovalDecide { id, approve: true })
            .await
            .is_err()
    );
    d.call(Request::ApprovalCreate {
        pane,
        title: "Short expiry".into(),
        details: String::new(),
        destructive: false,
        ttl: 1,
    })
    .await;
    tokio::time::sleep(Duration::from_millis(1400)).await;
    assert_eq!(
        d.snapshot().await.state.approvals[1].status,
        ApprovalStatus::Expired
    );
    d.stop().await;
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn high_output_does_not_prevent_another_pane_from_responding() {
    let mut d = Daemon::start().await;
    let (w, p) = d.new_workspace("output").await;
    let Reply::Created(other) = d
        .call(Request::Split {
            workspace: w,
            pane: None,
            axis: Axis::Columns,
            command: vec![],
            agent: None,
        })
        .await
    else {
        panic!()
    };
    let output = if cfg!(windows) {
        "1..4000 | ForEach-Object { '0123456789' * 20 }\r"
    } else {
        "for i in {1..4000}; do printf '%0200d\\n' 0; done\r"
    };
    d.call(Request::Input {
        pane: p,
        data: output.as_bytes().to_vec(),
    })
    .await;
    let command = if cfg!(windows) {
        "Write-Output ('OTHER_' + 'PANE_437')\r"
    } else {
        "printf 'OTHER_%s\\n' PANE_437\r"
    };
    d.call(Request::Input {
        pane: other.clone(),
        data: command.as_bytes().to_vec(),
    })
    .await;
    wait_text(&d, &other, "OTHER_PANE_437").await;
    assert!(matches!(d.call(Request::Ping).await, Reply::Pong));
    d.stop().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn rapid_client_reconnect_keeps_the_endpoint_available() {
    let mut d = Daemon::start().await;
    let mut clients = tokio::task::JoinSet::new();
    for _ in 0..8 {
        let home = d.home.clone();
        clients.spawn(async move {
            for _ in 0..40 {
                assert!(matches!(
                    wmux_ipc::call(&home, Request::Ping).await.unwrap(),
                    Reply::Pong
                ));
            }
        });
    }
    while let Some(result) = clients.join_next().await {
        result.unwrap();
    }
    d.stop().await;
}
