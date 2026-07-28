package dev.wmux.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import dev.wmux.core.net.SessionInfo

private val RUNNING = Color(0xFF3FB950)
private val EXITED = Color(0xFF6E7681)
private val MUTED = Color(0xFF888888)
private val SELECTED_BG = Color(0xFF2A2D2E)

/**
 * The live session list — the Kotlin GUI's equivalent of `wmux sidebar`'s
 * TUI row anatomy: running dot, id + git branch, cwd tail + ports, and an
 * unread-notification snippet when there is one.
 */
@Composable
fun Sidebar(
    sessions: List<SessionInfo>,
    activeSurfaceId: String?,
    unreadIds: Set<String>,
    onSelect: (SessionInfo) -> Unit,
    onClose: (SessionInfo) -> Unit,
    onNewPane: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(modifier.background(Color(0xFF181818))) {
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text("SESSIONS", color = MUTED, style = MaterialTheme.typography.labelSmall)
            Text(
                "+ new",
                color = Color(0xFF4FC1FF),
                style = MaterialTheme.typography.labelSmall,
                modifier = Modifier.focusProperties { canFocus = false }.clickable { onNewPane() },
            )
        }
        LazyColumn(Modifier.weight(1f)) {
            items(sessions, key = { it.id }) { session ->
                SessionRow(
                    session = session,
                    isActive = session.id == activeSurfaceId,
                    unread = session.id in unreadIds,
                    onSelect = { onSelect(session) },
                    onClose = { onClose(session) },
                )
            }
        }
        val unreadCount = unreadIds.size
        Text(
            "${sessions.size} sessions" + (if (unreadCount > 0) " · $unreadCount unread" else ""),
            color = MUTED,
            style = MaterialTheme.typography.labelSmall,
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
        )
    }
}

@Composable
private fun SessionRow(
    session: SessionInfo,
    isActive: Boolean,
    unread: Boolean,
    onSelect: () -> Unit,
    onClose: () -> Unit,
) {
    Column(
        Modifier
            .fillMaxWidth()
            // clickable() makes its target focusable and grabs keyboard
            // focus by default; sidebar rows never need keyboard focus,
            // and that default steals focus away from the newly-focused
            // pane's terminal input on every session switch.
            .focusProperties { canFocus = false }
            .clickable { onSelect() }
            .background(if (isActive) SELECTED_BG else Color.Transparent)
            .padding(horizontal = 12.dp, vertical = 6.dp),
    ) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Row {
                Text(if (session.running) "●" else "○", color = if (session.running) RUNNING else EXITED)
                Spacer(Modifier.width(6.dp))
                Text(session.id, color = Color.White, style = MaterialTheme.typography.bodyMedium)
                if (session.branch.isNotEmpty()) {
                    Spacer(Modifier.width(6.dp))
                    Text(session.branch, color = MUTED, style = MaterialTheme.typography.labelSmall)
                }
                if (!session.native) {
                    Spacer(Modifier.width(6.dp))
                    Text("wsl", color = MUTED, style = MaterialTheme.typography.labelSmall)
                }
            }
            Text(
                "✕",
                color = MUTED,
                style = MaterialTheme.typography.labelSmall,
                modifier = Modifier.focusProperties { canFocus = false }.clickable { onClose() },
            )
        }
        val portsSuffix = if (session.ports.isNotEmpty()) "   :" + session.ports.joinToString(",") else ""
        Text(cwdTail(session.cwd) + portsSuffix, color = MUTED, style = MaterialTheme.typography.labelSmall)
        if (unread && session.lastNote.isNotEmpty()) {
            Text(
                "✉ " + session.lastNote,
                color = Color(0xFF4FC1FF),
                style = MaterialTheme.typography.labelSmall,
            )
        }
    }
}

/** Shortens a cwd to its last path segment or two, matching the TUI's compact row. */
private fun cwdTail(cwd: String): String {
    val parts = cwd.split('\\', '/').filter { it.isNotEmpty() }
    return if (parts.size <= 2) cwd else "…\\" + parts.takeLast(2).joinToString("\\")
}
