package dev.wmux.core.net

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader
import java.net.URI
import java.net.URLEncoder
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.nio.charset.StandardCharsets
import java.time.Duration
import java.util.Base64

/** Env var overriding the token file location — mirrors internal/authtoken.EnvVar. */
private const val TOKEN_ENV_VAR = "WMUX_TOKEN_FILE"

/** Env var overriding the daemon address — mirrors cmd/wmux's daemonAddr. */
private const val ADDR_ENV_VAR = "WMUX_ADDR"

/** Header the shared token travels in — mirrors internal/authtoken.HeaderName. */
private const val TOKEN_HEADER = "X-Wmux-Token"

const val DEFAULT_ADDR = "http://127.0.0.1:47823"

/** A stream is kept open this long before its client-side timeout would ever fire. */
private val STREAM_TIMEOUT: Duration = Duration.ofDays(365)

/** Reads the daemon's shared auth token the same way the Go CLI does. */
fun defaultTokenPath(): File {
    System.getenv(TOKEN_ENV_VAR)?.takeIf { it.isNotEmpty() }?.let { return File(it) }
    val home = System.getProperty("user.home") ?: return File("wmux-token")
    return File(File(home, ".wmux"), "token")
}

fun loadToken(path: File = defaultTokenPath()): String =
    if (path.exists()) path.readText().trim() else ""

fun defaultAddr(): String = System.getenv(ADDR_ENV_VAR)?.takeIf { it.isNotEmpty() } ?: DEFAULT_ADDR

/** A daemon response that came back but wasn't the status a call expected. */
class WmuxStatusException(val code: Int, val status: String, val body: String) :
    Exception(if (body.isEmpty()) "daemon returned $status" else "daemon returned $status: $body")

/**
 * Talks to one wmuxd over its local HTTP API — the Kotlin equivalent of
 * internal/client/client.go, scoped to what the GUI and CLI modules need:
 * session lifecycle/listing/events, and surface creation/attach/input.
 * `wmux attach`-style TTY passthrough and debug/pprof routes stay
 * Go-CLI-only and aren't reproduced here.
 *
 * Every call is synchronous (blocking) except [events] and
 * [attachSurfaceCells], which return cold Flows backed by a dedicated
 * reader thread per stream — callers on Compose's UI thread should launch
 * these from a coroutine scope, same as the Go TUI treats its SSE/NDJSON
 * subscriptions as background work.
 */
