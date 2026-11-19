//! Readiness-driven Unix PTY transport. No blocking reader or writer worker is
//! allocated per pane. Only the native PTY lifecycle/ioctls remain synchronous.
use crate::Event;
use anyhow::{Context, Result};
use std::{
    io,
    os::fd::{AsRawFd, FromRawFd, OwnedFd},
    sync::Arc,
};
use tokio::{io::unix::AsyncFd, runtime::Handle, sync::mpsc, task::AbortHandle};

pub(super) struct Tasks([AbortHandle; 2]);
impl Drop for Tasks {
    fn drop(&mut self) {
        // Also cancels a reader parked on a full output queue or a writer
        // parked on a full kernel input buffer. Neither may retain its fd.
        for task in &self.0 {
            task.abort();
        }
    }
}

pub(super) fn prepare(
    master: &dyn portable_pty::MasterPty,
    runtime: &Handle,
) -> Result<Arc<AsyncFd<OwnedFd>>> {
    let fd = master
        .as_raw_fd()
        .context("Unix PTY has no native descriptor")?;
    // SAFETY: The borrowed master fd remains live throughout duplication. The
    // new descriptor is uniquely owned, close-on-exec, and closed on all errors.
    // O_NONBLOCK applies to the shared master description; no portable-pty
    // reader/writer clone is used on Unix, so none can mistake EAGAIN for EOF.
    let descriptor = unsafe {
        let copy = libc::fcntl(fd, libc::F_DUPFD_CLOEXEC, 0);
        if copy < 0 {
            return Err(io::Error::last_os_error().into());
        }
        let owned = OwnedFd::from_raw_fd(copy);
        let flags = libc::fcntl(copy, libc::F_GETFL);
        if flags < 0 || libc::fcntl(copy, libc::F_SETFL, flags | libc::O_NONBLOCK) < 0 {
            return Err(io::Error::last_os_error().into());
        }
        owned
    };
    // Registration is prepared before spawning a child, including when called
    // from the daemon's blocking actor rather than a Tokio worker thread.
    let _entered = runtime.enter();
    // SAFETY: OwnedFd uniquely owns the duplicate and its numeric descriptor
    // cannot change or close until AsyncFd drops it. No raw-fd owner escapes.
    Ok(Arc::new(unsafe { AsyncFd::register(descriptor) }?))
}

