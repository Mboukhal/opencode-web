package dev.mboukhal.ocmobile

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedReader
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.util.Base64
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLHandshakeException

/** One OpenCode session, scoped to the project directory it runs in. */
data class Session(
    val id: String,
    val title: String,
    val directory: String,
    val updated: Long,
)

/** One line of streamed output from the agent. */
data class StreamEvent(
    val type: String,   // text | tool | error | notice | done
    val text: String = "",
    val tool: String = "",
    val detail: String = "",
    val error: String = "",
)

class ApiException(message: String, val status: Int = 0) : IOException(message)

/**
 * Client for the OpenCode v2 HTTP API.
 *
 * Every call carries the server password as HTTP basic auth. HTTPS is required
 * unless the config explicitly allowed cleartext for a LAN server; there is no
 * "trust all certificates" path, so a bad certificate fails the connection.
 */
class OpenCodeClient(private val cfg: ServerConfig) {

    private fun open(path: String): HttpURLConnection {
        if (!cfg.origin.startsWith("https://") && !cfg.allowCleartext) {
            throw ApiException("Refusing plain HTTP: HTTPS is required")
        }
        val conn = URL(cfg.origin + path).openConnection() as HttpURLConnection
        conn.connectTimeout = 15_000
        conn.readTimeout = 60_000
        conn.setRequestProperty("Accept", "application/json")
        val token = Base64.getEncoder()
            .encodeToString("opencode:${cfg.password}".toByteArray(Charsets.UTF_8))
        conn.setRequestProperty("Authorization", "Basic $token")
        return conn
    }

    private fun mapError(conn: HttpURLConnection, body: String): ApiException = when (conn.responseCode) {
        401, 403 -> ApiException("Wrong server password", conn.responseCode)
        404 -> ApiException("Not found on the server (is it an OpenCode server?)", 404)
        else -> ApiException("Server error ${conn.responseCode}: ${body.take(200)}", conn.responseCode)
    }

    /** Verifies the origin and password; returns the server version. */
    suspend fun info(): String = withContext(Dispatchers.IO) {
        val conn = open("/api/info")
        try {
            val code = conn.responseCode
            val body = (if (code in 200..299) conn.inputStream else conn.errorStream)
                ?.bufferedReader()?.use { it.readText() } ?: ""
            if (code !in 200..299) throw mapError(conn, body)
            JSONObject(body).optString("version", "unknown")
        } catch (e: SSLHandshakeException) {
            throw ApiException("TLS failed: the certificate is not trusted for this host")
        } finally {
            conn.disconnect()
        }
    }

    suspend fun sessions(directory: String?): List<Session> = withContext(Dispatchers.IO) {
        val params = mutableMapOf("order" to "desc", "limit" to "50")
        if (!directory.isNullOrBlank()) params["directory"] = directory
        val query = buildingQuery(params)
        val body = get("/api/session?$query")
        val arr = JSONObject(body).optJSONArray("data") ?: JSONArray()
        (0 until arr.length()).mapNotNull { i ->
            val o = arr.optJSONObject(i) ?: return@mapNotNull null
            val loc = o.optJSONObject("location")
            val time = o.optJSONObject("time")
            Session(
                id = o.optString("id"),
                title = o.optString("title").ifBlank { "Untitled session" },
                directory = loc?.optString("directory").orEmpty(),
                updated = time?.optLong("updated", 0) ?: 0,
            )
        }.filter { directory.isNullOrBlank() || it.directory == directory }
    }

    suspend fun createSession(directory: String, title: String): Session = withContext(Dispatchers.IO) {
        val payload = JSONObject()
            .put("location", JSONObject().put("directory", directory))
            .put("title", title)
        val body = post("/api/session", payload.toString())
        val o = JSONObject(body).optJSONObject("data") ?: throw ApiException("Bad create response")
        Session(
            id = o.optString("id"),
            title = o.optString("title").ifBlank { title },
            directory = o.optJSONObject("location")?.optString("directory").orEmpty(),
            updated = o.optJSONObject("time")?.optLong("updated", 0) ?: 0,
        )
    }

    suspend fun prompt(sessionId: String, text: String) = withContext(Dispatchers.IO) {
        val payload = JSONObject().put("text", text)
        post("/api/session/$sessionId/prompt", payload.toString())
        Unit
    }

    suspend fun interrupt(sessionId: String) = withContext(Dispatchers.IO) {
        post("/api/session/$sessionId/interrupt", "{}")
        Unit
    }

