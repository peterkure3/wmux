use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use std::{
    path::Path,
    process::{Command, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Info {
    pub root: String,
    pub branch: String,
    pub changed: usize,
    pub worktrees: usize,
}
fn run(cwd: &Path, args: &[&str]) -> Result<Vec<u8>> {
    use std::io::Read;
    let mut child = Command::new("git")
        .args(["-c", "core.fsmonitor=false"])
        .arg("-C")
        .arg(cwd)
        .args(args)
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("GIT_PAGER", "cat")
        .env("GIT_OPTIONAL_LOCKS", "0")
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .context("cannot execute git")?;
    let exceeded = Arc::new(AtomicBool::new(false));
    let read = |stream: Box<dyn Read + Send>, limit: usize, exceeded: Arc<AtomicBool>| {
        std::thread::spawn(move || {
            let mut bytes = Vec::new();
            let result = stream.take((limit + 1) as u64).read_to_end(&mut bytes);
            if bytes.len() > limit {
                exceeded.store(true, Ordering::Release);
            }
            result.map(|_| bytes)
        })
    };
    let stdout = read(
        Box::new(child.stdout.take().unwrap()),
        8 * 1024 * 1024,
        exceeded.clone(),
    );
    let stderr = read(
        Box::new(child.stderr.take().unwrap()),
        65536,
        exceeded.clone(),
    );
    let deadline = Instant::now() + Duration::from_secs(20);
    let mut timed_out = false;
    let status = loop {
        if let Some(status) = child.try_wait()? {
            break status;
        }
        if exceeded.load(Ordering::Acquire) || Instant::now() >= deadline {
            timed_out = Instant::now() >= deadline;
            let _ = child.kill();
            break child.wait()?;
        }
        std::thread::sleep(Duration::from_millis(5));
    };
    let out = stdout
        .join()
        .map_err(|_| anyhow::anyhow!("git stdout reader failed"))??;
    let err = stderr
        .join()
        .map_err(|_| anyhow::anyhow!("git stderr reader failed"))??;
    ensure!(!timed_out, "git command exceeded 20 seconds");
    ensure!(
        !exceeded.load(Ordering::Acquire),
        "git output exceeds limit (stdout 8 MiB, stderr 64 KiB)"
    );
    ensure!(
        status.success(),
        "git: {}",
        String::from_utf8_lossy(&err).trim()
    );
    Ok(out)
}
pub fn info(cwd: &Path) -> Result<Info> {
    let root = String::from_utf8(run(cwd, &["rev-parse", "--show-toplevel"])?)?
        .trim()
        .to_string();
    let branch = String::from_utf8(run(cwd, &["rev-parse", "--abbrev-ref", "HEAD"])?)?
        .trim()
        .to_string();
    let status = run(
        cwd,
        &["status", "--porcelain=v1", "-z", "--untracked-files=normal"],
    )?;
    // Rename records have an extra NUL-delimited source path.
    let mut changed = 0;
    let mut entries = status.split(|b| *b == 0);
    while let Some(entry) = entries.next() {
        if entry.is_empty() {
            continue;
        }
        changed += 1;
        if entry[0] == b'R' || entry[0] == b'C' || entry.get(1) == Some(&b'R') {
            entries.next();
        }
    }
    let trees = run(cwd, &["worktree", "list", "--porcelain"])?;
    let worktrees = String::from_utf8_lossy(&trees)
        .lines()
        .filter(|s| s.starts_with("worktree "))
        .count();
    Ok(Info {
        root,
        branch,
        changed,
        worktrees,
    })
}
pub fn diff(cwd: &Path) -> Result<String> {
    diff_mode(cwd, false)
}
pub fn staged_diff(cwd: &Path) -> Result<String> {
    diff_mode(cwd, true)
}
fn diff_mode(cwd: &Path, staged: bool) -> Result<String> {
    let mut args = vec![
        "--no-pager",
        "diff",
        "--no-color",
        "--no-ext-diff",
        "--no-textconv",
    ];
    if staged {
        args.push("--cached");
    }
    args.extend(["--", "."]);
    Ok(String::from_utf8_lossy(&run(cwd, &args)?).into_owned())
}
pub fn untracked(cwd: &Path) -> Result<Vec<String>> {
    let root = run(cwd, &["rev-parse", "--show-toplevel"])?;
    let root = String::from_utf8(root)?;
    let paths = run(
        Path::new(root.trim()),
        &["ls-files", "--others", "--exclude-standard", "-z"],
    )?;
    Ok(paths
        .split(|b| *b == 0)
        .filter(|s| !s.is_empty())
        .map(|s| String::from_utf8_lossy(s).into_owned())
        .collect())
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Preview {
    pub path: String,
    pub text: String,
    pub truncated: bool,
}
/// Explicit file previews are confined to the canonical repository root. A
/// symlink cannot redirect a preview outside that root; output is capped at 64K.
pub fn preview(cwd: &Path, path: &Path) -> Result<Preview> {
    use std::io::Read;
    let root = run(cwd, &["rev-parse", "--show-toplevel"])?;
    let root = Path::new(String::from_utf8(root)?.trim()).canonicalize()?;
    ensure!(
        !path.is_absolute(),
        "preview path must be relative to repository root"
    );
    let file = root.join(path).canonicalize()?;
    ensure!(file.starts_with(&root), "preview path escapes repository");
    ensure!(file.is_file(), "preview needs a regular file");
    let mut bytes = Vec::new();
    std::fs::File::open(&file)?
        .take(65537)
        .read_to_end(&mut bytes)?;
    let truncated = bytes.len() > 65536;
    bytes.truncate(65536);
    let text = if bytes.contains(&0) {
        "[binary file]".into()
    } else {
        String::from_utf8_lossy(&bytes).into_owned()
    };
    Ok(Preview {
        path: path.to_string_lossy().into_owned(),
        text,
        truncated,
    })
}
pub fn create_worktree(cwd: &Path, path: &Path, branch: &str) -> Result<()> {
    ensure!(
        !branch.starts_with('-') && !branch.is_empty(),
        "invalid branch"
    );
    let path = path.to_str().context("worktree path must be UTF-8")?;
    run(cwd, &["worktree", "add", "-b", branch, "--", path])?;
    Ok(())
}
pub fn remove_worktree(cwd: &Path, path: &Path) -> Result<()> {
    let canonical = path.canonicalize()?;
    ensure!(
        !canonical.join(".git").is_dir(),
        "refusing to remove a primary checkout"
    );
    // Git worktree remove itself refuses dirty/untracked worktrees; never pass --force.
    run(
        cwd,
        &[
            "worktree",
            "remove",
            "--",
            canonical.to_str().context("worktree path must be UTF-8")?,
        ],
    )?;
    Ok(())
}
