package dev.wmux.app

import androidx.compose.ui.window.Window
import androidx.compose.ui.window.application
import dev.wmux.core.net.WmuxDaemonClient
import dev.wmux.ui.WmuxApp

fun main(args: Array<String>) {
    if (args.isNotEmpty()) {
        // Delegate to the Clikt CLI for `wmux new`, `wmux list`, `wmux kill`, etc.
        dev.wmux.cli.main(args)
        return
    }

    val client = WmuxDaemonClient()

    application {
        Window(onCloseRequest = ::exitApplication, title = "wmux") {
            WmuxApp(client)
        }
    }
}