    /** Project directories the server knows about. */
    suspend fun projects(): List<String> = withContext(Dispatchers.IO) {
        val body = get("/api/project")
        val arr = JSONArray(body)
        (0 until arr.length()).mapNotNull { i ->
            arr.optJSONObject(i)?.optString("canonical")?.takeIf { it.isNotBlank() }
        }
    }

    private fun get(path: String): String {
        val conn = open(path)
        try {
            val code = conn.responseCode
            val body = (if (code in 200..299) conn.inputStream else conn.errorStream)
                ?.bufferedReader()?.use { it.readText() } ?: ""
            if (code !in 200..299) throw mapError(conn, body)
            return body
        } finally {
            conn.disconnect()
        }
    }

    private fun post(path: String, json: String): String {
        val conn = open(path)
        try {
            conn.requestMethod = "POST"
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(json.toByteArray(Charsets.UTF_8)) }
            val code = conn.responseCode
            val body = (if (code in 200..299) conn.inputStream else conn.errorStream)
                ?.bufferedReader()?.use { it.readText() } ?: ""
            if (code !in 200..299) throw mapError(conn, body)
            return body
        } finally {
            conn.disconnect()
        }
    }

    /**
     * Streams events for one session as they arrive.
     *
     * Cancelling the collecting coroutine disconnects the socket, so leaving
     * the chat screen does not leak a connection.
     */
    fun events(sessionId: String): Flow<StreamEvent> = callbackFlow {
        val conn = open("/api/event")
        conn.setRequestProperty("Accept", "text/event-stream")
        conn.readTimeout = 0
        try {
            val reader: BufferedReader = conn.inputStream.bufferedReader()
            var line = reader.readLine()
            while (line != null) {
                if (line.startsWith("data:")) {
                    val payload = line.removePrefix("data:").trim()
                    if (payload.isNotEmpty()) {
                        translate(payload, sessionId)?.let { trySend(it) }
                    }
                }
                line = reader.readLine()
            }
            close()
        } catch (e: Exception) {
            close(e)
        }
        awaitClose { conn.disconnect() }
    }.flowOn(Dispatchers.IO)

    /** Converts a raw server event into a UI event for [sessionId], or null. */
    fun translate(raw: String, sessionId: String): StreamEvent? = translateEvent(raw, sessionId)

    private fun buildingQuery(params: Map<String, String>): String =
        params.entries.joinToString("&") { "${it.key}=${java.net.URLEncoder.encode(it.value, "UTF-8")}" }
}

/**
 * Pure translation from the OpenCode event envelope to a UI event.
 *
 * Kept top-level so it is unit-testable on the JVM without Android or network.
 * Returns null for events belonging to other sessions or with nothing to show.
 */
fun translateEvent(raw: String, sessionId: String): StreamEvent? {
    val obj = try {
        JSONObject(raw)
    } catch (e: Exception) {
        return null
    }
    val type = obj.optString("type")
    val data = obj.optJSONObject("data") ?: return null
    val sid = data.optString("sessionID")
    if (sid.isEmpty() || (sessionId.isNotEmpty() && sid != sessionId)) return null

    return when (type) {
        "session.text.delta" -> {
            val delta = data.optString("delta")
            if (delta.isEmpty()) null else StreamEvent("text", text = delta)
        }
        "session.tool.called" -> {
            val input = data.optJSONObject("input")
            val detail = sequenceOf("command", "filePath", "path", "pattern", "description")
                .mapNotNull { input?.optString(it)?.takeIf { s -> s.isNotBlank() } }
                .firstOrNull().orEmpty().take(160)
            val name = data.optJSONObject("native")?.optString("name").orEmpty().ifBlank { "tool" }
            StreamEvent("tool", tool = name, detail = detail)
        }
        "session.tool.failed" -> StreamEvent("tool", tool = "tool", detail = "failed")
        "session.step.failed" ->
            StreamEvent("error", error = errorMessage(data))
        "session.execution.failed" ->
            StreamEvent("done", error = errorMessage(data))
        "session.execution.succeeded" -> StreamEvent("done")
        "session.execution.interrupted" -> StreamEvent("done", text = "stopped")
        else -> null
    }
}

private fun errorMessage(data: JSONObject): String {
    val err = data.optJSONObject("error") ?: return "the model call failed"
    return err.optString("message").ifBlank { err.optString("type") }.ifBlank { "the model call failed" }
}
