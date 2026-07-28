package dev.wmux.core

import dev.wmux.core.net.CellsFrame
import dev.wmux.core.net.Pos
import dev.wmux.core.net.RowUpdate
import dev.wmux.core.net.WmuxDaemonClient
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import java.util.concurrent.CopyOnWriteArrayList

/**
 * One attached surface's known screen state, kept current by [applyFrame]
 * as [CellsFrame]s arrive over GET /surfaces/attach?mode=cells — the
 * Kotlin counterpart of internal/tui/pane.go's tuiSurfacePane. Framework
 * agnostic on purpose (no Compose types here) so `core` stays portable;
 * the `ui` module reads [grid]/[cursor]/[revision] to render.
 */
class SurfacePane(private val client: WmuxDaemonClient, val id: String) : AutoCloseable {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val job: Job

    var cols: Int = 0
        private set
    var rows: Int = 0
        private set
    var grid: List<RowUpdate> = emptyList()
        private set
    var cursor: Pos = Pos()
        private set
    var cursorVisible: Boolean = false
        private set
    var exited: Boolean = false
        private set

    /** Bumped on every applied frame; UI reads this to know when to recompose. */
    @Volatile
    var revision: Long = 0L
        private set

    private val listeners = CopyOnWriteArrayList<() -> Unit>()

    init {
        job = scope.launch {
            client.attachSurfaceCells(id).collect { frame ->
                applyFrame(frame)
                revision++
                listeners.forEach { it() }
            }
        }
    }

    fun onUpdate(listener: () -> Unit) {
        listeners.add(listener)
    }

    fun removeUpdateListener(listener: () -> Unit) {
        listeners.remove(listener)
    }

    /** Sends text (already translated to the terminal's expected byte sequence) to the surface. */
    fun sendText(text: String) {
        scope.launch { runCatching { client.inputSurface(id, text.toByteArray(Charsets.UTF_8)) } }
    }

    fun resize(newCols: Int, newRows: Int) {
        scope.launch { runCatching { client.resizeSurface(id, newCols, newRows) } }
    }

    override fun close() {
        job.cancel()
    }

    /** Folds a newly received [CellsFrame] into this pane's state. */
    private fun applyFrame(f: CellsFrame) {
        when (f.type) {
            CellsFrame.TYPE_REPLAY -> {
                cols = f.cols
                rows = f.rowCount
                grid = f.rows
                cursor = f.cursor
                cursorVisible = f.cursorVisible
            }
            CellsFrame.TYPE_UPDATE -> {
                if (f.rows.isNotEmpty()) {
                    val updated = grid.toMutableList()
                    for (row in f.rows) {
                        if (row.y in updated.indices) updated[row.y] = row
                    }
                    grid = updated
                }
                cursor = f.cursor
                cursorVisible = f.cursorVisible
            }
            CellsFrame.TYPE_EXIT -> exited = true
        }
    }
}
