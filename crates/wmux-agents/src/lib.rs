use anyhow::{Result, bail};
use wmux_core::config::Config;
pub trait Adapter {
    fn identity(&self) -> &str;
    fn command(&self, config: &Config) -> Vec<String>;
    fn available(&self, config: &Config) -> bool {
        let c = self.command(config);
        find(&c[0]).is_some()
    }
}
pub struct CommandAgent(pub String);
impl Adapter for CommandAgent {
    fn identity(&self) -> &str {
        &self.0
    }
    fn command(&self, config: &Config) -> Vec<String> {
        config
            .agents
            .get(&self.0)
            .cloned()
            .unwrap_or_else(|| vec![self.0.clone()])
    }
}
pub const BUILTINS: [&str; 4] = ["codex", "claude", "gemini", "aider"];
pub fn launch(name: &str, config: &Config) -> Result<Vec<String>> {
    if !BUILTINS.contains(&name) && !config.agents.contains_key(name) {
        bail!("unknown agent {name}; configure [agents] for custom commands");
    }
    let adapter = CommandAgent(name.into());
    let cmd = adapter.command(config);
    if !adapter.available(config) {
        bail!("agent executable {} was not found on PATH", cmd[0]);
    }
    #[cfg(windows)]
    {
        windows_command(cmd)
    }
    #[cfg(unix)]
    {
        Ok(cmd)
    }
}
pub fn find(executable: &str) -> Option<std::path::PathBuf> {
    use std::path::PathBuf;
    let p = PathBuf::from(executable);
    let executable_file = |p: &std::path::Path| {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if !p.is_file() {
                return false;
            }
            std::fs::metadata(p).is_ok_and(|m| m.permissions().mode() & 0o111 != 0)
        }
        #[cfg(windows)]
        {
            p.is_file()
        }
    };
    if p.is_absolute() || p.components().count() > 1 {
        return executable_file(&p).then_some(p);
    }
    for dir in std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()) {
        #[cfg(unix)]
        let candidate = dir.join(executable);
        #[cfg(unix)]
        if executable_file(&candidate) {
            return Some(candidate);
        }
        #[cfg(windows)]
        for ext in ["", ".exe", ".com", ".cmd", ".bat"] {
            if ext.is_empty()
                && !executable.to_ascii_lowercase().ends_with(".exe")
                && !executable.to_ascii_lowercase().ends_with(".com")
                && !executable.to_ascii_lowercase().ends_with(".cmd")
                && !executable.to_ascii_lowercase().ends_with(".bat")
            {
                continue;
            }
            let candidate = dir.join(format!("{executable}{ext}"));
            if executable_file(&candidate) {
                return Some(candidate);
            }
        }
    }
    None
}

#[cfg(windows)]
fn windows_command(mut command: Vec<String>) -> Result<Vec<String>> {
    use base64::Engine;
    let path = find(&command[0]).ok_or_else(|| anyhow::anyhow!("executable disappeared"))?;
    command[0] = path.to_string_lossy().into_owned();
    if path
        .extension()
        .is_some_and(|s| s.eq_ignore_ascii_case("cmd") || s.eq_ignore_ascii_case("bat"))
    {
        // Npm installs Windows command shims. PowerShell's encoded, literal
        // invocation handles spaces in their path. Refuse CMD metacharacters
        // rather than pretending arbitrary CMD arguments are shell-safe.
        anyhow::ensure!(
            command
                .iter()
                .all(|s| !s.chars().any(|c| "&|<>^%!\"\r\n".contains(c))),
            "CMD shim arguments contain shell metacharacters; configure the agent's native executable or node script directly"
        );
        let script = format!(
            "& {}",
            command
                .iter()
                .map(|s| format!("'{}'", s.replace('\'', "''")))
                .collect::<Vec<_>>()
                .join(" ")
        );
        let bytes = script
            .encode_utf16()
            .flat_map(u16::to_le_bytes)
            .collect::<Vec<_>>();
        return Ok(vec![
            "powershell.exe".into(),
            "-NoLogo".into(),
            "-NoProfile".into(),
            "-EncodedCommand".into(),
            base64::engine::general_purpose::STANDARD.encode(bytes),
        ]);
    }
    Ok(command)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn missing_agent_fails_without_text_inference() {
        let mut c = Config::default();
        c.agents.insert(
            "custom".into(),
            vec!["wmux-impossible-executable-8173".into()],
        );
        assert!(launch("custom", &c).is_err());
        assert!(launch("unknown", &c).is_err());
    }
}
