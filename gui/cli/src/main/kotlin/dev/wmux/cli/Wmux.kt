package dev.wmux.cli

import com.github.ajalt.clikt.core.CliktCommand
import com.github.ajalt.clikt.core.main
import com.github.ajalt.clikt.core.subcommands
import com.github.ajalt.clikt.parameters.arguments.argument
import com.github.ajalt.clikt.parameters.arguments.multiple
import com.github.ajalt.clikt.parameters.options.flag
import com.github.ajalt.clikt.parameters.options.option
import dev.wmux.core.net.NewSurfaceRequest
import dev.wmux.core.net.SessionInfo
import dev.wmux.core.net.WmuxDaemonClient
import dev.wmux.core.net.WmuxStatusException

class Wmux : CliktCommand(name = "wmux") {
    override fun run() = Unit
}

/** Every session-touching command shares one client, built fresh per invocation. */
private fun daemonClient() = WmuxDaemonClient()

private fun reportUnreachable(cmd: String, e: Exception): Nothing {
    System.err.println("wmux $cmd: could not reach wmuxd: ${e.message}")
    kotlin.system.exitProcess(3)
}

class New : CliktCommand(name = "new") {
    override fun help(context: com.github.ajalt.clikt.core.Context) =
        "Create a daemon-owned surface (attachable from the GUI and `wmux connect`)"

    private val id by option("--id", help = "session ID (default: derived from --cwd)")
    private val cwd by option("--cwd", help = "working directory (default: the current directory)")
    private val distro by option("--distro", help = "WSL distro name; omit to run natively")
    private val commandArgs by argument(help = "command to run, e.g. 'claude'").multiple(required = true)

    override fun run() {
        val dir = cwd ?: System.getProperty("user.dir")
        val sessionId = id ?: dir.substringAfterLast('\\').substringAfterLast('/').ifBlank { "session" }
        val client = daemonClient()
        val info = try {
            client.newSurface(
                NewSurfaceRequest(
                    id = sessionId,
                    cwd = dir,
                    command = commandArgs.joinToString(" "),
                    distro = distro ?: "",
                    native = distro == null,
                ),
            )
        } catch (e: WmuxStatusException) {
            echo("wmux new: daemon returned ${e.status}: ${e.body}", err = true)
            kotlin.system.exitProcess(1)
        } catch (e: Exception) {
            reportUnreachable("new", e)
        }
        echo(info.id)
    }
}

class ListSessions : CliktCommand(name = "list") {
    override fun help(context: com.github.ajalt.clikt.core.Context) = "List sessions the daemon is tracking"

    override fun run() {
        val client = daemonClient()
        val sessions = try {
            client.listSessions()
        } catch (e: Exception) {
            reportUnreachable("list", e)
        }
        if (sessions.isEmpty()) {
            echo("no sessions")
            return
        }
        for (s in sessions) echo(formatRow(s))
    }

    private fun formatRow(s: SessionInfo): String {
        val status = if (s.running) "running" else "exited"
        val surface = if (s.surface) " [surface]" else ""
        return "%-20s %-8s %-30s branch=%-15s ports=%s note=%s%s".format(
            s.id, status, s.cwd, s.branch, s.ports, "\"${s.lastNote}\"", surface,
        )
    }
}

class Kill : CliktCommand(name = "kill") {
    override fun help(context: com.github.ajalt.clikt.core.Context) = "Kill a session's tracked process"

    private val session by argument(help = "session ID")

    override fun run() {
        val client = daemonClient()
        try {
            client.closeSession(session)
        } catch (e: WmuxStatusException) {
            echo("wmux kill: daemon returned ${e.status}: ${e.body}", err = true)
            kotlin.system.exitProcess(1)
        } catch (e: Exception) {
            reportUnreachable("kill", e)
        }
        echo("killed '$session'")
    }
}

fun main(args: Array<String>) {
    Wmux()
        .subcommands(New(), ListSessions(), Kill())
        .main(args)
}
