package dev.wmux.core.win

import com.sun.jna.platform.win32.Advapi32Util
import com.sun.jna.platform.win32.Shell32
import com.sun.jna.platform.win32.WinUser

/**
 * On-demand UAC elevation, per the wmux spec: the app itself always runs
 * asInvoker (the jpackage launcher's default manifest, plus per-user
 * install), and individual commands that need administrator rights are
 * launched through [runElevated], which triggers the standard UAC
 * consent prompt via the shell's "runas" verb.
 *
 * The elevated process runs detached: it cannot share the parent's
 * ConPTY, so elevated commands open in their own console window. That is
 * the deliberate trade-off of not running the whole multiplexer
 * elevated.
 */
object Elevation {

    /** True when the current process already holds an elevated token. */
    fun isElevated(): Boolean = Advapi32Util.isCurrentProcessElevated()

    /**
     * Launches [executable] elevated (UAC prompt) with optional
     * [parameters] and [workingDir]. Returns true if the shell accepted
     * the launch; false if it failed or the user declined the prompt.
     *
     * [parameters] is passed through to the target verbatim - callers
     * building it from user input are responsible for quoting each
     * argument, since Windows command lines are a single string.
     */
    fun runElevated(executable: String, parameters: String? = null, workingDir: String? = null): Boolean {
        val result = Shell32.INSTANCE.ShellExecute(
            null,
            "runas",
            executable,
            parameters,
            workingDir,
            WinUser.SW_SHOWNORMAL,
        )
        // ShellExecute contract: values > 32 mean success; <= 32 are error
        // codes (SE_ERR_ACCESSDENIED = 5 is what a declined UAC prompt returns).
        return result.toLong() > 32
    }
}
