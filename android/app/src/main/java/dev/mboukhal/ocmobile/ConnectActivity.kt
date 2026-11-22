package dev.mboukhal.ocmobile

import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.launch

/**
 * Entry point: where the server lives and how to authenticate to it.
 *
 * The password is the OpenCode *server* password. Nothing is stored until the
 * server answers `/api/info`, so a typo cannot leave the app pointed at a host
 * that is not an OpenCode server.
 */
class ConnectActivity : AppCompatActivity() {

    private lateinit var originField: EditText
    private lateinit var passwordField: EditText
    private lateinit var clearText: CheckBox
    private lateinit var status: TextView
    private lateinit var connectButton: Button

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_connect)
        title = getString(R.string.connect_title)

        originField = findViewById(R.id.origin)
        passwordField = findViewById(R.id.password)
        clearText = findViewById(R.id.allow_cleartext)
        status = findViewById(R.id.status)
        connectButton = findViewById(R.id.connect)

        val app = application as App
        app.settings.load()?.let {
            originField.setText(it.origin)
            passwordField.setText(it.password)
            clearText.isChecked = it.allowCleartext
        }

        connectButton.setOnClickListener { attemptConnect() }

        findViewById<TextView>(R.id.hint).text = getString(R.string.connect_hint)
    }

    private fun attemptConnect() {
        val rawOrigin = originField.text.toString()
        val password = passwordField.text.toString()
        val allow = clearText.isChecked

        val origin = validateConfig(rawOrigin, password, allow).getOrElse { e ->
            val err = (e as? ConfigException)?.error
            status.text = when (err) {
                ConfigError.EmptyOrigin -> getString(R.string.err_empty_origin)
                ConfigError.EmptyPassword -> getString(R.string.err_empty_password)
                ConfigError.CleartextNotAllowed -> getString(R.string.err_cleartext)
                else -> getString(R.string.err_bad_origin)
            }
            return
        }

        val cfg = ServerConfig(origin, password, allow)
        setBusy(true, getString(R.string.connecting))

        lifecycleScope.launch {
            try {
                val version = OpenCodeClient(cfg).info()
                (application as App).settings.save(cfg)
                setBusy(false, getString(R.string.connected_version, version))
                startActivity(Intent(this@ConnectActivity, SessionsActivity::class.java))
                finish()
            } catch (e: ApiException) {
                setBusy(false, e.message ?: getString(R.string.err_generic))
            } catch (e: Exception) {
                setBusy(false, e.message ?: getString(R.string.err_generic))
            }
        }
    }

    private fun setBusy(busy: Boolean, message: String) {
        connectButton.isEnabled = !busy
        connectButton.text = if (busy) getString(R.string.connecting) else getString(R.string.connect)
        status.text = message
    }
}
