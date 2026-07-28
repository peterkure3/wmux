package dev.wmux.core.net

import kotlinx.serialization.Serializable

/**
 * Wire types for wmuxd's local HTTP API, mirroring internal/proto/proto.go
 * on the Go side field-for-field. Kept in one file for the same reason the
 * Go package is one file: these are shared vocabulary between wmuxd and
 * every client, not a place for per-feature drift.
 */

const val PROTOCOL_VERSION = 1

@Serializable
data class IdentifyResponse(
    val app: String = "",
    val version: String = "",
    val protocol: Int = 0,
    val pid: Int = 0,
    val sessions: Int = 0,
)

@Serializable
data class NotifyEvent(
    val sessionId: String,
    val title: String = "",
    val body: String = "",
    val kind: String = "",
    val time: String = "",
    val dropped: Int = 0,
) {
    /** One-line human-readable note, matching Go's NotifyEvent.Display(). */
    fun display(): String = when {
        title.isNotEmpty() && body.isNotEmpty() -> "$title: $body"
        title.isNotEmpty() -> title
        else -> body
    }
}

/**
 * Envelope streamed over GET /events. Exactly one of [notify]/[sessions] is
 * set, matching [type] — "notify" or "sessions". Modeled as one flat data
 * class (not a sealed hierarchy) because that's the actual wire shape: a
 * single JSON object with a type tag and optional fields, not a tagged
 * union kotlinx.serialization would need a custom discriminator for.
 */
@Serializable
data class EventEnvelope(
    val type: String,
    val notify: NotifyEvent? = null,
    val sessions: List<SessionInfo>? = null,
) {
    companion object {
        const val TYPE_NOTIFY = "notify"
        const val TYPE_SESSIONS = "sessions"
    }
}

@Serializable
data class SessionInfo(
    val id: String,
    val cwd: String = "",
    val branch: String = "",
    val ports: List<Int> = emptyList(),
    val lastNote: String = "",
    val running: Boolean = false,
    val pid: Int = 0,
    val native: Boolean = false,
    val surface: Boolean = false,
)

@Serializable
data class NewSessionRequest(
    val id: String,
    val cwd: String,
    val command: String,
    val distro: String = "",
)

@Serializable
data class RegisterSessionRequest(
    val id: String,
    val cwd: String,
    val distro: String = "",
    val pid: Int = 0,
    val native: Boolean = false,
)

@Serializable
data class DeregisterSessionRequest(val id: String)

@Serializable
data class CloseSessionRequest(val id: String)

@Serializable
data class PruneResult(val removed: List<String> = emptyList())

/**
 * Body for POST /surfaces — a daemon-owned ConPTY session. Cols/Rows
 * default to 0 here (omitted on the wire via the encoder's
 * encodeDefaults=false setting) so the daemon applies its own 120x30
 * default rather than us duplicating that constant client-side.
 */
@Serializable
data class NewSurfaceRequest(
    val id: String,
    val cwd: String,
    val command: String,
    val distro: String = "",
    val native: Boolean = false,
    val cols: Int = 0,
    val rows: Int = 0,
)

@Serializable
data class Pos(val x: Int = 0, val y: Int = 0)

/**
 * A styled run of same-style cells on one row. fg/bg are "#rrggbb" (empty
 * = default color), already resolved server-side — the client never sees
 * a palette index or raw SGR code, only a literal color to draw.
 */
@Serializable
data class Run(
    val x: Int = 0,
    val text: String = "",
    val fg: String = "",
    val bg: String = "",
    val attrs: Int = 0,
) {
    companion object {
        const val ATTR_BOLD = 1
        const val ATTR_FAINT = 1 shl 1
        const val ATTR_ITALIC = 1 shl 2
        const val ATTR_UNDERLINE = 1 shl 3
        const val ATTR_BLINK = 1 shl 4
        const val ATTR_REVERSE = 1 shl 5
        const val ATTR_STRIKETHROUGH = 1 shl 6
    }
}

@Serializable
data class RowUpdate(val y: Int, val runs: List<Run> = emptyList())

/**
 * One JSON line from GET /surfaces/attach?mode=cells. "replay" carries
 * every row and is sent first and after every resize; "update" carries
 * only the rows that changed; "exit" ends the stream.
 */
@Serializable
data class CellsFrame(
    val type: String,
    val rows: List<RowUpdate> = emptyList(),
    val cursor: Pos = Pos(),
    val cursorVisible: Boolean = false,
    val cols: Int = 0,
    val rowCount: Int = 0,
) {
    companion object {
        const val TYPE_REPLAY = "replay"
        const val TYPE_UPDATE = "update"
        const val TYPE_EXIT = "exit"
    }
}

/**
 * Body for POST /surfaces/input. [data] is base64 text, not raw bytes —
 * Go's encoding/json marshals a []byte field as a base64 string, so that's
 * the wire shape a `String` here has to reproduce; use
 * [dev.wmux.core.net.WmuxDaemonClient.inputSurface] rather than
 * constructing this by hand.
 */
@Serializable
data class SurfaceInputRequest(val id: String, val data: String)

@Serializable
data class SurfaceResizeRequest(val id: String, val cols: Int, val rows: Int)
