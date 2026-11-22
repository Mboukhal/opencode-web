package dev.mboukhal.ocmobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ValidationTest {

    private fun err(origin: String, password: String = "pw", allow: Boolean = false): ConfigError? =
        (validateConfig(origin, password, allow).exceptionOrNull() as? ConfigException)?.error

    @Test
    fun `https origin is normalised`() {
        val r = validateConfig("  https://opencode.mboukhal.dev/  ", "pw", false)
        assertEquals("https://opencode.mboukhal.dev", r.getOrThrow())
    }

    @Test
    fun `port is preserved`() {
        val r = validateConfig("https://host.example:8443", "pw", false)
        assertEquals("https://host.example:8443", r.getOrThrow())
    }

    @Test
    fun `cleartext is rejected unless allowed`() {
        assertEquals(ConfigError.CleartextNotAllowed, err("http://192.168.1.17:4700"))
        val r = validateConfig("http://192.168.1.17:4700", "pw", true)
        assertEquals("http://192.168.1.17:4700", r.getOrThrow())
    }

    @Test
    fun `empty inputs are reported`() {
        assertEquals(ConfigError.EmptyOrigin, err(""))
        assertEquals(ConfigError.EmptyPassword, err("https://h.example", password = ""))
    }

    @Test
    fun `paths and unknown schemes are rejected`() {
        assertEquals(ConfigError.BadOrigin, err("https://h.example/api"))
        assertEquals(ConfigError.BadOrigin, err("ftp://h.example"))
        assertEquals(ConfigError.BadOrigin, err("not a url"))
    }
}

class EventTranslationTest {

    @Test
    fun `text delta becomes text`() {
        val ev = translateEvent(
            """{"type":"session.text.delta","data":{"sessionID":"s1","ordinal":0,"delta":"hi"}}""",
            "s1",
        )
        assertEquals(StreamEvent("text", text = "hi"), ev)
    }

    @Test
    fun `events for another session are ignored`() {
        val ev = translateEvent(
            """{"type":"session.text.delta","data":{"sessionID":"other","ordinal":0,"delta":"x"}}""",
            "s1",
        )
        assertNull(ev)
    }

    @Test
    fun `failure ends the run with its message`() {
        val ev = translateEvent(
            """{"type":"session.execution.failed","data":{"sessionID":"s1","error":{"type":"provider.auth","message":"User not found."}}}""",
            "s1",
        )
        assertEquals("done", ev?.type)
        assertEquals("User not found.", ev?.error)
    }

    @Test
    fun `success ends the run`() {
        val ev = translateEvent(
            """{"type":"session.execution.succeeded","data":{"sessionID":"s1"}}""",
            "s1",
        )
        assertEquals("done", ev?.type)
        assertTrue(ev!!.error.isEmpty())
    }

    @Test
    fun `tool call carries its detail`() {
        val ev = translateEvent(
            """{"type":"session.tool.called","data":{"sessionID":"s1","id":"t","input":{"command":"ls -la"},"native":{"name":"shell"}}}""",
            "s1",
        )
        assertEquals("tool", ev?.type)
        assertEquals("shell", ev?.tool)
        assertEquals("ls -la", ev?.detail)
    }

    @Test
    fun `malformed payload is ignored`() {
        assertNull(translateEvent("not json", "s1"))
    }
}
