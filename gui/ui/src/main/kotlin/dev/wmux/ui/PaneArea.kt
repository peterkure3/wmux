package dev.wmux.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.foundation.gestures.Orientation as GestureOrientation
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import dev.wmux.core.Orientation
import dev.wmux.core.PaneNode
import dev.wmux.core.net.WmuxDaemonClient

/** Renders a PaneNode tree: leaves become attached surface panes, splits recurse. */
@Composable
fun PaneArea(client: WmuxDaemonClient, root: PaneNode, activePaneId: String?, onPaneFocus: (String) -> Unit) {
    when (root) {
        is PaneNode.Leaf -> TerminalPane(
            client = client,
            leaf = root,
            isActive = root.surfaceId == activePaneId,
            onFocus = { onPaneFocus(root.surfaceId) },
        )
        is PaneNode.Split -> SplitContainer(client, root, activePaneId, onPaneFocus)
    }
}

@Composable
private fun SplitContainer(
    client: WmuxDaemonClient,
    split: PaneNode.Split,
    activePaneId: String?,
    onPaneFocus: (String) -> Unit,
) {
    var ratio by remember { mutableStateOf(split.ratio) }
    val isHorizontal = split.orientation == Orientation.HORIZONTAL

    if (isHorizontal) {
        Row(Modifier.fillMaxSize()) {
            Box(Modifier.weight(ratio).fillMaxHeight()) { PaneArea(client, split.first, activePaneId, onPaneFocus) }
            Divider(
                orientation = GestureOrientation.Horizontal,
                onDrag = { delta -> ratio = (ratio + delta).coerceIn(0.1f, 0.9f) },
            )
            Box(Modifier.weight(1f - ratio).fillMaxHeight()) { PaneArea(client, split.second, activePaneId, onPaneFocus) }
        }
    } else {
        Column(Modifier.fillMaxSize()) {
            Box(Modifier.weight(ratio).fillMaxWidth()) { PaneArea(client, split.first, activePaneId, onPaneFocus) }
            Divider(
                orientation = GestureOrientation.Vertical,
                onDrag = { delta -> ratio = (ratio + delta).coerceIn(0.1f, 0.9f) },
            )
            Box(Modifier.weight(1f - ratio).fillMaxWidth()) { PaneArea(client, split.second, activePaneId, onPaneFocus) }
        }
    }
}

@Composable
private fun Divider(orientation: GestureOrientation, onDrag: (Float) -> Unit) {
    val modifier = if (orientation == GestureOrientation.Horizontal) {
        Modifier.width(4.dp).fillMaxHeight()
    } else {
        Modifier.height(4.dp).fillMaxWidth()
    }
    Box(
        modifier
            .background(Color(0xFF3C3C3C))
            .draggable(
                orientation = orientation,
                state = rememberDraggableState { delta -> onDrag(delta / 1000f) },
            ),
    )
}
