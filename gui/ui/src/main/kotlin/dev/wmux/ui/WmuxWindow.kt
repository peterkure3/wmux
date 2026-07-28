package dev.wmux.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isShiftPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.unit.dp
import dev.wmux.core.Orientation
import dev.wmux.core.PaneNode
import dev.wmux.core.leaves
import dev.wmux.core.without
import dev.wmux.core.net.EventEnvelope
import dev.wmux.core.net.NewSurfaceRequest
import dev.wmux.core.net.SessionInfo
import dev.wmux.core.net.WmuxDaemonClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private enum class ConnectionState { CONNECTING, CONNECTED, RECONNECTING }

/**
 * Top-level window: sidebar is a fixed-width sibling column, independent
 * of the split-pane tree in the main area — same layout as v1's scaffold,
 * now driven by the daemon's live session list instead of a locally
 * persisted `Session`. Ctrl+Tab / Ctrl+Shift+Tab cycle focus through open
 * panes; plain Tab goes to the shell in the active pane.
 */
@Composable
fun WmuxWindow(client: WmuxDaemonClient) {
    var sessions by remember { mutableStateOf<List<SessionInfo>>(emptyList()) }
    var paneTree by remember { mutableStateOf<PaneNode?>(null) }
    var activeSurfaceId by remember { mutableStateOf<String?>(null) }
    var unreadIds by remember { mutableStateOf<Set<String>>(emptySet()) }
    var connectionState by remember { mutableStateOf(ConnectionState.CONNECTING) }
    var showNewPaneDialog by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(client) {
        while (isActive) {
            runCatching { withContext(Dispatchers.IO) { client.listSessions() } }.onSuccess { sessions = it }
            try {
                connectionState = ConnectionState.CONNECTED
                // client.events() itself only sets up a background reader
                // thread; collect() suspends on the resulting channel
                // rather than blocking, so this doesn't need Dispatchers.IO.
                client.events().collect { evt ->
                    when (evt.type) {
                        EventEnvelope.TYPE_SESSIONS -> evt.sessions?.let { sessions = it }
                        EventEnvelope.TYPE_NOTIFY -> evt.notify?.let { unreadIds = unreadIds + it.sessionId }
                    }
                }
            } catch (_: Exception) {
                // Reconnect below after a short backoff.
            }
            connectionState = ConnectionState.RECONNECTING
            delay(2000)
        }
    }

    fun openSurface(session: SessionInfo) {
        unreadIds = unreadIds - session.id
        activeSurfaceId = session.id
        val tree = paneTree
        if (tree == null) {
            paneTree = PaneNode.Leaf(session.id)
        } else if (tree.leaves().none { it.surfaceId == session.id }) {
            paneTree = PaneNode.Split(Orientation.HORIZONTAL, 0.5f, tree, PaneNode.Leaf(session.id))
        }
    }

    fun closeSession(session: SessionInfo) {
        scope.launch { runCatching { withContext(Dispatchers.IO) { client.closeSession(session.id) } } }
        paneTree = paneTree?.without(session.id)
        if (activeSurfaceId == session.id) activeSurfaceId = paneTree?.leaves()?.firstOrNull()?.surfaceId
        unreadIds = unreadIds - session.id
    }

    fun cyclePane(backward: Boolean) {
        val ids = paneTree?.leaves()?.map { it.surfaceId } ?: return
        if (ids.isEmpty()) return
        val current = ids.indexOf(activeSurfaceId).coerceAtLeast(0)
        val step = if (backward) ids.size - 1 else 1
        activeSurfaceId = ids[(current + step) % ids.size]
    }

    MaterialTheme {
        Column(Modifier.fillMaxSize()) {
            Row(Modifier.weight(1f).fillMaxWidth()) {
                Sidebar(
                    sessions = sessions,
                    activeSurfaceId = activeSurfaceId,
                    unreadIds = unreadIds,
                    onSelect = ::openSurface,
                    onClose = ::closeSession,
                    onNewPane = { showNewPaneDialog = true },
                    modifier = Modifier.width(260.dp).fillMaxHeight(),
                )
                Box(
                    // No .focusable()/.focusRequester() here on purpose — see
                    // the original scaffold's note: onPreviewKeyEvent fires
                    // for any ancestor of whichever pane currently holds
                    // real focus, and requesting focus here would steal it
                    // away from the active pane's TerminalPane on every
                    // session switch.
                    Modifier
                        .weight(1f)
                        .fillMaxHeight()
                        .onPreviewKeyEvent { event ->
                            if (event.type == KeyEventType.KeyDown && event.key == Key.Tab && event.isCtrlPressed) {
                                cyclePane(backward = event.isShiftPressed)
                                true
                            } else {
                                false
                            }
                        },
                ) {
                    val tree = paneTree
                    if (tree != null) {
                        PaneArea(client, tree, activeSurfaceId) { activeSurfaceId = it }
                    } else {
                        Text(
                            "No pane open — select a session or \u201c+ new\u201d",
                            modifier = Modifier.padding(16.dp),
                        )
                    }
                }
            }
            StatusBar(client.addr, connectionState, sessions.size)
        }
    }

    if (showNewPaneDialog) {
        NewPaneDialog(
            onDismiss = { showNewPaneDialog = false },
            onCreate = { id, cwd, command, native, distro ->
                showNewPaneDialog = false
                scope.launch {
                    val info = runCatching {
                        withContext(Dispatchers.IO) {
                            client.newSurface(NewSurfaceRequest(id = id, cwd = cwd, command = command, native = native, distro = distro))
                        }
                    }.getOrNull()
                    if (info != null) openSurface(info)
                }
            },
        )
    }
}

@Composable
private fun StatusBar(addr: String, state: ConnectionState, sessionCount: Int) {
    Row(
        Modifier
            .fillMaxWidth()
            .height(28.dp)
            .background(Color(0xFF1E1E1E))
            .padding(horizontal = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        val (label, color) = when (state) {
            ConnectionState.CONNECTED -> "connected  $addr" to Color(0xFF3FB950)
            ConnectionState.CONNECTING -> "connecting…  $addr" to Color(0xFFD4A72C)
            ConnectionState.RECONNECTING -> "reconnecting…  $addr" to Color(0xFFF85149)
        }
        Text(text = "$label   •   $sessionCount sessions", color = color, style = MaterialTheme.typography.labelSmall)
    }
}

@Composable
private fun NewPaneDialog(
    onDismiss: () -> Unit,
    onCreate: (id: String, cwd: String, command: String, native: Boolean, distro: String) -> Unit,
) {
    var id by remember { mutableStateOf("") }
    var cwd by remember { mutableStateOf(System.getProperty("user.dir") ?: "") }
    var command by remember { mutableStateOf("") }
    var native by remember { mutableStateOf(true) }
    var distro by remember { mutableStateOf("") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New pane") },
        text = {
            Column {
                OutlinedTextField(value = id, onValueChange = { id = it }, label = { Text("id") })
                OutlinedTextField(value = cwd, onValueChange = { cwd = it }, label = { Text("cwd") })
                OutlinedTextField(value = command, onValueChange = { command = it }, label = { Text("command (e.g. claude, pwsh)") })
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = native, onCheckedChange = { native = it })
                    Text("native (uncheck for WSL)")
                }
                if (!native) {
                    OutlinedTextField(value = distro, onValueChange = { distro = it }, label = { Text("distro (optional)") })
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onCreate(id.ifBlank { cwd.substringAfterLast('\\').substringAfterLast('/') }, cwd, command, native, distro) },
                enabled = command.isNotBlank() && cwd.isNotBlank(),
            ) { Text("Create") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
