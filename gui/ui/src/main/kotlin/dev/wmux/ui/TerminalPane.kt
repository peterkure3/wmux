package dev.wmux.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.focusable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.input.key.utf16CodePoint
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import dev.wmux.core.PaneNode
import dev.wmux.core.SurfacePane
import dev.wmux.core.net.Run
import dev.wmux.core.net.WmuxDaemonClient

private val ACTIVE_BORDER = Color(0xFF007ACC)
private val INACTIVE_BORDER = Color(0xFF2D2D2D)
private val DEFAULT_FG = Color(0xFFD4D4D4)
private val DEFAULT_BG = Color(0xFF0C0C0C)

/**
 * A single leaf pane attached to one daemon-owned surface: opens the
 * mode=cells stream on first composition, sends keystrokes back over
 * POST /surfaces/input, and detaches (not closes — the surface outlives
 * this window) when it leaves the tree.
 */
@Composable
fun TerminalPane(
    client: WmuxDaemonClient,
    leaf: PaneNode.Leaf,
    isActive: Boolean,
    onFocus: () -> Unit,
) {
    val inputFocus = remember { FocusRequester() }

    val pane = remember(leaf.surfaceId) { SurfacePane(client, leaf.surfaceId) }
    DisposableEffect(leaf.surfaceId) {
        onDispose { pane.close() }
    }

    var revision by remember(leaf.surfaceId) { mutableStateOf(0L) }
    DisposableEffect(leaf.surfaceId) {
        val listener = { revision = pane.revision }
        pane.onUpdate(listener)
        onDispose { pane.removeUpdateListener(listener) }
    }

    // Keyed on both isActive and surfaceId: switching between two
    // single-pane sessions can leave isActive's own value unchanged, so a
    // LaunchedEffect keyed on isActive alone would never refire on that
    // kind of switch. surfaceId always changes when the attached surface
    // changes, which is what actually means "focus needs re-establishing."
    LaunchedEffect(isActive, leaf.surfaceId) {
        if (isActive) {
            repeat(10) {
                inputFocus.requestFocus()
                withFrameNanos {}
            }
        }
    }

    Column(
        Modifier
            .fillMaxSize()
            .border(1.dp, if (isActive) ACTIVE_BORDER else INACTIVE_BORDER)
            .background(DEFAULT_BG)
            .pointerInput(Unit) {
                awaitEachGesture {
                    awaitFirstDown(pass = PointerEventPass.Initial)
                    onFocus()
                }
            },
    ) {
        PaneTabBar(leaf, isActive, pane)
        Box(Modifier.weight(1f).fillMaxWidth()) {
            // Reading `revision` subscribes this composition to frame updates.
            val content: AnnotatedString = remember(revision) { renderGrid(pane) }
            Box(
                Modifier
                    .fillMaxSize()
                    .padding(8.dp)
                    .focusRequester(inputFocus)
                    .focusable()
                    .onKeyEvent { event ->
                        if (!isActive) return@onKeyEvent false
                        if (event.isCtrlPressed && event.key == Key.Tab) return@onKeyEvent false
                        if (event.type != KeyEventType.KeyDown) return@onKeyEvent false
                        val payload = keyToVt(event.key, event.utf16CodePoint)
                        if (payload != null) {
                            pane.sendText(payload)
                            true
                        } else {
                            false
                        }
                    },
            ) {
                Text(
                    text = content,
                    color = DEFAULT_FG,
                    fontFamily = FontFamily.Monospace,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
        }
    }
}

/** Maps a key press to the byte sequence a terminal expects, or null to let it bubble. */
private fun keyToVt(key: Key, codePoint: Int): String? {
    val esc = ""
    return when (key) {
        Key.Enter -> "\r"
        Key.Backspace -> ""
        Key.Tab -> "\t"
        Key.Escape -> esc
        Key.DirectionUp -> "$esc[A"
        Key.DirectionDown -> "$esc[B"
        Key.DirectionRight -> "$esc[C"
        Key.DirectionLeft -> "$esc[D"
        Key.Home -> "$esc[H"
        Key.MoveEnd -> "$esc[F"
        Key.Delete -> "$esc[3~"
        Key.PageUp -> "$esc[5~"
        Key.PageDown -> "$esc[6~"
        else -> if (codePoint >= 0x20 && codePoint != 0x7F) {
            String(Character.toChars(codePoint))
        } else {
            null
        }
    }
}

/**
 * Draws exactly [SurfacePane.rows] lines of [SurfacePane.cols] columns from
 * the pane's current grid, marking the cursor cell with reverse video when
 * visible — the same substitute internal/tui/pane.go's render() uses,
 * since there's no way to place a native text-cursor from styled runs.
 */
private fun renderGrid(pane: SurfacePane): AnnotatedString = buildAnnotatedString {
    val grid = pane.grid
    val cursor = pane.cursor
    val showCursor = pane.cursorVisible
    for (y in 0 until pane.rows) {
        val row = grid.getOrNull(y)
        if (row != null) {
            for (run in row.runs) {
                if (showCursor && cursor.y == y && cursor.x in run.x until (run.x + run.text.length)) {
                    // Cursor cell falls inside this run: split it so only that one cell reverses.
                    val cursorOffset = cursor.x - run.x
                    if (cursorOffset > 0) withStyle(run.toSpanStyle()) { append(run.text.substring(0, cursorOffset)) }
                    withStyle(run.toSpanStyle().copy(background = DEFAULT_FG, color = DEFAULT_BG)) {
                        append(run.text[cursorOffset])
                    }
                    if (cursorOffset + 1 < run.text.length) {
                        withStyle(run.toSpanStyle()) { append(run.text.substring(cursorOffset + 1)) }
                    }
                } else {
                    withStyle(run.toSpanStyle()) { append(run.text) }
                }
            }
        }
        if (y < pane.rows - 1) append('\n')
    }
}

private fun Run.toSpanStyle(): SpanStyle = SpanStyle(
    color = fg.takeIf { it.isNotEmpty() }?.let(::parseHexColor) ?: DEFAULT_FG,
    background = bg.takeIf { it.isNotEmpty() }?.let(::parseHexColor) ?: Color.Unspecified,
    fontWeight = if (attrs and Run.ATTR_BOLD != 0) FontWeight.Bold else null,
    fontStyle = if (attrs and Run.ATTR_ITALIC != 0) FontStyle.Italic else null,
    textDecoration = if (attrs and Run.ATTR_UNDERLINE != 0 || attrs and Run.ATTR_STRIKETHROUGH != 0) {
        TextDecoration.combine(
            listOfNotNull(
                TextDecoration.Underline.takeIf { attrs and Run.ATTR_UNDERLINE != 0 },
                TextDecoration.LineThrough.takeIf { attrs and Run.ATTR_STRIKETHROUGH != 0 },
            ),
        )
    } else {
        null
    },
).let {
    if (attrs and Run.ATTR_REVERSE != 0) it.copy(color = it.background.takeIf { c -> c != Color.Unspecified } ?: DEFAULT_BG, background = it.color)
    else it
}.let {
    if (attrs and Run.ATTR_FAINT != 0) it.copy(color = it.color.copy(alpha = 0.6f)) else it
}

/** Parses a "#rrggbb" wire color; null on anything else. */
private fun parseHexColor(hex: String): Color? {
    val h = hex.removePrefix("#")
    if (h.length != 6) return null
    val rgb = h.toIntOrNull(16) ?: return null
    return Color(0xFF000000.toInt() or rgb)
}

@Composable
private fun PaneTabBar(leaf: PaneNode.Leaf, isActive: Boolean, pane: SurfacePane) {
    Row(
        Modifier
            .fillMaxWidth()
            .height(28.dp)
            .background(if (isActive) Color(0xFF37373D) else Color(0xFF252526))
            .padding(horizontal = 8.dp),
    ) {
        val status = if (pane.exited) "  [exited]" else ""
        Text(
            text = leaf.surfaceId + status,
            color = if (isActive) Color(0xFFFFFFFF) else Color(0xFF999999),
            style = MaterialTheme.typography.labelSmall,
            modifier = Modifier.align(Alignment.CenterVertically),
        )
    }
}
