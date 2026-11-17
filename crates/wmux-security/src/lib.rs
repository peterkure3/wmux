use anyhow::{Result, ensure};
use std::path::Path;

pub fn private_dir(path: &Path) -> Result<()> {
    std::fs::create_dir_all(path)?;
    ensure!(
        !std::fs::symlink_metadata(path)?.file_type().is_symlink(),
        "state directory must not be a symlink"
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        let meta = std::fs::metadata(path)?;
        // The home directory is user-selected; permission tightening must never
        // be applied to another user's directory.
        ensure!(
            meta.uid() == current_uid(),
            "state directory has a different owner"
        );
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))?;
    }
    #[cfg(windows)]
    windows::protect(path)?;
    Ok(())
}
#[cfg(unix)]
fn current_uid() -> u32 {
    // SAFETY: getuid has no arguments or memory preconditions.
    unsafe { libc::getuid() }
}
pub fn private_file(path: &Path) -> Result<()> {
    ensure!(
        !std::fs::symlink_metadata(path)?.file_type().is_symlink(),
        "state file must not be a symlink"
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))?;
    }
    #[cfg(windows)]
    windows::protect(path)?;
    Ok(())
}
pub fn clean_text(s: &str) -> String {
    s.chars()
        .filter(|c| !c.is_control() || *c == '\n' || *c == '\t')
        .take(65536)
        .collect()
}

#[cfg(windows)]
pub mod windows {
    use super::*;
    use windows_sys::Win32::{
        Foundation::{CloseHandle, LocalFree},
        Security::{
            Authorization::{
                ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW,
            },
            GetTokenInformation, SECURITY_ATTRIBUTES, TOKEN_QUERY, TOKEN_USER, TokenUser,
        },
        System::Threading::{GetCurrentProcess, OpenProcessToken},
    };
    pub fn protect(path: &Path) -> Result<()> {
        use std::os::windows::ffi::OsStrExt;
        use windows_sys::Win32::Security::{
            DACL_SECURITY_INFORMATION, PROTECTED_DACL_SECURITY_INFORMATION, SetFileSecurityW,
        };
        let security = PipeSecurity::new()?;
        let path: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        // SAFETY: Path is NUL-terminated; descriptor stays alive through the call.
        unsafe {
            ensure!(
                SetFileSecurityW(
                    path.as_ptr(),
                    DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                    security.descriptor
                ) != 0,
                "protect state path: {}",
                std::io::Error::last_os_error()
            );
        }
        Ok(())
    }
    pub fn user_sid() -> Result<String> {
        // SAFETY: Every Win32 output buffer has the documented size; allocated
        // SID strings are copied before LocalFree, and token handles are closed.
        unsafe {
            let mut token = std::ptr::null_mut();
            ensure!(
                OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) != 0,
                "OpenProcessToken: {}",
                std::io::Error::last_os_error()
            );
            let mut len = 0;
            GetTokenInformation(token, TokenUser, std::ptr::null_mut(), 0, &mut len);
            let mut buffer = vec![0usize; (len as usize).div_ceil(std::mem::size_of::<usize>())];
            let ok =
                GetTokenInformation(token, TokenUser, buffer.as_mut_ptr().cast(), len, &mut len);
            CloseHandle(token);
            ensure!(
                ok != 0,
                "GetTokenInformation: {}",
                std::io::Error::last_os_error()
            );
            let user = &*(buffer.as_ptr().cast::<TOKEN_USER>());
            let mut sid = std::ptr::null_mut();
            ensure!(
                ConvertSidToStringSidW(user.User.Sid, &mut sid) != 0,
                "cannot format user SID"
            );
            let mut n = 0;
            while *sid.add(n) != 0 {
                n += 1;
            }
            let result = String::from_utf16_lossy(std::slice::from_raw_parts(sid, n));
            LocalFree(sid.cast());
            Ok(result)
        }
    }
    pub struct PipeSecurity {
        descriptor: *mut core::ffi::c_void,
        pub attrs: SECURITY_ATTRIBUTES,
    }
    impl PipeSecurity {
        pub fn new() -> Result<Self> {
            let sddl: Vec<u16> = format!("D:P(A;OICI;GA;;;{})", user_sid()?)
                .encode_utf16()
                .chain(Some(0))
                .collect();
            let mut descriptor = std::ptr::null_mut();
            // SAFETY: SDDL is NUL terminated. Descriptor is kept alive through
            // pipe creation and released by Drop, never sent across threads.
            unsafe {
                ensure!(
                    ConvertStringSecurityDescriptorToSecurityDescriptorW(
                        sddl.as_ptr(),
                        1,
                        &mut descriptor,
                        std::ptr::null_mut()
                    ) != 0,
                    "pipe ACL: {}",
                    std::io::Error::last_os_error()
                );
            }
            Ok(Self {
                descriptor,
                attrs: SECURITY_ATTRIBUTES {
                    nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
                    lpSecurityDescriptor: descriptor,
                    bInheritHandle: 0,
                },
            })
        }
    }
    impl Drop for PipeSecurity {
        fn drop(&mut self) {
            unsafe {
                LocalFree(self.descriptor);
            }
        }
    }
}
