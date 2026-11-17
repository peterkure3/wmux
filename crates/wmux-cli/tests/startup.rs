use std::{collections::BTreeMap, path::PathBuf, process::Command};
use wmux_ipc::{Reply, Request};
struct Cleanup {
    home: PathBuf,
}
impl Drop for Cleanup {
    fn drop(&mut self) {
        let _ = Command::new(env!("CARGO_BIN_EXE_wmux"))
            .args([
                "--home",
                &self.home.to_string_lossy(),
                "daemon",
                "stop",
                "--force",
            ])
            .output();
        let _ = std::fs::remove_dir_all(&self.home);
    }
}
#[tokio::test(flavor = "current_thread")]
async fn on_demand_startup_reports_to_caller_and_survives_cli_exit() {
    let home = std::env::temp_dir().join(format!("wmux-cli-start-{}", wmux_core::id()));
    std::fs::create_dir_all(&home).unwrap();
    let shell = if cfg!(windows) {
        "shell = [\"cmd.exe\", \"/d\", \"/q\"]"
    } else {
        "shell = [\"/bin/bash\", \"--noprofile\", \"--norc\"]"
    };
    std::fs::write(home.join("config.toml"), shell).unwrap();
    let cleanup = Cleanup { home: home.clone() };
    let run = |name: &str| {
        Command::new(env!("CARGO_BIN_EXE_wmux"))
            .args(["--home", &home.to_string_lossy(), "new", name, "--detach"])
            .output()
            .unwrap()
    };
    let first = run("first");
    assert!(
        first.status.success(),
        "{}",
        String::from_utf8_lossy(&first.stderr)
    );
    assert!(String::from_utf8_lossy(&first.stderr).contains("wmuxd started in background"));
    assert!(matches!(
        wmux_ipc::call(&home, Request::Ping).await.unwrap(),
        Reply::Pong
    ));
    let second = run("second");
    assert!(
        second.status.success(),
        "{}",
        String::from_utf8_lossy(&second.stderr)
    );
    assert!(!String::from_utf8_lossy(&second.stderr).contains("started in background"));
    let Reply::Snapshot(snapshot) = wmux_ipc::call(
        &home,
        Request::Get {
            scrollback: BTreeMap::new(),
        },
    )
    .await
    .unwrap() else {
        panic!()
    };
    assert_eq!(snapshot.state.workspaces.len(), 2);
    let pane = snapshot
        .state
        .workspaces
        .values()
        .next()
        .unwrap()
        .focused
        .clone()
        .unwrap();
    // Exercise the supported hook JSON contracts through real CLI processes.
    // No provider executable or network request is used by this contract test.
    for (provider, approve) in [("claude", true), ("codex", false)] {
        use std::io::Write;
        let mut child = Command::new(env!("CARGO_BIN_EXE_wmux"))
            .args([
                "--home",
                &home.to_string_lossy(),
                "agent",
                "hook",
                provider,
                "--pane",
                &pane,
                "--ttl",
                "10",
            ])
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .unwrap();
        child.stdin.take().unwrap().write_all(br#"{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"git status","api_key":"do-not-store"}}"#).unwrap();
        let approval = tokio::time::timeout(std::time::Duration::from_secs(8), async {
            loop {
                let Reply::Snapshot(s) = wmux_ipc::call(
                    &home,
                    Request::Get {
                        scrollback: BTreeMap::new(),
                    },
                )
                .await
                .unwrap() else {
                    panic!()
                };
                if let Some(a) = s
                    .state
                    .approvals
                    .iter()
                    .find(|a| a.status == wmux_core::ApprovalStatus::Pending)
                {
                    assert!(a.details.contains("git status"));
                    assert!(!a.details.contains("do-not-store"));
                    break a.id.clone();
                }
                tokio::time::sleep(std::time::Duration::from_millis(25)).await;
            }
        })
        .await
        .unwrap();
        wmux_ipc::call(
            &home,
            Request::ApprovalDecide {
                id: approval,
                approve,
            },
        )
        .await
        .unwrap();
        let output = tokio::task::spawn_blocking(move || child.wait_with_output().unwrap())
            .await
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        let json: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
        assert_eq!(
            json["hookSpecificOutput"]["decision"]["behavior"],
            if approve { "allow" } else { "deny" }
        );
    }
    // A valid permission event whose daemon/pane is unavailable must explicitly
    // deny, never fall through to an implicit allow or start another daemon.
    let output = tokio::task::spawn_blocking({ let home = home.clone(); move || {
        use std::io::Write;
        let mut child = Command::new(env!("CARGO_BIN_EXE_wmux"))
            .args(["--home", &home.to_string_lossy(), "agent", "hook", "claude", "--pane", "missing"])
            .stdin(std::process::Stdio::piped()).stdout(std::process::Stdio::piped()).spawn().unwrap();
        child.stdin.take().unwrap().write_all(br#"{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"echo safe"}}"#).unwrap();
        child.wait_with_output().unwrap()
    }}).await.unwrap();
    let json: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(json["hookSpecificOutput"]["decision"]["behavior"], "deny");
    drop(cleanup);
}
