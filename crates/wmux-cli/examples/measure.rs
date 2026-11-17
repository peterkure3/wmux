//! Reproducible measurements; not a claim about physical display latency.
use anyhow::{Result, ensure};
use std::{
    collections::BTreeMap,
    path::Path,
    time::{Duration, Instant},
};
use wmux_core::{
    config::Config,
    layout::{Node, Rect},
};
use wmux_ipc::{Reply, Request, Snapshot};
fn micros(start: Instant, n: u32) -> f64 {
    start.elapsed().as_secs_f64() * 1e6 / f64::from(n)
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
        _ => anyhow::bail!("invalid response"),
    }
}
#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<()> {
    let home = wmux_core::config::home();
    let home = std::fs::canonicalize(home)?;
    let mut report = serde_json::Map::new();
    report.insert(
        "platform".into(),
        format!("{}-{}", std::env::consts::OS, std::env::consts::ARCH).into(),
    );
    report.insert(
        "profile".into(),
        if cfg!(debug_assertions) {
            "debug"
        } else {
            "release"
        }
        .into(),
    );
    let mut t = wmux_terminal::Terminal::new(40, 120, 2000);
    let line = b"\x1b[32mCompiling real terminal output\x1b[0m\r\n";
    let start = Instant::now();
    for _ in 0..100000 {
        t.process(line);
    }
    report.insert(
        "parse_mib_per_second".into(),
        ((line.len() * 100000) as f64 / (1024.0 * 1024.0) / start.elapsed().as_secs_f64()).into(),
    );
    let start = Instant::now();
    for _ in 0..1000 {
        std::hint::black_box(t.snapshot(0));
    }
    report.insert("screen_snapshot_us".into(), micros(start, 1000).into());
    let n = Node::preset(&["a".into(), "b".into(), "c".into(), "d".into()], "grid").unwrap();
    let start = Instant::now();
    for _ in 0..100000 {
        std::hint::black_box(n.rects(Rect {
            x: 0,
            y: 0,
            width: 120,
            height: 40,
        }));
    }
    report.insert("four_pane_layout_us".into(), micros(start, 100000).into());
    let start = Instant::now();
    for _ in 0..50 {
        wmux_ipc::call(&home, Request::Ping).await?;
    }
    report.insert("ipc_ping_mean_us".into(), micros(start, 50).into());
    let start = Instant::now();
    let Reply::Created(w) = wmux_ipc::call(
        &home,
        Request::New {
            name: format!("measure-{}", wmux_core::id()),
            cwd: std::env::current_dir()?.to_string_lossy().into(),
            command: vec![],
        },
    )
    .await?
    else {
        anyhow::bail!("invalid create response")
    };
    report.insert(
        "workspace_spawn_ms".into(),
        (start.elapsed().as_secs_f64() * 1000.0).into(),
    );
    let s = snapshot(&home).await?;
    let pane = s.state.workspaces[&w].focused.clone().unwrap();
    // Wait for the real shell prompt/startup before timing input.
    tokio::time::sleep(Duration::from_secs(2)).await;
    let mut latencies = vec![];
    for i in 0..10 {
        let marker = format!("WMUX_MEASURE_{i}");
        let command = if cfg!(windows) {
            format!("Write-Output ('WMUX_MEASURE_' + '{i}')\r")
        } else {
            format!("printf 'WMUX_MEASURE_%s\\n' {i}\r")
        };
        let start = Instant::now();
        wmux_ipc::call(
            &home,
            Request::Input {
                pane: pane.clone(),
                data: command.into_bytes(),
            },
        )
        .await?;
        loop {
            let s = snapshot(&home).await?;
            if s.screens
                .get(&pane)
                .is_some_and(|s| s.text().contains(&marker))
            {
                break;
            }
            ensure!(
                start.elapsed() < Duration::from_secs(10),
                "input/output measurement timed out"
            );
            tokio::time::sleep(Duration::from_millis(2)).await;
        }
        latencies.push(start.elapsed().as_secs_f64() * 1000.0);
    }
    latencies.sort_by(f64::total_cmp);
    report.insert(
        "input_to_cell_median_ms".into(),
        latencies[latencies.len() / 2].into(),
    );
    report.insert(
        "input_to_cell_max_ms".into(),
        latencies[latencies.len() - 1].into(),
    );
    let start = Instant::now();
    for i in 0..20 {
        wmux_ipc::call(
            &home,
            Request::Resize {
                pane: pane.clone(),
                cols: 80 + i,
                rows: 24,
            },
        )
        .await?;
    }
    report.insert("resize_mean_us".into(), micros(start, 20).into());
    let start = Instant::now();
    for _ in 0..20 {
        let mut stream = wmux_ipc::connect(&home).await?;
        wmux_ipc::write(
            &mut stream,
            &wmux_ipc::Envelope {
                version: wmux_ipc::VERSION,
                request: Request::Subscribe,
            },
        )
        .await?;
        wmux_ipc::read::<_, wmux_ipc::Response>(&mut stream)
            .await?
            .result()?;
    }
    report.insert("attach_snapshot_mean_us".into(), micros(start, 20).into());
    let s = snapshot(&home).await?;
    let start = Instant::now();
    for _ in 0..100 {
        std::hint::black_box(wmux_tui::render_buffer(
            s.clone(),
            Some(w.clone()),
            Config::default(),
            120,
            40,
        )?);
    }
    report.insert(
        "ratatui_headless_render_us".into(),
        micros(start, 100).into(),
    );
    wmux_ipc::call(&home, Request::Kill { workspace: w }).await?;

    let probe = std::env::current_exe()?.with_file_name(if cfg!(windows) {
        "pty_probe.exe"
    } else {
        "pty_probe"
    });
    ensure!(
        probe.is_file(),
        "build examples/pty_probe with the measurement harness"
    );
    let Reply::Created(probe_workspace) = wmux_ipc::call(
        &home,
        Request::New {
            name: format!("pty-probe-{}", wmux_core::id()),
            cwd: std::env::current_dir()?.to_string_lossy().into(),
            command: vec![probe.to_string_lossy().into_owned()],
        },
    )
    .await?
    else {
        anyhow::bail!("invalid probe create response")
    };
    let s = snapshot(&home).await?;
    let probe_pane = s.state.workspaces[&probe_workspace]
        .focused
        .clone()
        .unwrap();
    let started = Instant::now();
    loop {
        if snapshot(&home)
            .await?
            .screens
            .get(&probe_pane)
            .is_some_and(|s| s.text().contains("WMUX_PROBE_READY"))
        {
            break;
        }
        ensure!(
            started.elapsed() < Duration::from_secs(10),
            "PTY fixture startup timed out"
        );
        tokio::time::sleep(Duration::from_millis(2)).await;
    }
    let mut echoes = Vec::new();
    for i in 0..10 {
        let start = Instant::now();
        wmux_ipc::call(
            &home,
            Request::Input {
                pane: probe_pane.clone(),
                data: format!("{i}\r").into_bytes(),
            },
        )
        .await?;
        let marker = format!("WMUX_PROBE_{i}");
        loop {
            if snapshot(&home)
                .await?
                .screens
                .get(&probe_pane)
                .is_some_and(|s| s.text().contains(&marker))
            {
                break;
            }
            ensure!(
                start.elapsed() < Duration::from_secs(10),
                "PTY echo timed out"
            );
            tokio::time::sleep(Duration::from_millis(2)).await;
        }
        echoes.push(start.elapsed().as_secs_f64() * 1000.0);
    }
    echoes.sort_by(f64::total_cmp);
    report.insert("pty_echo_median_ms".into(), echoes[echoes.len() / 2].into());
    report.insert("pty_echo_max_ms".into(), echoes[echoes.len() - 1].into());
    wmux_ipc::call(
        &home,
        Request::Kill {
            workspace: probe_workspace,
        },
    )
    .await?;

    println!("{}", serde_json::to_string_pretty(&report)?);
    Ok(())
}
