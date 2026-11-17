use anyhow::{Result, ensure};
use windows_sys::Win32::{
    Foundation::{CloseHandle, HANDLE},
    System::JobObjects::*,
};
pub struct Job(HANDLE);
// SAFETY: A Job Object handle may be transferred between threads. It is uniquely
// owned here, never duplicated, and closed once in Drop.
unsafe impl Send for Job {}
impl Job {
    pub fn terminate(&self) -> Result<()> {
        // SAFETY: This object owns a live Job handle. Termination targets only
        // processes assigned to this pane, including descendants.
        unsafe {
            ensure!(
                TerminateJobObject(self.0, 1) != 0,
                "terminate Job Object: {}",
                std::io::Error::last_os_error()
            );
        }
        Ok(())
    }

    pub fn assign(child: &dyn portable_pty::Child) -> Result<Self> {
        // SAFETY: The child owns a live process handle; the new Job handle is
        // checked, configured with a correctly-sized structure and retained.
        unsafe {
            let handle = CreateJobObjectW(std::ptr::null(), std::ptr::null());
            ensure!(
                !handle.is_null(),
                "CreateJobObject: {}",
                std::io::Error::last_os_error()
            );
            let job = Self(handle);
            let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
            info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            ensure!(
                SetInformationJobObject(
                    handle,
                    JobObjectExtendedLimitInformation,
                    (&info as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                    std::mem::size_of_val(&info) as u32
                ) != 0,
                "configure Job Object: {}",
                std::io::Error::last_os_error()
            );
            let process = child
                .as_raw_handle()
                .ok_or_else(|| anyhow::anyhow!("child has no Windows process handle"))?;
            if AssignProcessToJobObject(handle, process.cast()) == 0 {
                anyhow::bail!("assign Job Object: {}", std::io::Error::last_os_error());
            }
            Ok(job)
        }
    }
}
impl Drop for Job {
    fn drop(&mut self) {
        unsafe {
            CloseHandle(self.0);
        }
    }
}
