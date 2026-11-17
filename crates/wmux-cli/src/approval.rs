use anyhow::{Context, Result, bail, ensure};
use clap::Subcommand;
use std::{path::Path, time::Duration};
use wmux_core::ApprovalStatus;
use wmux_ipc::{Reply, Request};
#[derive(Subcommand)]
pub enum Command {
    /// Wait for an explicit decision. Exit 0 only when approved; execute nothing.
    Request {
        title: String,
        #[arg(long, env = "WMUX_PANE_ID")]
        pane: String,
        #[arg(long, default_value = "")]
        details: String,
        #[arg(long)]
        destructive: bool,
        #[arg(long, default_value_t = 300)]
        ttl: u64,
    },
    Approve {
        id: String,
        #[arg(long)]
        force: bool,
    },
    Reject {
        id: String,
    },
    Inspect {
        id: String,
    },
}
pub async fn run(home: &Path, command: Command) -> Result<()> {
    match command {
        Command::Request {
            title,
            pane,
            details,
            destructive,
            ttl,
        } => {
            let Reply::Created(id) = wmux_ipc::call(
                home,
                Request::ApprovalCreate {
                    pane,
                    title,
                    details,
                    destructive,
                    ttl,
                },
            )
            .await?
            else {
                bail!("invalid approval response")
            };
            eprintln!("Waiting for approval {id}");
            loop {
                let s = super::snapshot(home).await?;
                let a = s
                    .state
                    .approvals
                    .iter()
                    .find(|a| a.id == id)
                    .context("approval disappeared")?;
                match a.status {
                    ApprovalStatus::Approved => {
                        println!("{id} approved");
                        return Ok(());
                    }
                    ApprovalStatus::Rejected => bail!("approval rejected"),
                    ApprovalStatus::Expired => bail!("approval expired or requester stopped"),
                    ApprovalStatus::Pending => (),
                }
                tokio::time::sleep(Duration::from_millis(250)).await;
            }
        }
        Command::Approve { id, force } => {
            let s = super::snapshot(home).await?;
            let a = s
                .state
                .approvals
                .iter()
                .find(|a| a.id == id)
                .context("approval not found")?;
            println!("{}\n{}", a.title, a.details);
            if a.destructive {
                super::confirm(force, "Approve the destructive operation shown above?")?;
            }
            ensure!(
                a.status == ApprovalStatus::Pending,
                "approval is not pending"
            );
            wmux_ipc::call(home, Request::ApprovalDecide { id, approve: true }).await?;
        }
        Command::Reject { id } => {
            wmux_ipc::call(home, Request::ApprovalDecide { id, approve: false }).await?;
        }
        Command::Inspect { id } => {
            let s = super::snapshot(home).await?;
            let a = s
                .state
                .approvals
                .iter()
                .find(|a| a.id == id)
                .context("approval not found")?;
            println!(
                "{}\n{}\n{:?} · destructive={} · expires={}\n{}",
                a.id, a.title, a.status, a.destructive, a.expires, a.details
            );
        }
    }
    Ok(())
}
