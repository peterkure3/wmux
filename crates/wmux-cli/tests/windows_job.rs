//! A terminal host may kill its job on close. Detached console flags alone do
//! not remove job membership. Verify actual survival after that job is closed.
#![cfg(windows)]
use std::{collections::BTreeMap, os::windows::ffi::OsStrExt};
use windows_sys::Win32::{
    Foundation::*,
    System::{JobObjects::*, Threading::*},
};
use wmux_ipc::{Reply, Request};
struct Handle(HANDLE);
struct Cleanup(std::path::PathBuf);
impl Drop for Cleanup {
    fn drop(&mut self) {
        let _ = std::process::Command::new(env!("CARGO_BIN_EXE_wmux"))
            .args([
                "--home",
                &self.0.to_string_lossy(),
                "daemon",
                "stop",
                "--force",
            ])
            .output();
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
impl Drop for Handle {
    fn drop(&mut self) {
        unsafe {
            CloseHandle(self.0);
        }
    }
}
#[tokio::test]
async fn daemon_survives_terminal_host_job_close() {
    let home = std::env::temp_dir().join(format!("wmux-job-{}", wmux_core::id()));
    std::fs::create_dir_all(&home).unwrap();
    let _cleanup = Cleanup(home.clone());
    std::fs::write(
        home.join("config.toml"),
        "shell = [\"cmd.exe\", \"/d\", \"/q\"]",
    )
    .unwrap();
    let executable = std::path::Path::new(env!("CARGO_BIN_EXE_wmux"));
    let application = executable
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect::<Vec<_>>();
    let text = format!(
        "\"{}\" --home \"{}\" new job-close --detach",
        executable.display(),
        home.display()
    );
    let mut command = text.encode_utf16().chain(Some(0)).collect::<Vec<_>>();
    // SAFETY: Handles are uniquely owned, buffers remain live during each API
    // call, and the process is suspended until its test-only job is installed.
    unsafe {
        let job = Handle(CreateJobObjectW(std::ptr::null(), std::ptr::null()));
        assert!(!job.0.is_null());
        let mut limits: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
        limits.BasicLimitInformation.LimitFlags =
            JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | JOB_OBJECT_LIMIT_BREAKAWAY_OK;
        assert_ne!(
            SetInformationJobObject(
                job.0,
                JobObjectExtendedLimitInformation,
                &limits as *const _ as *const _,
                std::mem::size_of_val(&limits) as u32
            ),
            0
        );
        let mut startup: STARTUPINFOW = std::mem::zeroed();
        startup.cb = std::mem::size_of_val(&startup) as u32;
        let mut process: PROCESS_INFORMATION = std::mem::zeroed();
        assert_ne!(
            CreateProcessW(
                application.as_ptr(),
                command.as_mut_ptr(),
                std::ptr::null(),
                std::ptr::null(),
                0,
                CREATE_SUSPENDED | CREATE_NO_WINDOW,
                std::ptr::null(),
                std::ptr::null(),
                &startup,
                &mut process
            ),
            0
        );
        let process_handle = Handle(process.hProcess);
        let thread = Handle(process.hThread);
        assert_ne!(AssignProcessToJobObject(job.0, process_handle.0), 0);
        assert_ne!(ResumeThread(thread.0), u32::MAX);
        assert_eq!(WaitForSingleObject(process_handle.0, 30000), WAIT_OBJECT_0);
        let mut code = 1;
        assert_ne!(GetExitCodeProcess(process_handle.0, &mut code), 0);
        assert_eq!(code, 0);
        drop(job); // Kill any children still associated with the terminal host.
    }
    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    assert!(matches!(
        wmux_ipc::call(&home, Request::Ping).await.unwrap(),
        Reply::Pong
    ));
    let Reply::Snapshot(s) = wmux_ipc::call(
        &home,
        Request::Get {
            scrollback: BTreeMap::new(),
        },
    )
    .await
    .unwrap() else {
        panic!()
    };
    assert!(s.state.workspaces.values().any(|w| w.name == "job-close"));
    wmux_ipc::call(&home, Request::Shutdown).await.unwrap();
    // State may still be flushing when the shutdown acknowledgement arrives.
    for _ in 0..100 {
        if std::fs::remove_dir_all(&home).is_ok() {
            break;
        }
        tokio::time::sleep(std::time::Duration::from_millis(25)).await;
    }
}
