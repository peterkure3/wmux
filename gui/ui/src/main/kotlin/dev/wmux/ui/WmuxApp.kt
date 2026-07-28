package dev.wmux.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import dev.wmux.core.net.WmuxDaemonClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Whole-window entry point: waits for wmuxd to answer GET /healthz before
 * showing the live session/pane UI. Kept in `ui` (not `app`) so the app
 * module's Main.kt stays free of any Compose Material dependency, same
 * split as the original scaffold.
 */
@Composable
fun WmuxApp(client: WmuxDaemonClient) {
    var reachable by remember { mutableStateOf<Boolean?>(null) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(client) {
        while (true) {
            reachable = withContext(Dispatchers.IO) { client.healthz() }
            if (reachable == true) break
            delay(2000)
        }
    }

    MaterialTheme {
        when (reachable) {
            true -> WmuxWindow(client)
            else -> DaemonUnreachable(client.addr) {
                scope.launch { reachable = withContext(Dispatchers.IO) { client.healthz() } }
            }
        }
    }
}

/**
 * Shown until wmuxd answers GET /healthz. Deliberately doesn't launch
 * wmuxd.exe itself — the README's install model already runs it headless
 * at logon (or via Task Scheduler), so fighting that pattern with an
 * auto-launch here would just risk a second daemon instance.
 */
@Composable
private fun DaemonUnreachable(addr: String, onRetry: () -> Unit) {
    Column(
        Modifier.fillMaxSize().padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Text("wmuxd isn't reachable at $addr")
        Spacer(Modifier.height(8.dp))
        Text("Start it (wmuxd.exe, or your Startup/Task Scheduler entry) then retry.")
        Spacer(Modifier.height(16.dp))
        Button(onClick = onRetry) { Text("Retry") }
    }
}
