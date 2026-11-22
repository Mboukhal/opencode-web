package dev.mboukhal.ocmobile

import android.os.Bundle
import android.widget.Button
import android.widget.EditText
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/**
 * One OpenCode session: send a prompt and watch the agent work.
 *
 * Output streams from the server's event bus; leaving the screen cancels the
 * stream but the turn keeps running on the server, so it can be resumed from
 * the terminal or any other paired client.
 */
class ChatActivity : AppCompatActivity() {

    private lateinit var transcript: TextView
    private lateinit var scroll: ScrollView
    private lateinit var prompt: EditText
    private lateinit var send: Button
    private lateinit var stop: Button

    private var sessionId: String = ""
    private var streamJob: Job? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_chat)

        sessionId = intent.getStringExtra(EXTRA_SESSION).orEmpty()
        title = intent.getStringExtra(EXTRA_TITLE) ?: getString(R.string.chat_title)

        transcript = findViewById(R.id.transcript)
        scroll = findViewById(R.id.scroll)
        prompt = findViewById(R.id.prompt)
        send = findViewById(R.id.send)
        stop = findViewById(R.id.stop)

        send.setOnClickListener { sendPrompt() }
        stop.setOnClickListener { interrupt() }

        if (sessionId.isEmpty()) {
            append(getString(R.string.err_no_session))
            send.isEnabled = false
            return
        }
        startStreaming()
    }

    private fun startStreaming() {
        val client = (application as App).client() ?: return
        streamJob?.cancel()
        streamJob = lifecycleScope.launch {
            try {
                client.events(sessionId).collect { ev -> render(ev) }
            } catch (e: Exception) {
                append("⚠ ${e.message}")
            }
        }
    }

    private fun sendPrompt() {
        val text = prompt.text.toString().trim()
        if (text.isEmpty() || sessionId.isEmpty()) return
        val client = (application as App).client() ?: return
        prompt.setText("")
        append("\n────────\nYou: $text\n\n")
        lifecycleScope.launch {
            try {
                client.prompt(sessionId, text)
            } catch (e: Exception) {
                append("⚠ ${e.message}")
            }
        }
    }

    private fun interrupt() {
        val client = (application as App).client() ?: return
        lifecycleScope.launch {
            try {
                client.interrupt(sessionId)
            } catch (e: Exception) {
                append("⚠ ${e.message}")
            }
        }
    }

    private fun render(ev: StreamEvent) {
        when (ev.type) {
            "text" -> append(ev.text)
            "tool" -> append("\n⚙ ${ev.tool} ${ev.detail}\n")
            "error" -> append("\n⚠ ${ev.error}\n")
            "done" -> {
                if (ev.error.isNotEmpty()) append("\n✖ ${ev.error}\n")
                else if (ev.text == "stopped") append("\n■ stopped\n")
                else append("\n✓ done\n")
            }
        }
    }

    private fun append(s: String) {
        transcript.append(s)
        scroll.post { scroll.fullScroll(ScrollView.FOCUS_DOWN) }
    }

    override fun onDestroy() {
        streamJob?.cancel()
        super.onDestroy()
    }

    companion object {
        const val EXTRA_SESSION = "session"
        const val EXTRA_TITLE = "title"
    }
}
