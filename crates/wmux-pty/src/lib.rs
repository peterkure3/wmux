use anyhow::{Context, Result, ensure};
use portable_pty::{Child, CommandBuilder, MasterPty, PtySize};
use std::io::{Read, Write};
use tokio::sync::mpsc;
use wmux_core::config::Config;
#[cfg(windows)]
mod job;

#[derive(Debug)]
pub enum Event {
    Output(String, Vec<u8>),
    Eof(String),
    Error(String, String),
}
pub struct Pty {
    master: Box<dyn MasterPty + Send>,
    child: Box<dyn Child + Send + Sync>,
    input: mpsc::Sender<Vec<u8>>,
    #[cfg(windows)]
    _job: job::Job,
}
impl Pty {
    pub fn spawn(
        id: String,
        command: &[String],
        cwd: &str,
        config: &Config,
        home: &std::path::Path,
        events: mpsc::Sender<Event>,
        runtime: &tokio::runtime::Handle,
    ) -> Result<Self> {
        ensure!(!command.is_empty(), "empty process command");
        ensure!(
            std::path::Path::new(cwd).is_dir(),
            "working directory does not exist"
        );
        let pair = portable_pty::native_pty_system().openpty(size(80, 24)?)?;
        let mut cmd = CommandBuilder::new(&command[0]);
        cmd.args(&command[1..]);
        cmd.cwd(cwd);
        if let Some(names) = &config.forward_env {
            cmd.env_clear();
            for name in names {
                if let Some(v) = std::env::var_os(name) {
                    cmd.env(name, v);
                }
            }
        }
        cmd.env("TERM", "xterm-256color");
        cmd.env("COLORTERM", "truecolor");
        cmd.env("WMUX_PANE_ID", &id);
        cmd.env("WMUX_HOME", home);
        // Readers/writers are allocated before spawning, so setup failures cannot leak a child.
        let mut reader = pair.master.try_clone_reader()?;
        let writer = pair.master.take_writer()?;
        let mut child = pair
            .slave
            .spawn_command(cmd)
            .with_context(|| format!("cannot launch {}", command[0]))?;
        drop(pair.slave);
        #[cfg(windows)]
        let job = match job::Job::assign(&*child) {
            Ok(j) => j,
            Err(e) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(e);
            }
        };
        let (input, mut rx) = mpsc::channel::<Vec<u8>>(64);
        let writer_events = events.clone();
        let writer_id = id.clone();
        runtime.spawn(async move {
            let mut writer = writer;
            while let Some(data) = rx.recv().await {
                let result = tokio::task::spawn_blocking(move || {
                    let r = writer.write_all(&data).and_then(|_| writer.flush());
                    (writer, r)
                })
                .await;
                match result {
                    Ok((returned, Ok(()))) => writer = returned,
                    Ok((_, Err(e))) => {
                        let _ = writer_events
                            .send(Event::Error(writer_id.clone(), format!("PTY input: {e}")))
                            .await;
                        break;
                    }
                    Err(e) => {
                        let _ = writer_events
                            .send(Event::Error(
                                writer_id.clone(),
                                format!("PTY input worker: {e}"),
                            ))
                            .await;
                        break;
                    }
                }
            }
        });
        let reader_id = id.clone();
        let thread = std::thread::Builder::new()
            .name(format!("pty-{id}"))
            .spawn(move || {
                let mut buf = [0u8; 16384];
                loop {
                    match reader.read(&mut buf) {
                        Ok(0) => break,
                        Ok(n) => {
                            if events
                                .blocking_send(Event::Output(reader_id.clone(), buf[..n].to_vec()))
                                .is_err()
                            {
                                return;
                            }
                        }
                        Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                        // Unix PTY masters report EIO on slave closure.
                        Err(e) if cfg!(unix) && e.raw_os_error() == Some(5) => break,
                        Err(e) => {
                            let _ = events.blocking_send(Event::Error(
                                reader_id.clone(),
                                format!("PTY output: {e}"),
                            ));
                            break;
                        }
                    }
                }
                let _ = events.blocking_send(Event::Eof(reader_id));
            });
        if let Err(e) = thread {
            let _ = child.kill();
            let _ = child.wait();
            return Err(e.into());
        }
        Ok(Self {
            master: pair.master,
            child,
            input,
            #[cfg(windows)]
            _job: job,
        })
    }
    pub fn pid(&self) -> Option<u32> {
        self.child.process_id()
    }
    pub fn input(&self, data: Vec<u8>) -> Result<()> {
        ensure!(data.len() <= 65536, "input chunk exceeds 64 KiB");
        self.input
            .try_send(data)
            .context("pane input queue full or closed")?;
        Ok(())
    }
    pub fn resize(&self, cols: u16, rows: u16) -> Result<()> {
        self.master.resize(size(cols, rows)?)
    }
    pub fn poll(&mut self) -> Result<Option<u32>> {
        Ok(self.child.try_wait()?.map(|s| s.exit_code()))
    }
    pub fn terminate(&mut self) -> Result<()> {
        if self.child.try_wait()?.is_none() {
            #[cfg(unix)]
            {
                // SAFETY: Only signal groups obtained from this live PTY/root,
                // never the daemon's own process group. No shell interpolation.
                unsafe {
                    let own = libc::getpgrp();
                    if let Some(group) = self.master.process_group_leader()
                        && group > 0
                        && group != own
                    {
                        libc::kill(-group, libc::SIGKILL);
                    }
                    if let Some(pid) = self.child.process_id() {
                        if pid as i32 != own {
                            libc::kill(-(pid as i32), libc::SIGKILL);
                        }
                        if libc::kill(pid as i32, libc::SIGKILL) != 0
                            && std::io::Error::last_os_error().raw_os_error() != Some(libc::ESRCH)
                        {
                            return Err(std::io::Error::last_os_error().into());
                        }
                    }
                }
            }
            #[cfg(windows)]
            self._job.terminate()?;
            self.child.wait()?;
        }
        Ok(())
    }
}
impl Drop for Pty {
    fn drop(&mut self) {
        let _ = self.terminate();
    }
}
fn size(cols: u16, rows: u16) -> Result<PtySize> {
    ensure!(
        (1..=400).contains(&cols) && (1..=150).contains(&rows),
        "PTY dimensions must be 1..400 by 1..150"
    );
    Ok(PtySize {
        cols,
        rows,
        pixel_width: 0,
        pixel_height: 0,
    })
}
