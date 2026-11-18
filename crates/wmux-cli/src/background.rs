//! Platform-isolated daemon launch. Never inherit the calling terminal's pipes.
use anyhow::Result;
use std::path::Path;

#[cfg(unix)]
pub struct Child(std::process::Child);
#[cfg(unix)]
impl Child {
    pub fn try_wait(&mut self) -> std::io::Result<Option<u32>> {
        Ok(self.0.try_wait()?.map(|s| s.code().unwrap_or(1) as u32))
    }
}
#[cfg(unix)]
pub fn spawn(executable: &Path, home: &Path) -> Result<Child> {
    use std::os::unix::process::CommandExt;
    let log = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(home.join("daemon.log"))?;
    wmux_security::private_file(&home.join("daemon.log"))?;
    let mut command = std::process::Command::new(executable);
    command
        .env("WMUX_HOME", home)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(log);
    // SAFETY: setsid is async-signal-safe; no allocation/locks run between fork
    // and exec. A separate session prevents the parent terminal's SIGHUP.
    unsafe {
        command.pre_exec(|| {
            if libc::setsid() < 0 {
                Err(std::io::Error::last_os_error())
            } else {
                Ok(())
            }
        });
    }
    Ok(Child(command.spawn()?))
}

#[cfg(windows)]
pub struct Child(windows_sys::Win32::Foundation::HANDLE);
#[cfg(windows)]
impl Child {
    pub fn try_wait(&mut self) -> std::io::Result<Option<u32>> {
        use windows_sys::Win32::{
            Foundation::{WAIT_OBJECT_0, WAIT_TIMEOUT},
            System::Threading::{GetExitCodeProcess, WaitForSingleObject},
        };
        // SAFETY: This object uniquely owns a live process handle; the output
        // buffer is a correctly-sized initialized u32.
        unsafe {
            match WaitForSingleObject(self.0, 0) {
                WAIT_TIMEOUT => Ok(None),
                WAIT_OBJECT_0 => {
                    let mut code = 0;
                    if GetExitCodeProcess(self.0, &mut code) == 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                    Ok(Some(code))
                }
                _ => Err(std::io::Error::last_os_error()),
            }
        }
    }
}
#[cfg(windows)]
impl Drop for Child {
    fn drop(&mut self) {
        unsafe {
            windows_sys::Win32::Foundation::CloseHandle(self.0);
        }
    }
}

#[cfg(windows)]
pub fn spawn(executable: &Path, home: &Path) -> Result<Child> {
    use std::{ffi::OsString, os::windows::ffi::OsStrExt};
    use windows_sys::Win32::{Foundation::CloseHandle, System::Threading::*};
    let application: Vec<u16> = executable
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect();
    let mut command = vec![b'"' as u16];
    command.extend(executable.as_os_str().encode_wide());
    command.extend([b'"' as u16, 0]);
    let original_command = command.clone();
    let mut entries = std::env::vars_os()
        .filter(|(key, _)| !key.to_string_lossy().eq_ignore_ascii_case("WMUX_HOME"))
        .collect::<Vec<_>>();
    entries.push((OsString::from("WMUX_HOME"), home.as_os_str().to_os_string()));
    entries.sort_by_key(|(key, _)| key.to_string_lossy().to_uppercase());
    let mut environment = Vec::new();
    for (key, value) in entries {
        environment.extend(key.encode_wide());
        environment.push(b'=' as u16);
        environment.extend(value.encode_wide());
        environment.push(0);
    }
    environment.push(0);
    // SAFETY: Application/command/environment are NUL-terminated UTF-16.
    // CreateProcess inherits NO handles, so a captured CLI output pipe cannot
    // stay open in wmuxd. The daemon opens its own private log file. Thread and
    // process handles are closed exactly once; closing our handle does not kill
    // the background process.
    unsafe {
        let mut startup: STARTUPINFOW = std::mem::zeroed();
        startup.cb = std::mem::size_of::<STARTUPINFOW>() as u32;
        startup.dwFlags = STARTF_USESHOWWINDOW;
        startup.wShowWindow = 0; // SW_HIDE
        let mut process: PROCESS_INFORMATION = std::mem::zeroed();
        let flags = DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_UNICODE_ENVIRONMENT;
        let mut ok = CreateProcessW(
            application.as_ptr(),
            command.as_mut_ptr(),
            std::ptr::null(),
            std::ptr::null(),
            0,
            flags | CREATE_BREAKAWAY_FROM_JOB,
            environment.as_ptr().cast(),
            std::ptr::null(),
            &startup,
            &mut process,
        );
        if ok == 0 && std::io::Error::last_os_error().raw_os_error() == Some(5) {
            // Some managed hosts forbid job breakaway. Respect that policy;
            // retain their job and report the actual persistence limitation.
            command.clone_from(&original_command);
            ok = CreateProcessW(
                application.as_ptr(),
                command.as_mut_ptr(),
                std::ptr::null(),
                std::ptr::null(),
                0,
                flags,
                environment.as_ptr().cast(),
                std::ptr::null(),
                &startup,
                &mut process,
            );
            if ok != 0 {
                eprintln!(
                    "wmux: host job forbids breakaway; closing that host may stop this daemon"
                );
            }
        }
        if ok == 0 {
            return Err(std::io::Error::last_os_error().into());
        }
        CloseHandle(process.hThread);
        Ok(Child(process.hProcess))
    }
}
