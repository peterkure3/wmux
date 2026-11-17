use std::{path::Path, process::Command};
fn git(path: &Path, args: &[&str]) {
    let output = Command::new("git")
        .arg("-C")
        .arg(path)
        .args(args)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
}
#[test]
fn worktree_creation_diff_and_dirty_cleanup_refusal() {
    let root = std::env::temp_dir().join(format!(
        "wmux-git-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let repo = root.join("repo");
    std::fs::create_dir_all(&repo).unwrap();
    git(&repo, &["init", "-b", "main"]);
    git(&repo, &["config", "core.autocrlf", "false"]);
    git(&repo, &["config", "user.email", "test@example.invalid"]);
    git(&repo, &["config", "user.name", "WMUX test"]);
    std::fs::write(repo.join("file.txt"), "first\n").unwrap();
    git(&repo, &["add", "file.txt"]);
    git(&repo, &["commit", "-m", "initial"]);
    let worktree = root.join("agent worktree");
    wmux_git::create_worktree(&repo, &worktree, "agent-test").unwrap();
    assert_eq!(wmux_git::info(&worktree).unwrap().branch, "agent-test");
    assert_eq!(wmux_git::info(&repo).unwrap().worktrees, 2);
    std::fs::write(worktree.join("file.txt"), "second\n").unwrap();
    assert_eq!(wmux_git::info(&worktree).unwrap().changed, 1);
    assert!(wmux_git::diff(&worktree).unwrap().contains("+second"));
    git(&worktree, &["add", "file.txt"]);
    assert!(wmux_git::diff(&worktree).unwrap().is_empty());
    assert!(
        wmux_git::staged_diff(&worktree)
            .unwrap()
            .contains("+second")
    );
    std::fs::write(worktree.join("new file.txt"), "new content\n").unwrap();
    assert_eq!(
        wmux_git::untracked(&worktree).unwrap(),
        vec!["new file.txt"]
    );
    assert!(
        wmux_git::preview(&worktree, Path::new("new file.txt"))
            .unwrap()
            .text
            .contains("new content")
    );
    assert!(wmux_git::preview(&worktree, Path::new("../repo/file.txt")).is_err());
    assert!(wmux_git::preview(&worktree, &repo.join("file.txt")).is_err());
    std::fs::write(worktree.join("new file.txt"), vec![b'x'; 70000]).unwrap();
    let preview = wmux_git::preview(&worktree, Path::new("new file.txt")).unwrap();
    assert!(preview.truncated);
    assert_eq!(preview.text.len(), 65536);
    std::fs::write(worktree.join("file.txt"), vec![b'x'; 9 * 1024 * 1024]).unwrap();
    assert!(
        wmux_git::diff(&worktree)
            .unwrap_err()
            .to_string()
            .contains("output exceeds limit")
    );
    assert!(wmux_git::remove_worktree(&repo, &worktree).is_err());
    git(
        &worktree,
        &[
            "restore",
            "--source=HEAD",
            "--staged",
            "--worktree",
            "--",
            "file.txt",
        ],
    );
    std::fs::remove_file(worktree.join("new file.txt")).unwrap();
    wmux_git::remove_worktree(&repo, &worktree).unwrap();
    assert!(!worktree.exists());
    std::fs::remove_dir_all(root).unwrap();
}
