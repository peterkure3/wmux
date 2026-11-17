use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
};

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Config {
    pub shell: Vec<String>,
    pub default_workspace: String,
    pub default_layout: String,
    pub sidebar: bool,
    pub scrollback: usize,
    pub max_panes: usize,
    pub terminal_memory_mb: usize,
    pub keybindings: BTreeMap<String, String>,
    pub notifications: bool,
    pub log_level: String,
    pub prefix: char,
    pub agents: BTreeMap<String, Vec<String>>,
    pub theme: Theme,
    /// None inherits the daemon environment; Some limits forwarding to these names.
    pub forward_env: Option<Vec<String>>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Theme {
    pub background: [u8; 3],
    pub foreground: [u8; 3],
    pub purple: [u8; 3],
    pub cyan: [u8; 3],
}
impl Default for Theme {
    fn default() -> Self {
        Self {
            background: [8, 13, 22],
            foreground: [239, 244, 252],
            purple: [186, 97, 255],
            cyan: [48, 217, 231],
        }
    }
}
impl Default for Config {
    fn default() -> Self {
        Self {
            shell: if cfg!(windows) {
                vec!["powershell.exe".into(), "-NoLogo".into()]
            } else {
                vec![std::env::var("SHELL").unwrap_or("/bin/bash".into())]
            },
            default_workspace: "workspace".into(),
            default_layout: "single".into(),
            sidebar: true,
            scrollback: 2000,
            max_panes: 16,
            terminal_memory_mb: 128,
            keybindings: default_bindings(),
            notifications: true,
            log_level: "warn".into(),
            prefix: 'b',
            agents: BTreeMap::new(),
            theme: Theme::default(),
            forward_env: None,
        }
    }
}
impl Config {
    pub fn load(home: &Path) -> Result<Self> {
        let file = home.join("config.toml");
        let mut c: Self = if file.exists() {
            toml::from_str(&std::fs::read_to_string(file)?)?
        } else {
            Self::default()
        };
        for (action, key) in default_bindings() {
            c.keybindings.entry(action).or_insert(key);
        }
        c.validate()?;
        Ok(c)
    }
    pub fn effective_scrollback(&self) -> usize {
        // vt100 cells occupy 32 bytes. Reserve half of the terminal budget for
        // visible screens, snapshots and parser overhead; cap at maximum width.
        self.scrollback
            .min(self.terminal_memory_mb * 1024 * 1024 / 2 / (self.max_panes * 400 * 32))
    }
    pub fn validate(&self) -> Result<()> {
        ensure!(
            (16..=2048).contains(&self.terminal_memory_mb),
            "terminal_memory_mb must be 16..2048"
        );
        let defaults = default_bindings();
        let mut used = std::collections::BTreeSet::new();
        for (action, key) in &self.keybindings {
            ensure!(
                defaults.contains_key(action),
                "unknown keybinding action {action}"
            );
            ensure!(
                key.chars().count() == 1 && !key.chars().any(char::is_control),
                "keybindings must be printable characters"
            );
            ensure!(used.insert(key), "duplicate command key {key}");
        }
        ensure!(
            !self.shell.is_empty() && !self.shell[0].is_empty(),
            "shell must contain an executable"
        );
        ensure!(
            (1..=32).contains(&self.max_panes),
            "max_panes must be 1..32"
        );
        ensure!(
            self.scrollback <= 100_000,
            "scrollback exceeds 100000 lines"
        );
        ensure!(
            self.prefix.is_ascii_lowercase(),
            "prefix must be a lowercase ASCII letter"
        );
        ensure!(
            ["single", "columns", "rows", "grid", "main-stack"]
                .contains(&self.default_layout.as_str()),
            "unknown default_layout"
        );
        ensure!(
            self.agents
                .values()
                .all(|v| !v.is_empty() && !v[0].is_empty()),
            "agent commands need an executable"
        );
        Ok(())
    }
}
pub fn home() -> PathBuf {
    std::env::var_os("WMUX_HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            PathBuf::from(
                std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                    .unwrap_or_else(|| ".".into()),
            )
            .join(".wmux-rust")
        })
}
pub fn default_bindings() -> BTreeMap<String, String> {
    [
        ("detach", "d"),
        ("quit", "q"),
        ("sidebar", "b"),
        ("lower", "l"),
        ("inspector", "i"),
        ("preview", "f"),
        ("approvals", "a"),
        ("panel_view", "t"),
        ("lower_grow", "+"),
        ("lower_shrink", "-"),
        ("palette", "p"),
        ("help", "?"),
        ("new", "n"),
        ("rename", "r"),
        ("previous_workspace", "["),
        ("next_workspace", "]"),
        ("split_horizontal", "s"),
        ("split_vertical", "v"),
        ("zoom", "z"),
        ("grid", "g"),
        ("close", "x"),
        ("copy", "c"),
        ("swap", "w"),
    ]
    .into_iter()
    .map(|(a, k)| (a.into(), k.into()))
    .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn roundtrip_and_invalid_config() {
        let s = toml::to_string(&Config::default()).unwrap();
        toml::from_str::<Config>(&s).unwrap().validate().unwrap();
        assert!(toml::from_str::<Config>("scrolback=12").is_err());
        assert!(
            Config {
                max_panes: 0,
                ..Config::default()
            }
            .validate()
            .is_err()
        );
    }
}