class WmuxDaemonClient(
    val addr: String = defaultAddr(),
    val token: String = loadToken(),
) {
    private val json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = false
    }

    private val http: HttpClient = HttpClient.newBuilder()
        .connectTimeout(Duration.ofSeconds(5))
        .build()

    private fun send(req: HttpRequest): HttpResponse<String> =
        http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8))

    private fun getRequest(path: String): HttpRequest =
        HttpRequest.newBuilder(URI.create(addr + path))
            .header(TOKEN_HEADER, token)
            .timeout(Duration.ofSeconds(15))
            .GET()
            .build()

    private fun postRequest(path: String, body: String?): HttpRequest =
        HttpRequest.newBuilder(URI.create(addr + path))
            .header(TOKEN_HEADER, token)
            .header("Content-Type", "application/json")
            .timeout(Duration.ofSeconds(15))
            .POST(
                if (body != null) HttpRequest.BodyPublishers.ofString(body, StandardCharsets.UTF_8)
                else HttpRequest.BodyPublishers.noBody(),
            )
            .build()

    private inline fun <reified T> get(path: String): T {
        val resp = send(getRequest(path))
        if (resp.statusCode() != 200) throw WmuxStatusException(resp.statusCode(), "HTTP ${resp.statusCode()}", resp.body())
        return json.decodeFromString(resp.body())
    }

    /** POSTs [body] (or no body) and returns the raw response text on a status match. */
    private inline fun <reified B> post(path: String, body: B?, want: Int = 200): String {
        val payload = body?.let { json.encodeToString(it) }
        val resp = send(postRequest(path, payload))
        if (resp.statusCode() != want) throw WmuxStatusException(resp.statusCode(), "HTTP ${resp.statusCode()}", resp.body())
        return resp.body()
    }

    /** GET /healthz — no auth needed; true if something answered 200. */
    fun healthz(): Boolean = try {
        send(HttpRequest.newBuilder(URI.create("$addr/healthz")).GET().build()).statusCode() == 200
    } catch (_: Exception) {
        false
    }

    /** GET /identify — reports app/version/protocol so a caller can name a mismatch. */
    fun identify(): IdentifyResponse = get("/identify")

    /** GET /sessions — snapshot on connect, before /events starts pushing deltas. */
    fun listSessions(): List<SessionInfo> = get("/sessions")

    /** POST /sessions — a daemon-owned, piped (no TTY) session. */
    fun spawnSession(req: NewSessionRequest): SessionInfo = json.decodeFromString(post("/sessions", req))

    /** POST /sessions/register — tracking-only registration for a caller-owned TTY. */
    fun registerSession(req: RegisterSessionRequest): SessionInfo = json.decodeFromString(post("/sessions/register", req))

    /** POST /sessions/deregister. */
    fun deregisterSession(id: String) {
        post("/sessions/deregister", DeregisterSessionRequest(id))
    }

    /** POST /sessions/close — kills a session's tracked process. */
    fun closeSession(id: String) {
        post("/sessions/close", CloseSessionRequest(id))
    }

    /** POST /sessions/prune. */
    fun prune(): PruneResult = json.decodeFromString(post<Unit>("/sessions/prune", null))

    /** POST /surfaces — creates a daemon-owned ConPTY session. */
    fun newSurface(req: NewSurfaceRequest): SessionInfo = json.decodeFromString(post("/surfaces", req))

    /** POST /surfaces/input — raw keystrokes, base64-encoded per the wire format. */
    fun inputSurface(id: String, data: ByteArray) {
        post("/surfaces/input", SurfaceInputRequest(id, Base64.getEncoder().encodeToString(data)))
    }

    /** POST /surfaces/resize. */
    fun resizeSurface(id: String, cols: Int, rows: Int) {
        post("/surfaces/resize", SurfaceResizeRequest(id, cols, rows))
    }

    /**
     * GET /events (SSE) as a cold Flow. Each daemon push is one
     * `data: {json}\n\n` line pair; a blank line just separates events, so
     * reacting to lines prefixed `data:` is sufficient without buffering
     * multi-line payloads the daemon never sends.
     */
    fun events(): Flow<EventEnvelope> = callbackFlow {
        val resp = http.send(
            HttpRequest.newBuilder(URI.create("$addr/events"))
                .header(TOKEN_HEADER, token)
                .timeout(STREAM_TIMEOUT)
                .GET()
                .build(),
            HttpResponse.BodyHandlers.ofInputStream(),
        )
        val stream = resp.body()
        val reader = BufferedReader(InputStreamReader(stream, StandardCharsets.UTF_8))
        val thread = Thread({
            try {
                while (true) {
                    val line = reader.readLine() ?: break
                    if (!line.startsWith("data:")) continue
                    val payload = line.removePrefix("data:").trim()
                    if (payload.isEmpty()) continue
                    runCatching { json.decodeFromString<EventEnvelope>(payload) }.onSuccess { trySend(it) }
                }
            } catch (_: Exception) {
                // Stream closed (daemon restart, network drop) or awaitClose tore it down.
            } finally {
                close()
            }
        }, "wmux-events")
        thread.isDaemon = true
        thread.start()
        awaitClose { runCatching { stream.close() } }
    }.flowOn(Dispatchers.IO) // the initial http.send() above is a blocking call

    /**
     * GET /surfaces/attach?id=..&mode=cells (NDJSON) as a cold Flow, per
     * surface. Mirrors internal/tui/pane.go's attachSurfaceCellsPane: a
     * failure to even reach the stream is reported as an immediate exit
     * frame rather than an error, since there's nothing else useful a pane
     * can show for "never connected."
     */
    fun attachSurfaceCells(id: String): Flow<CellsFrame> = callbackFlow {
        val encodedId = URLEncoder.encode(id, "UTF-8")
        val resp = try {
            http.send(
                HttpRequest.newBuilder(URI.create("$addr/surfaces/attach?id=$encodedId&mode=cells"))
                    .header(TOKEN_HEADER, token)
                    .timeout(STREAM_TIMEOUT)
                    .GET()
                    .build(),
                HttpResponse.BodyHandlers.ofInputStream(),
            )
        } catch (_: Exception) {
            trySend(CellsFrame(type = CellsFrame.TYPE_EXIT))
            close()
            return@callbackFlow
        }
        val stream = resp.body()
        val reader = BufferedReader(InputStreamReader(stream, StandardCharsets.UTF_8))
        val thread = Thread({
            try {
                while (true) {
                    val line = reader.readLine() ?: break
                    if (line.isBlank()) continue
                    val frame = runCatching { json.decodeFromString<CellsFrame>(line) }.getOrNull()
                        ?: CellsFrame(type = CellsFrame.TYPE_EXIT)
                    trySend(frame)
                    if (frame.type == CellsFrame.TYPE_EXIT) break
                }
            } catch (_: Exception) {
                trySend(CellsFrame(type = CellsFrame.TYPE_EXIT))
            } finally {
                close()
            }
        }, "wmux-surface-$id")
        thread.isDaemon = true
        thread.start()
        awaitClose { runCatching { stream.close() } }
    }.flowOn(Dispatchers.IO) // the initial http.send() above is a blocking call
}
