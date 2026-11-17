//! Explicit migration boundary. Legacy HTTP sessions and tokens stay with Go;
//! the Rust daemon never parses or mutates their state.
use anyhow::{Context, Result, ensure};
use std::{
    ffi::OsString,
    path::{Path, PathBuf},
};

fn arguments(args: &[OsString]) -> Option<&[OsString]> {
    let command = args.first()?.to_str()?;
    if command == "legacy" {
        return Some(&args[1..]);
    }
    // Existing installed notification hooks retain their original protocol.
    if matches!(
        command,
        "notify"
            | "hook"
            | "watch"
            | "log"
            | "debug"
            | "autostart"
            | "session"
            | "surface"
            | "connect"
            | "close"
            | "grid"
            | "tui"
            | "theme"
            | "focus"
            | "version"
    ) {
        return Some(args);
    }
    if command == "daemon"
        && args
            .get(1)
            .and_then(|s| s.to_str())
            .is_some_and(|s| matches!(s, "log" | "debug" | "update" | "autostart"))
    {
        return Some(args);
    }
    None
}
fn executable(current: &Path) -> Result<PathBuf> {
    let name = if cfg!(windows) { "wmux.exe" } else { "wmux" };
    Ok(current
        .parent()
        .context("executable directory missing")?
        .join("legacy")
        .join(name))
}
pub fn run(args: &[OsString]) -> Result<Option<i32>> {
    let Some(args) = arguments(args) else {
        return Ok(None);
    };
    let legacy = executable(&std::env::current_exe()?)?;
    ensure!(
        legacy.is_file(),
        "Go compatibility binary missing at {}; install the preserved Go pair in legacy/",
        legacy.display()
    );
    let status = std::process::Command::new(legacy)
        .args(args)
        .status()
        .context("cannot launch Go compatibility command")?;
    Ok(Some(status.code().unwrap_or(1)))
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn routes_only_legacy_protocols_and_preserves_arguments() {
        let args = vec!["legacy".into(), "new".into(), "project with spaces".into()];
        assert_eq!(arguments(&args).unwrap(), &args[1..]);
        assert!(arguments(&["notify".into(), "--message".into(), "done".into()]).is_some());
        assert!(arguments(&["daemon".into(), "log".into()]).is_some());
        for command in ["new", "attach", "list", "agent", "update", "--help"] {
            assert!(arguments(&[command.into()]).is_none());
        }
        assert!(arguments(&["daemon".into(), "start".into()]).is_none());
    }
}
