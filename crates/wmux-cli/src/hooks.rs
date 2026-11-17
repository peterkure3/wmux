//! Opt-in official Claude/Codex command hooks. Stdout is reserved for the
//! provider's JSON decision contract. No hook starts wmuxd or executes a tool.
use anyhow::{Context, Result, ensure};
use serde_json::{Value, json};
use std::{io::Read, path::Path, time::Duration};
use wmux_core::{AgentActivity, ApprovalStatus};
use wmux_ipc::{Reply, Request};

fn redact(value: &mut Value) {
    match value {
        Value::Object(fields) => {
            for (name, value) in fields {
                let key = name.to_ascii_lowercase();
                if ["token", "password", "secret", "authorization", "api_key"]
                    .iter()
                    .any(|s| key.contains(s))
                {
                    *value = Value::String("[redacted]".into());
                } else {
                    redact(value);
                }
            }
        }
        Value::Array(items) => {
            for item in items {
                redact(item);
            }
        }
        _ => (),
    }
}
fn permission(input: &Value) -> Result<Option<(String, String)>> {
    let tool = input["tool_name"]
        .as_str()
        .context("permission event needs tool_name")?;
    // These dialogs require provider-specific rewritten input. Leave their
    // original native prompt intact instead of granting an incomplete decision.
    if matches!(tool, "AskUserQuestion" | "ExitPlanMode") {
        return Ok(None);
    }
    ensure!(
        !tool.is_empty() && tool.len() <= 150 && !tool.chars().any(char::is_control),
        "invalid tool name"
    );
    ensure!(
        input.get("tool_input").is_some(),
        "permission event needs tool_input"
    );
    let mut details =
        json!({"tool":tool, "input":input["tool_input"], "session_id":input["session_id"]});
    redact(&mut details);
    let details = serde_json::to_string_pretty(&details)?;
    ensure!(
        details.len() <= 65536,
        "permission details exceed 64 KiB; refusing incomplete context"
    );
    Ok(Some((format!("Tool permission: {tool}"), details)))
}
fn decision(allow: bool) -> Value {
    let mut result = json!({"behavior":if allow {"allow"} else {"deny"}});
    if !allow {
        result["message"] = json!("WMUX permission rejected, expired, or unavailable");
    }
    json!({"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":result}})
}
async fn wait(home: &Path, pane: String, title: String, details: String, ttl: u64) -> Result<bool> {
    let Reply::Created(id) = wmux_ipc::call(
        home,
        Request::ApprovalCreate {
            pane,
            title,
            details,
            destructive: true,
            ttl,
        },
    )
    .await?
    else {
        anyhow::bail!("invalid approval response")
    };
    loop {
        let snapshot = super::snapshot(home).await?;
        let approval = snapshot
            .state
            .approvals
            .iter()
            .find(|a| a.id == id)
            .context("approval disappeared")?;
        match approval.status {
            ApprovalStatus::Approved => return Ok(true),
            ApprovalStatus::Rejected | ApprovalStatus::Expired => return Ok(false),
            ApprovalStatus::Pending => (),
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
}
pub async fn run(home: &Path, provider: &str, pane: String, ttl: u64) -> Result<()> {
    ensure!(
        matches!(provider, "claude" | "codex"),
        "unsupported hook provider"
    );
    let bytes = tokio::task::spawn_blocking(|| {
        let mut bytes = Vec::new();
        std::io::stdin()
            .take(1024 * 1024 + 1)
            .read_to_end(&mut bytes)
            .map(|_| bytes)
    })
    .await??;
    ensure!(bytes.len() <= 1024 * 1024, "hook input exceeds 1 MiB");
    let input: Value = serde_json::from_slice(&bytes)?;
    let event = input["hook_event_name"]
        .as_str()
        .context("hook_event_name missing")?;
    if event == "PermissionRequest" {
        let result = async {
            let Some((title, details)) = permission(&input)? else {
                return Ok(None);
            };
            wait(home, pane, title, details, ttl).await.map(Some)
        }
        .await;
        let output = match result {
            Ok(Some(allow)) => decision(allow),
            Ok(None) => json!({}),
            Err(_) => decision(false), // No error/log may contain the raw request.
        };
        println!("{output}");
        return Ok(());
    }
    let (activity, notify) = match event {
        "SessionStart" | "SessionEnd" => (AgentActivity::Unknown, None),
        "UserPromptSubmit" | "PreToolUse" | "PostToolUse" => (AgentActivity::Running, None),
        "Stop" => (AgentActivity::Idle, Some(format!("{provider} turn ended"))),
        "Notification" => match input["notification_type"].as_str() {
            Some("permission_prompt") => (
                AgentActivity::WaitingForApproval,
                Some(format!("{provider} requests attention")),
            ),
            Some("idle_prompt") => (
                AgentActivity::WaitingForInput,
                Some(format!("{provider} waiting for input")),
            ),
            _ => return Ok(()),
        },
        _ => return Ok(()),
    };
    wmux_ipc::call(
        home,
        Request::AgentEvent {
            pane,
            activity,
            message: notify,
        },
    )
    .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn permission_contract_preserves_context_and_redacts_named_secrets() {
        let (_, details) = permission(
            &json!({"tool_name":"Bash", "tool_input":{"command":"git status","api_key":"private"}}),
        )
        .unwrap()
        .unwrap();
        assert!(details.contains("git status"));
        assert!(!details.contains("private"));
        assert!(permission(&json!({"tool_name":"Bash"})).is_err());
        assert!(
            permission(&json!({"tool_name":"AskUserQuestion"}))
                .unwrap()
                .is_none()
        );
        assert_eq!(
            decision(true)["hookSpecificOutput"]["decision"]["behavior"],
            "allow"
        );
        assert_eq!(
            decision(false)["hookSpecificOutput"]["decision"]["behavior"],
            "deny"
        );
    }
}
