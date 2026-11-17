use anyhow::{Result, ensure};
use rusqlite::Connection;
use wmux_core::{ProcessState, State};
pub struct Store(Connection);
impl Store {
    pub fn open(home: &std::path::Path) -> Result<Self> {
        wmux_security::private_dir(home)?;
        let file = home.join("state.sqlite3");
        if file.exists() {
            wmux_security::private_file(&file)?;
        }
        let conn = Connection::open(&file)?;
        wmux_security::private_file(&file)?;
        let version: i32 = conn.pragma_query_value(None, "user_version", |r| r.get(0))?;
        ensure!(version <= 1, "database schema newer than this executable");
        conn.execute_batch("PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), data TEXT NOT NULL); PRAGMA user_version=1;")?;
        Ok(Self(conn))
    }
    pub fn load(&self) -> Result<State> {
        use rusqlite::OptionalExtension;
        let json: Option<String> = self
            .0
            .query_row("SELECT data FROM state WHERE id=1", [], |r| r.get(0))
            .optional()?;
        let mut state: State = match json {
            Some(s) => serde_json::from_str(&s)?,
            None => State::default(),
        };
        for w in state.workspaces.values_mut() {
            for p in w.panes.values_mut() {
                if matches!(p.state, ProcessState::Starting | ProcessState::Running) {
                    p.state = ProcessState::Disconnected;
                }
                p.pid = None;
                p.activity = wmux_core::AgentActivity::Unknown;
            }
        }
        for a in &mut state.approvals {
            if a.status == wmux_core::ApprovalStatus::Pending {
                a.status = wmux_core::ApprovalStatus::Expired;
            }
        }
        Ok(state)
    }
    pub fn save(&mut self, state: &State) -> Result<()> {
        let tx = self.0.transaction()?;
        tx.execute("INSERT INTO state(id,data) VALUES(1,?1) ON CONFLICT(id) DO UPDATE SET data=excluded.data",[serde_json::to_string(state)?])?;
        tx.commit()?;
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn storage_roundtrip_and_recovery() {
        let home = std::env::temp_dir().join(format!("wmux-store-{}", wmux_core::id()));
        let mut db = Store::open(&home).unwrap();
        let mut s = State::default();
        s.tasks.push(wmux_core::Task {
            id: "t".into(),
            title: "task".into(),
            description: String::new(),
            workspace: "w".into(),
            pane: None,
            repository: None,
            worktree: None,
            status: "open".into(),
            created: 1,
            completed: None,
        });
        db.save(&s).unwrap();
        assert_eq!(db.load().unwrap().tasks.len(), 1);
        // Failed transactions must leave the previous committed state intact.
        db.0.pragma_update(None, "query_only", true).unwrap();
        s.tasks[0].title = "must not commit".into();
        assert!(db.save(&s).is_err());
        assert_eq!(db.load().unwrap().tasks[0].title, "task");
        db.0.pragma_update(None, "query_only", false).unwrap();
        let pages: i64 =
            db.0.pragma_query_value(None, "page_count", |row| row.get(0))
                .unwrap();
        db.0.pragma_update(None, "max_page_count", pages).unwrap();
        s.tasks[0].description = "x".repeat(1024 * 1024);
        assert!(db.save(&s).is_err());
        assert_eq!(db.load().unwrap().tasks[0].title, "task");
        drop(db);
        std::fs::remove_dir_all(home).unwrap();
    }
}
