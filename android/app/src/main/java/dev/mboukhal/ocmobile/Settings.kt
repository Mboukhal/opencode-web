package dev.mboukhal.ocmobile

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import java.net.URI

/**
 * Connection settings for the OpenCode server.
 *
 * The password is the OpenCode *server* password (the one `opencode serve`
 * prints or `OPENCODE_SERVER_PASSWORD` sets). It is sent as HTTP basic auth on
 * every request, so it must only ever travel over TLS.
 */
data class ServerConfig(
    val origin: String,
    val password: String,
    /** Allow http:// for a server on the local LAN. Off by default. */
    val allowCleartext: Boolean = false,
)

/** Result of validating what the user typed on the connect screen. */
sealed interface ConfigError {
    data object EmptyOrigin : ConfigError
    data object BadOrigin : ConfigError
    data object EmptyPassword : ConfigError
    data object CleartextNotAllowed : ConfigError
}

/**
 * Validates and normalises a server origin.
 *
 * Returns the canonical origin (scheme://host[:port], no trailing slash) or a
 * typed error. Package-private so it can be unit-tested without Android.
 */
fun validateConfig(origin: String, password: String, allowCleartext: Boolean): Result<String> {
    val trimmed = origin.trim().trimEnd('/')
    if (trimmed.isEmpty()) return Result.failure(ConfigException(ConfigError.EmptyOrigin))
    if (password.isEmpty()) return Result.failure(ConfigException(ConfigError.EmptyPassword))

    val uri = try {
        URI(trimmed)
    } catch (e: Exception) {
        return Result.failure(ConfigException(ConfigError.BadOrigin))
    }
    val scheme = uri.scheme?.lowercase()
    val host = uri.host
    if (scheme == null || host.isNullOrEmpty()) return Result.failure(ConfigException(ConfigError.BadOrigin))

    when (scheme) {
        "https" -> Unit
        "http" -> if (!allowCleartext) return Result.failure(ConfigException(ConfigError.CleartextNotAllowed))
        else -> return Result.failure(ConfigException(ConfigError.BadOrigin))
    }
    // A path in the origin would silently break every request URL.
    if (!uri.path.isNullOrEmpty() && uri.path != "/") return Result.failure(ConfigException(ConfigError.BadOrigin))

    val port = if (uri.port > 0) ":${uri.port}" else ""
    return Result.success("$scheme://$host$port")
}

class ConfigException(val error: ConfigError) : IllegalArgumentException()

/**
 * Persists the connection settings.
 *
 * The password is encrypted with a Keystore-backed master key; the origin and
 * the cleartext flag are ordinary preferences (they are not secrets, and the
 * password field must be readable back into the form for edits).
 */
class SettingsStore(context: Context) {
    private val app = context.applicationContext
    private val prefs: SharedPreferences by lazy {
        val key = MasterKey.Builder(app)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        EncryptedSharedPreferences.create(
            app,
            "server_prefs",
            key,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    fun load(): ServerConfig? {
        val origin = prefs.getString(KEY_ORIGIN, null) ?: return null
        val password = prefs.getString(KEY_PASSWORD, null) ?: return null
        return ServerConfig(origin, password, prefs.getBoolean(KEY_CLEARTEXT, false))
    }

    fun save(cfg: ServerConfig) {
        prefs.edit()
            .putString(KEY_ORIGIN, cfg.origin)
            .putString(KEY_PASSWORD, cfg.password)
            .putBoolean(KEY_CLEARTEXT, cfg.allowCleartext)
            .apply()
    }

    fun clear() {
        prefs.edit().clear().apply()
    }

    private companion object {
        const val KEY_ORIGIN = "origin"
        const val KEY_PASSWORD = "password"
        const val KEY_CLEARTEXT = "cleartext"
    }
}
