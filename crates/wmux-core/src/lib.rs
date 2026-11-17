pub mod config;
pub mod layout;

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

pub type Id = String;
pub fn id() -> Id {
    uuid::Uuid::new_v4().to_string()
}
pub fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub enum ProcessState {
    Starting,
    Running,
    Completed,
    Failed,
    Stopped,
    Disconnected,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub enum AgentActivity {
    Unknown,
    Running,
    Idle,
    WaitingForInput,
    WaitingForApproval,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Pane {
    pub id: Id,
    pub title: String,
    pub command: Vec<String>,
    pub cwd: String,
    pub state: ProcessState,
    pub exit_code: Option<u32>,
    pub pid: Option<u32>,
    pub agent: Option<String>,
    pub activity: AgentActivity,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Workspace {
    pub id: Id,
    pub name: String,
    pub cwd: String,
    pub panes: BTreeMap<Id, Pane>,
    pub layout: Option<layout::Node>,
    pub focused: Option<Id>,
    pub zoomed: bool,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Task {
    pub id: Id,
    pub title: String,
    pub description: String,
    pub workspace: Id,
    pub pane: Option<Id>,
    pub repository: Option<String>,
    pub worktree: Option<String>,
    pub status: String,
    pub created: u64,
    pub completed: Option<u64>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Notification {
    pub id: Id,
    pub workspace: Id,
    pub pane: Id,
    pub text: String,
    pub created: u64,
}
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct State {
    #[serde(default)]
    pub approvals: Vec<Approval>,
    pub workspaces: BTreeMap<Id, Workspace>,
    pub tasks: Vec<Task>,
    pub notifications: Vec<Notification>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub enum ApprovalStatus {
    Pending,
    Approved,
    Rejected,
    Expired,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Approval {
    pub id: Id,
    pub pane: Id,
    pub workspace: Id,
    pub title: String,
    pub details: String,
    pub destructive: bool,
    pub status: ApprovalStatus,
    pub created: u64,
    pub expires: u64,
}
