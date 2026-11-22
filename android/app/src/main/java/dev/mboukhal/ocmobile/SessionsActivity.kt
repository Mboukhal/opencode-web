package dev.mboukhal.ocmobile

import android.content.Intent
import android.os.Bundle
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.Spinner
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Picks a project directory and lists its OpenCode sessions.
 *
 * Sessions are scoped by directory on the server, so this is the same list the
 * terminal and the desktop app see for that project.
 */
class SessionsActivity : AppCompatActivity() {

    private lateinit var projectSpinner: Spinner
    private lateinit var list: RecyclerView
    private lateinit var empty: TextView
    private lateinit var status: TextView
    private val adapter = SessionAdapter { session -> openChat(session) }

    private val app get() = application as App
    private var directory: String = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_sessions)
        title = getString(R.string.sessions_title)

        projectSpinner = findViewById(R.id.project)
        list = findViewById(R.id.sessions)
        empty = findViewById(R.id.empty)
        status = findViewById(R.id.status)

        list.layoutManager = LinearLayoutManager(this)
        list.adapter = adapter

        findViewById<Button>(R.id.refresh).setOnClickListener { loadSessions() }
        findViewById<Button>(R.id.new_session).setOnClickListener { createSession() }
        findViewById<Button>(R.id.disconnect).setOnClickListener {
            app.settings.clear()
            startActivity(Intent(this, ConnectActivity::class.java))
            finish()
        }

        loadProjects()

        projectSpinner.setOnItemSelectedListener(object : android.widget.AdapterView.OnItemSelectedListener {
            override fun onItemSelected(parent: android.widget.AdapterView<*>?, view: android.view.View?, position: Int, id: Long) {
                val picked = parent?.getItemAtPosition(position) as? String ?: return
                if (picked != directory) {
                    directory = picked
                    loadSessions()
                }
            }
            override fun onNothingSelected(parent: android.widget.AdapterView<*>?) = Unit
        })
    }

    private fun loadProjects() {
        val client = app.client()
        if (client == null) {
            startActivity(Intent(this, ConnectActivity::class.java))
            finish()
            return
        }
        lifecycleScope.launch {
            try {
                val projects = client.projects().ifEmpty { listOf(defaultDirectory()) }
                directory = projects.first()
                projectSpinner.adapter = ArrayAdapter(
                    this@SessionsActivity,
                    android.R.layout.simple_spinner_dropdown_item,
                    projects,
                )
                projectSpinner.setSelection(0)
            } catch (e: Exception) {
                status.text = e.message
                directory = defaultDirectory()
                projectSpinner.adapter = ArrayAdapter(
                    this@SessionsActivity,
                    android.R.layout.simple_spinner_dropdown_item,
                    listOf(directory),
                )
            }
            loadSessions()
        }
    }

    /**
     * The server only knows directories that have had a session. Until one
     * exists, the user can type a path, but an empty value means "all".
     */
    private fun defaultDirectory(): String = app.settings.load()?.let { "" } ?: ""

    private fun loadSessions() {
        val client = app.client() ?: return
        status.text = getString(R.string.loading)
        lifecycleScope.launch {
            try {
                val sessions = client.sessions(directory.ifBlank { null })
                adapter.submit(sessions)
                empty.visibility = if (sessions.isEmpty()) android.view.View.VISIBLE else android.view.View.GONE
                status.text = getString(R.string.session_count, sessions.size)
            } catch (e: Exception) {
                status.text = e.message
            }
        }
    }

    private fun createSession() {
        val client = app.client() ?: return
        val dir = directory.trim()
        if (dir.isEmpty()) {
            status.text = getString(R.string.err_need_directory)
            return
        }
        status.text = getString(R.string.creating_session)
        lifecycleScope.launch {
            try {
                val s = client.createSession(dir, getString(R.string.new_session_title))
                openChat(s)
            } catch (e: Exception) {
                status.text = e.message
            }
        }
    }

    private fun openChat(session: Session) {
        startActivity(
            Intent(this, ChatActivity::class.java)
                .putExtra(ChatActivity.EXTRA_SESSION, session.id)
                .putExtra(ChatActivity.EXTRA_TITLE, session.title)
        )
    }
}

private class SessionAdapter(private val onClick: (Session) -> Unit) :
    RecyclerView.Adapter<SessionAdapter.Holder>() {

    private val items = mutableListOf<Session>()
    private val fmt = SimpleDateFormat("d MMM HH:mm", Locale.getDefault())

    fun submit(list: List<Session>) {
        items.clear()
        items.addAll(list)
        notifyDataSetChanged()
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): Holder {
        val view = android.widget.LinearLayout(parent.context).apply {
            orientation = android.widget.LinearLayout.VERTICAL
            setPadding(32, 24, 32, 24)
            layoutParams = RecyclerView.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
        }
        val title = TextView(parent.context).apply { textSize = 16f }
        val meta = TextView(parent.context).apply {
            textSize = 12f
            setTextColor(0xFF888888.toInt())
        }
        view.addView(title)
        view.addView(meta)
        return Holder(view, title, meta)
    }

    override fun onBindViewHolder(holder: Holder, position: Int) {
        val s = items[position]
        holder.title.text = s.title
        holder.meta.text = if (s.updated > 0) fmt.format(Date(s.updated)) else s.directory
        holder.itemView.setOnClickListener { onClick(s) }
    }

    override fun getItemCount(): Int = items.size

    class Holder(view: android.view.View, val title: TextView, val meta: TextView) :
        RecyclerView.ViewHolder(view)
}
