use anyhow::{Context, Result, bail, ensure};
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use std::{collections::BTreeMap, path::Path};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use wmux_core::{AgentActivity, State, Task, layout::Axis};
use wmux_terminal::Screen;
pub const VERSION: u16 = 2;
pub const MAX_FRAME: usize = 32 * 1024 * 1024;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum Request {
    Ping,
    /// Open a persistent command connection. Write leases last until disconnect.
    Control,
    Search {
        pane: String,
        query: String,
        offset: usize,
        backwards: bool,
    },
    Get {
        scrollback: BTreeMap<String, usize>,
    },
    Subscribe,
    New {
        name: String,
        cwd: String,
        command: Vec<String>,
    },
    Kill {
        workspace: String,
    },
    Split {
        workspace: String,
        pane: Option<String>,
        axis: Axis,
        command: Vec<String>,
        agent: Option<String>,
    },
    Close {
        workspace: String,
        pane: String,
    },
    Focus {
        workspace: String,
        pane: String,
    },
    Zoom {
        workspace: String,
    },
    Resize {
        pane: String,
        cols: u16,
        rows: u16,
    },
    Adjust {
        workspace: String,
        pane: String,
        delta: i16,
    },
    Swap {
        workspace: String,
        first: String,
        second: String,
    },
    Rename {
        workspace: String,
        pane: Option<String>,
        title: String,
    },
    Layout {
        workspace: String,
        name: String,
    },
    Input {
        pane: String,
        data: Vec<u8>,
    },
    TaskCreate {
        title: String,
        description: String,
        workspace: String,
    },
    TaskAssign {
        task: String,
        pane: String,
    },
    TaskStatus {
        task: String,
        status: String,
    },
    AgentEvent {
        pane: String,
        activity: AgentActivity,
        message: Option<String>,
    },
    ApprovalCreate {
        pane: String,
        title: String,
        details: String,
        destructive: bool,
        ttl: u64,
    },
    ApprovalDecide {
        id: String,
        approve: bool,
    },
    Shutdown,
}
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct Snapshot {
    pub state: State,
    pub screens: BTreeMap<String, Screen>,
    pub revision: u64,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum Reply {
    Pong,
    Search(Option<usize>),
    Ok,
    Created(String),
    Snapshot(Snapshot),
    Task(Task),
}
#[derive(Debug, Serialize, Deserialize)]
pub struct Envelope {
    pub version: u16,
    pub request: Request,
}
#[derive(Debug, Serialize, Deserialize)]
pub struct Response {
    pub version: u16,
    pub reply: Option<Reply>,
    pub error: Option<String>,
}
impl Response {
    pub fn new(result: Result<Reply>) -> Self {
        match result {
            Ok(reply) => Self {
                version: VERSION,
                reply: Some(reply),
                error: None,
            },
            Err(e) => Self {
                version: VERSION,
                reply: None,
                error: Some(e.to_string()),
            },
        }
    }
    pub fn result(self) -> Result<Reply> {
        ensure!(self.version == VERSION, "incompatible daemon protocol");
        if let Some(e) = self.error {
            bail!("{e}")
        }
        self.reply.context("empty daemon response")
    }
}
pub async fn read<S: AsyncRead + Unpin, T: DeserializeOwned>(s: &mut S) -> Result<T> {
    let n = s.read_u32_le().await? as usize;
    ensure!(n > 0 && n <= MAX_FRAME, "IPC frame exceeds limit");
    let mut buf = vec![0; n];
    s.read_exact(&mut buf).await?;
    Ok(serde_json::from_slice(&buf)?)
}
pub async fn write<S: AsyncWrite + Unpin, T: Serialize>(s: &mut S, value: &T) -> Result<()> {
    let buf = serde_json::to_vec(value)?;
    ensure!(buf.len() <= MAX_FRAME, "IPC frame exceeds limit");
    s.write_u32_le(buf.len() as u32).await?;
    s.write_all(&buf).await?;
    s.flush().await?;
    Ok(())
}
pub trait Stream: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Stream for T {}
pub type Connection = Box<dyn Stream>;
pub fn endpoint(home: &Path) -> Result<String> {
    #[cfg(unix)]
    {
        let p = home.join("daemon.sock");
        let s = p.to_str().context("IPC path must be UTF-8")?;
        ensure!(s.len() < 104, "WMUX_HOME is too long for a Unix socket");
        Ok(s.into())
    }
    #[cfg(windows)]
    {
        let sid = wmux_security::windows::user_sid()?;
        let canonical = std::fs::canonicalize(home).unwrap_or_else(|_| home.to_path_buf());
        let mut hash = 0xcbf29ce484222325u64;
        for b in canonical.to_string_lossy().to_lowercase().bytes() {
            hash = (hash ^ u64::from(b)).wrapping_mul(0x100000001b3);
        }
        Ok(format!(r"\\.\pipe\wmux-rust-{sid}-{hash:x}"))
    }
}
pub async fn connect(home: &Path) -> Result<Connection> {
    let endpoint = endpoint(home)?;
    #[cfg(unix)]
    {
        Ok(Box::new(tokio::net::UnixStream::connect(endpoint).await?))
    }
    #[cfg(windows)]
    {
        use tokio::net::windows::named_pipe::ClientOptions;
        for _ in 0..100 {
            match ClientOptions::new().open(&endpoint) {
                Ok(s) => return Ok(Box::new(s)),
                Err(e) if e.raw_os_error() == Some(231) => {
                    tokio::time::sleep(std::time::Duration::from_millis(20)).await
                }
                Err(e) => return Err(e.into()),
            }
        }
        bail!("daemon pipe stayed busy")
    }
}
pub async fn call(home: &Path, request: Request) -> Result<Reply> {
    tokio::time::timeout(std::time::Duration::from_secs(15), async {
        let mut s = connect(home)
            .await
            .context("cannot connect to wmuxd; start the Rust daemon first")?;
        write(
            &mut s,
            &Envelope {
                version: VERSION,
                request,
            },
        )
        .await?;
        read::<_, Response>(&mut s).await?.result()
    })
    .await
    .context("daemon request timed out")?
}

/// A command stream owns any workspaces it modifies until the stream closes.
/// Observers use Subscribe/Get and never acquire write ownership.
pub struct Control {
    stream: Connection,
    healthy: bool,
}
impl Control {
    pub async fn connect(home: &Path) -> Result<Self> {
        let mut client = Self {
            stream: connect(home).await?,
            healthy: true,
        };
        client.call(Request::Control).await?;
        Ok(client)
    }
    pub fn is_connected(&self) -> bool {
        self.healthy
    }
    pub async fn call(&mut self, request: Request) -> Result<Reply> {
        let response = tokio::time::timeout(std::time::Duration::from_secs(15), async {
            write(
                &mut self.stream,
                &Envelope {
                    version: VERSION,
                    request,
                },
            )
            .await?;
            read::<_, Response>(&mut self.stream).await
        })
        .await
        .context("daemon command timed out")
        .and_then(|r| r);
        match response {
            Ok(response) => response.result(),
            Err(error) => {
                self.healthy = false;
                Err(error)
            }
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn bounded_framing_and_version() {
        let (mut a, mut b) = tokio::io::duplex(4096);
        write(
            &mut a,
            &Envelope {
                version: VERSION,
                request: Request::Ping,
            },
        )
        .await
        .unwrap();
        assert_eq!(read::<_, Envelope>(&mut b).await.unwrap().version, VERSION);
        a.write_u32_le((MAX_FRAME + 1) as u32).await.unwrap();
        assert!(read::<_, Envelope>(&mut b).await.is_err());
        assert!(
            Response {
                version: 99,
                reply: Some(Reply::Ok),
                error: None
            }
            .result()
            .is_err()
        );
    }
}