async fn read(fd: &AsyncFd<OwnedFd>, bytes: &mut [u8]) -> io::Result<usize> {
    loop {
        let mut ready = fd.readable().await?;
        match ready.try_io(|fd| {
            // SAFETY: The live owned fd and initialized, writable slice remain
            // valid for this nonblocking syscall; no reference escapes it.
            let n = unsafe {
                libc::read(
                    fd.get_ref().as_raw_fd(),
                    bytes.as_mut_ptr().cast(),
                    bytes.len(),
                )
            };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }) {
            Ok(Err(e)) if e.kind() == io::ErrorKind::Interrupted => continue,
            Ok(result) => return result,
            Err(_) => (), // WouldBlock clears cached readiness; await the next event.
        }
    }
}
async fn write(fd: &AsyncFd<OwnedFd>, mut bytes: &[u8]) -> io::Result<()> {
    while !bytes.is_empty() {
        let mut ready = fd.writable().await?;
        match ready.try_io(|fd| {
            // SAFETY: The live fd and readable slice are valid for this syscall.
            let n = unsafe {
                libc::write(fd.get_ref().as_raw_fd(), bytes.as_ptr().cast(), bytes.len())
            };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }) {
            Ok(Ok(0)) => return Err(io::ErrorKind::WriteZero.into()),
            Ok(Ok(n)) => bytes = &bytes[n..],
            Ok(Err(e)) if e.kind() == io::ErrorKind::Interrupted => (),
            Ok(Err(e)) => return Err(e),
            Err(_) => (),
        }
    }
    Ok(())
}
pub(super) fn start(
    fd: Arc<AsyncFd<OwnedFd>>,
    id: String,
    events: mpsc::Sender<Event>,
    mut input: mpsc::Receiver<Vec<u8>>,
    runtime: &Handle,
) -> Tasks {
    let reader_fd = fd.clone();
    let reader_id = id.clone();
    let reader_events = events.clone();
    let reader = runtime.spawn(async move {
        let mut buffer = [0u8; 16384];
        loop {
            match read(&reader_fd, &mut buffer).await {
                Ok(0) => break,
                Ok(n) => {
                    if reader_events
                        .send(Event::Output(reader_id.clone(), buffer[..n].to_vec()))
                        .await
                        .is_err()
                    {
                        return;
                    }
                }
                // Linux PTY masters signal closure of the last slave with EIO.
                Err(e) if e.raw_os_error() == Some(libc::EIO) => break,
                Err(e) => {
                    let _ = reader_events
                        .send(Event::Error(reader_id.clone(), format!("PTY output: {e}")))
                        .await;
                    break;
                }
            }
        }
        let _ = reader_events.send(Event::Eof(reader_id)).await;
    });
    let writer = runtime.spawn(async move {
        while let Some(bytes) = input.recv().await {
            if let Err(e) = write(&fd, &bytes).await {
                let _ = events
                    .send(Event::Error(id.clone(), format!("PTY input: {e}")))
                    .await;
                break;
            }
        }
    });
    Tasks([reader.abort_handle(), writer.abort_handle()])
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use crate::Pty;
    use std::{collections::BTreeMap, path::Path, time::Duration};
    use wmux_core::config::Config;

    fn count(path: &str) -> usize {
        std::fs::read_dir(path).unwrap().count()
    }
    fn spawn(id: &str, script: &str, events: mpsc::Sender<Event>) -> Pty {
        Pty::spawn(
            id.into(),
            &["/bin/sh".into(), "-c".into(), script.into()],
            "/tmp",
            &Config::default(),
            Path::new("/tmp"),
            events,
            &Handle::current(),
        )
        .unwrap()
    }
    async fn ready(rx: &mut mpsc::Receiver<Event>, expected: usize) {
        let mut output = BTreeMap::<String, Vec<u8>>::new();
        tokio::time::timeout(Duration::from_secs(10), async {
            while output
                .values()
                .filter(|bytes| bytes.windows(5).any(|b| b == b"READY"))
                .count()
                < expected
            {
                match rx.recv().await.unwrap() {
                    Event::Output(id, bytes) => output.entry(id).or_default().extend(bytes),
                    other => panic!("unexpected startup event: {other:?}"),
                }
            }
        })
        .await
        .expect("PTY startup did not progress");
    }
    #[tokio::test(flavor = "current_thread")]
    async fn real_ptys_share_reactor_and_cancel_under_backpressure() {
        let descriptors = count("/proc/self/fd");
        let threads = count("/proc/self/task");
        // One output slot forces genuine reader backpressure, with sixteen
        // real PTYs and only the single-thread Tokio test reactor servicing I/O.
        let (tx, mut rx) = mpsc::channel(1);
        let mut panes = (0..16)
            .map(|i| {
                spawn(
                    &format!("pane-{i}"),
                    "stty raw -echo; printf READY; exec cat",
                    tx.clone(),
                )
            })
            .collect::<Vec<_>>();
        ready(&mut rx, 16).await;
        assert_eq!(
            count("/proc/self/task"),
            threads,
            "PTY I/O allocated OS threads"
        );
        for pane in &panes {
            let fd = pane.master.as_raw_fd().unwrap();
            // SAFETY: The PTY owns this live master fd for both queries.
            assert_ne!(
                unsafe { libc::fcntl(fd, libc::F_GETFL) } & libc::O_NONBLOCK,
                0
            );
        }
        // A full-size binary chunk requires partial kernel writes/reads and
        // must arrive byte-for-byte without UTF-8 or line-ending conversion.
        let payload = (0..65536).map(|i| (i % 256) as u8).collect::<Vec<_>>();
        panes[0].input(payload.clone()).unwrap();
        let mut output = Vec::new();
        tokio::time::timeout(Duration::from_secs(10), async {
            while output.len() < payload.len() {
                match rx.recv().await.unwrap() {
                    Event::Output(id, bytes) if id == "pane-0" => output.extend(bytes),
                    other => panic!("unexpected echo event: {other:?}"),
                }
            }
        })
        .await
        .expect("partial writes stalled");
        assert_eq!(output, payload);
        let mut handles = Vec::new();
        for pane in &panes {
            handles.extend(pane._io.0.iter().cloned());
        }
        // Cancel a reader blocked on a full event queue.
        panes[1].input(vec![b'x'; 65536]).unwrap();
        tokio::time::sleep(Duration::from_millis(20)).await;
        assert_eq!(rx.len(), 1);
        for pane in &mut panes {
            pane.terminate().unwrap();
        }
        drop(panes);
        drop(rx);
        drop(tx);
        tokio::time::timeout(Duration::from_secs(2), async {
            while handles.iter().any(|handle| !handle.is_finished()) {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("cancelled reader retained its descriptor");

        // A child that never reads fills its kernel input buffer. The writer
        // must await readiness without blocking the same reactor's timer.
        let (tx, mut rx) = mpsc::channel(1);
        let mut stalled = spawn("stalled", "stty raw -echo; printf READY; sleep 60", tx);
        ready(&mut rx, 1).await;
        for _ in 0..64 {
            stalled.input(vec![b'x'; 65536]).unwrap();
        }
        assert!(
            stalled.input(vec![b'x']).is_err(),
            "input queue was unbounded"
        );
        tokio::time::timeout(
            Duration::from_secs(1),
            tokio::time::sleep(Duration::from_millis(20)),
        )
        .await
        .expect("stalled PTY write blocked the reactor");
        let handles = stalled._io.0.clone();
        stalled.terminate().unwrap();
        drop(stalled);
        drop(rx);
        tokio::time::timeout(Duration::from_secs(2), async {
            while handles.iter().any(|handle| !handle.is_finished()) {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("cancelled writer retained its descriptor");
        assert!(
            count("/proc/self/fd") <= descriptors,
            "PTY descriptors leaked"
        );
        assert_eq!(count("/proc/self/task"), threads);
    }
}
